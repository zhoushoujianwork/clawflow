package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/agentcapacity"
	"github.com/zhoushoujianwork/clawflow/internal/housekeeping"
	offload "github.com/zhoushoujianwork/offload/pkg/execution"
)

var (
	runEmbeddedOffload = func(ctx context.Context, client *offload.Client, req offload.Request) (offload.Result, error) {
		return client.Run(ctx, req)
	}
	writeLibraryResult = writeResultFile
	libraryWarning     = func(message string) { _, _ = fmt.Fprintln(os.Stderr, "Warning: "+message) }
)

func runLibrary(ctx context.Context, c Config, req Request, env []string, events io.Writer, observe func(Observation)) (result Result, runErr error) {
	defer func() {
		result.CallerRunID = req.RequestID
		result.Machine, _ = os.Hostname()
		if result.Profile == "" {
			result.Profile = c.Profile
		}
		if result.ErrorKind == "" && runErr != nil {
			var e *Error
			switch {
			case errors.Is(runErr, context.DeadlineExceeded):
				result.ErrorKind = "timeout"
			case errors.Is(runErr, context.Canceled):
				result.ErrorKind = "canceled"
			case errors.As(runErr, &e):
				result.ErrorKind = e.Kind
			default:
				result.ErrorKind = "execution"
			}
		}
	}()
	if err := validateRequest(req); err != nil {
		return result, err
	}
	if err := ValidateConfig(c); err != nil {
		return result, err
	}
	var err error
	env, err = desiredEnvironment(c, req, env)
	if err != nil {
		return result, err
	}
	profileConfig, err := loadLibraryProfile(c.Profile)
	if err != nil {
		return result, errors.Join(&Error{Kind: "configuration"}, fmt.Errorf("load Offload profile: %w", err))
	}
	c.Profile = profileConfig.ProfileName
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	canonical, err := filepath.EvalSymlinks(req.Workspace)
	if err != nil {
		return result, err
	}
	req.Workspace = canonical
	checkpointPath, checkpoint, err := recoveryCheckpoint(ctx, c, &req)
	if err != nil {
		return result, err
	}
	if err := validateRequest(req); err != nil {
		return result, err
	}
	var checkpointFile *os.File
	var checkpointErr error
	if checkpoint != nil {
		checkpointFile, err = checkpointLock(checkpointPath)
		if err != nil {
			return result, err
		}
		defer checkpointFile.Close()
		if checkpoint.ObservationOnly {
			if _, statErr := os.Lstat(checkpointPath); !os.IsNotExist(statErr) {
				if statErr != nil {
					return result, statErr
				}
				return result, errors.New("observation checkpoint already exists; reconcile the original invocation")
			}
		}
		if err := writeCheckpoint(checkpointPath, *checkpoint); err != nil {
			return result, err
		}
		originalObserve := observe
		observe = func(o Observation) {
			if o.NativeSessionID != "" {
				checkpoint.Session = o.NativeSessionID
			}
			if o.Backend != "" {
				if checkpoint.Backend != "" && checkpoint.Backend != o.Backend {
					checkpointErr = &Error{Kind: "backend_mismatch"}
					return
				}
				checkpoint.Backend = o.Backend
			}
			if o.RunID != "" {
				checkpoint.RunID = o.RunID
			}
			if err := writeCheckpoint(checkpointPath, *checkpoint); err != nil {
				checkpointErr = err
			}
			if originalObserve != nil {
				originalObserve(o)
			}
		}
	}
	releaseSlot, err := agentcapacity.Acquire(ctx)
	if err != nil {
		return result, fmt.Errorf("acquire agent capacity: %w", err)
	}
	defer releaseSlot()
	release, err := acquireOwnership(req.Workspace, c.Profile, req.SessionKey, req.Resume)
	if err != nil {
		return result, err
	}
	defer release()
	releaseMaintenance, err := housekeeping.Gate(ctx, os.TempDir(), false)
	if err != nil {
		return result, err
	}
	defer releaseMaintenance()

	artifactParent := ""
	if checkpoint != nil {
		artifactParent, err = filepath.Abs(filepath.Join(filepath.Dir(checkpointPath), "execution-artifacts"))
		if err != nil {
			return result, err
		}
		if err = os.MkdirAll(artifactParent, 0700); err != nil {
			return result, err
		}
	}
	dir, err := os.MkdirTemp(artifactParent, "clawflow-offload-*")
	if err != nil {
		return result, err
	}
	if checkpoint == nil {
		defer os.RemoveAll(dir)
	} else {
		checkpoint.ArtifactDir = dir
		if err := writeCheckpoint(checkpointPath, *checkpoint); err != nil {
			return result, err
		}
	}
	instructions := filepath.Join(dir, "instructions.md")
	if err := os.WriteFile(instructions, []byte(req.Instructions), 0600); err != nil {
		return result, err
	}
	if redact(req.Prompt, env) != req.Prompt || redact(req.Instructions, env) != req.Instructions {
		return result, errors.New("Offload request contains an environment secret")
	}

	client, err := offload.New(profileConfig)
	if err != nil {
		return result, errors.Join(&Error{Kind: "configuration"}, err)
	}
	delta := environmentDelta(os.Environ(), env)
	timeout := time.Duration(req.TimeoutMS) * time.Millisecond
	libReq := offload.Request{
		ProcessEnvironment: &delta,
		CallerRunID:        req.RequestID,
		Caller:             "clawflow",
		Task:               req.Prompt,
		Directory:          req.Workspace,
		Model:              req.Model,
		InstructionsFile:   instructions,
		Write:              req.PermissionMode == "bypassPermissions",
		ToolsDisabled:      req.ToolsDisabled,
		AllowedTools:       strings.Join(req.AllowedTools, ","),
		DisallowedTools:    strings.Join(req.DisallowedTools, ","),
		Timeout:            &timeout,
	}
	if req.Resume {
		libReq.NativeSessionID = req.SessionKey
	}
	if req.PermissionMode == "bypassPermissions" {
		libReq.ExecutionPolicy = "managed-development"
		libReq.CodexSandbox = c.CodexSandbox
	}

	tail := newEventTail("", req, events, env, observe)
	progress := ProgressFrom(ctx)
	progress.begin()
	defer progress.end()
	var eventErr error
	libReq.OnRunEvent = func(event offload.RunEvent) error {
		raw, marshalErr := json.Marshal(event)
		if marshalErr != nil {
			eventErr = &Error{Kind: "events_io"}
			return eventErr
		}
		if eventErr = tail.accept(raw); eventErr == nil {
			progress.event()
			eventErr = checkpointErr
		}
		return eventErr
	}

	sink := transcriptSink(ctx)
	if sink != nil {
		libReq.Monitor = &offload.MonitorOptions{CaptureTranscript: true}
		if end := sink.Begin(TranscriptTarget{CallerRunID: req.RequestID, Env: env, Capture: true}); end != nil {
			defer func() { end(result, runErr) }()
		}
	}

	packageResult, packageErr := callEmbeddedOffload(ctx, client, libReq)
	if runErrorKind(packageErr) == "monitor" && libReq.Monitor != nil {
		libraryWarning("Offload transcript monitor failed before native launch; retrying without transcript capture")
		libReq.Monitor = nil
		packageResult, packageErr = callEmbeddedOffload(ctx, client, libReq)
	}
	if len(packageResult.RawRecord) > MaxOutputBytes {
		return result, &Error{Kind: "protocol_output_limit"}
	}
	if err := writeLibraryResult(filepath.Join(dir, "result.json"), packageResult.RawRecord); err != nil {
		return result, errors.Join(&Error{Kind: "result_write"}, err)
	}
	if len(packageResult.RawRecord) == 0 || len(packageResult.RawRecord) > MaxOutputBytes || !validJSON(packageResult.RawRecord) || json.Unmarshal(packageResult.RawRecord, &result) != nil {
		return result, &Error{Kind: "protocol_invalid_result"}
	}
	result.Text = redact(packageResult.Report, env)
	result.Error = redact(packageResult.Error, env)
	result.SessionKey = result.NativeSessionID
	if result.Model == "" {
		result.Model = packageResult.Model
	}
	if packageErr != nil && libraryStartFailure(result.ErrorKind) {
		return result, &Error{Kind: safeKind(result.ErrorKind)}
	}
	if tail.runID != "" {
		if checkpoint != nil {
			checkpoint.Session, checkpoint.Backend, checkpoint.RunID = tail.session, tail.backend, tail.runID
			if err := writeCheckpoint(checkpointPath, *checkpoint); err != nil {
				return result, err
			}
		}
	}
	if eventErr != nil {
		return result, eventErr
	}
	if tail.seq > 0 {
		if err := tail.finishStream(result); err != nil {
			return result, err
		}
	}
	if !idPattern.MatchString(result.RunID) || result.CallerRunID != req.RequestID || result.Profile != c.Profile || (result.Status == "ok" && !idPattern.MatchString(result.NativeSessionID)) || (result.NativeSessionID != "" && !idPattern.MatchString(result.NativeSessionID)) || (req.Resume && result.NativeSessionID != "" && result.NativeSessionID != req.SessionKey) || result.ExitCode != packageResult.ExitCode || (result.Status != "ok" && result.Status != "error" && result.Status != "timeout") || (result.Status == "ok" && (result.ExitCode != 0 || result.ErrorKind != "" || result.Error != "")) || (result.Status != "ok" && result.ExitCode == 0) {
		return result, &Error{Kind: "protocol_invalid_result"}
	}
	wantPolicy := "default"
	if req.PermissionMode == "bypassPermissions" {
		wantPolicy = "managed-development"
	}
	if result.ExecutionPolicy != wantPolicy {
		return result, &Error{Kind: "protocol_policy_mismatch"}
	}
	if checkpoint != nil {
		checkpoint.Session, checkpoint.Backend, checkpoint.RunID = result.NativeSessionID, result.Backend, result.RunID
		checkpoint.Completed = result.Status == "ok"
		if err := writeCheckpoint(checkpointPath, *checkpoint); err != nil {
			return result, err
		}
	}
	if runErrorKind(packageErr) == "cleanup" && result.Status == "ok" && result.ExitCode == 0 {
		return result, nil
	}
	if packageErr != nil || result.Status != "ok" {
		if errors.Is(packageErr, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			return result, context.Canceled
		}
		if errors.Is(packageErr, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) || result.ErrorKind == "timeout" {
			return result, context.DeadlineExceeded
		}
		kind := safeKind(result.ErrorKind)
		if kind == "execution" && capacityMessage.MatchString(result.Error) {
			kind = "capacity"
		}
		return result, &Error{Kind: kind}
	}
	return result, nil
}

func callEmbeddedOffload(ctx context.Context, client *offload.Client, req offload.Request) (offload.Result, error) {
	runCtx, cancelRun := context.WithCancel(ctx)
	done := make(chan struct{})
	activeRun := registerActiveLibraryRun(cancelRun, done)
	result, err := runEmbeddedOffload(runCtx, client, req)
	close(done)
	unregisterActiveLibraryRun(activeRun)
	cancelRun()
	return result, err
}

func runErrorKind(err error) string {
	var runErr *offload.RunError
	if errors.As(err, &runErr) {
		return runErr.Kind
	}
	return ""
}

func libraryStartFailure(kind string) bool {
	switch kind {
	case "configuration", "unsupported_tools", "launch", "globally_disabled":
		return true
	default:
		return false
	}
}

func loadLibraryProfile(name string) (offload.Config, error) {
	path := offload.DefaultConfigPath()
	if name == "" {
		var err error
		name, err = offload.DefaultProfile(path)
		if err != nil {
			return offload.Config{}, err
		}
	}
	return offload.LoadConfig(path, name)
}

func environmentDelta(base, desired []string) offload.ProcessEnvironment {
	baseValues := environmentValues(base)
	desiredValues := environmentValues(desired)
	delta := offload.ProcessEnvironment{}
	for key, value := range desiredValues {
		if current, ok := baseValues[key]; !ok || current != value {
			if delta.Set == nil {
				delta.Set = make(map[string]string)
			}
			delta.Set[key] = value
		}
	}
	for key := range baseValues {
		if _, ok := desiredValues[key]; !ok {
			delta.Unset = append(delta.Unset, key)
		}
	}
	sort.Strings(delta.Unset)
	return delta
}

func environmentValues(env []string) map[string]string {
	values := make(map[string]string, len(env))
	for _, item := range env {
		if key, value, ok := strings.Cut(item, "="); ok {
			values[key] = value
		}
	}
	return values
}

func writeResultFile(path string, raw []byte) error {
	if len(raw) == 0 || len(raw) > MaxOutputBytes || !validJSON(raw) {
		return errors.New("Offload result record is missing or invalid")
	}
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".result-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(raw)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
