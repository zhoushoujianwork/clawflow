package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	offload "github.com/zhoushoujianwork/offload/pkg/execution"
)

func libraryFixture(t *testing.T, script string) (Config, Request) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("OFFLOAD_MAX_WORKERS", "-1")
	fake := filepath.Join(root, "claude")
	if runtime.GOOS == "windows" {
		t.Skip("shell fake backend is Unix-only")
	}
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"+script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OFFLOAD_CLAUDE", fake)
	configDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "offload")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	t.Cleanup(server.Close)
	raw := []byte(`{"default":"fixture","profiles":{"fixture":{"backend":"claude","base_url":"` + server.URL + `","model":"profile-model","token":"fixture-token"}}}`)
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	return Config{Engine: EngineLibrary, Profile: "fixture"}, Request{Version: 1, RequestID: NewSessionKey(), Workspace: t.TempDir(), Prompt: "task", Instructions: "private context", PermissionMode: "default", TimeoutMS: 15000}
}

func TestLibraryEngineSuccessUsesReportAndValidatedEvents(t *testing.T) {
	c, req := libraryFixture(t, `cat >/dev/null
printf '%s\n' '{"type":"system","subtype":"init","session_id":"native-actual","model":"fixture"}'
printf '%s\n' '{"type":"assistant","message":{"content":[{"type":"text","text":"progress must not become result"}]}}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"session_id":"native-actual","result":"Final answer"}'
`)
	var evidence bytes.Buffer
	result, err := Run(context.Background(), c, req, nil, &evidence, nil)
	if err != nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if result.Text != "Final answer" || result.NativeSessionID != "native-actual" || result.Profile != "fixture" || result.ExecutionPolicy != "default" {
		t.Fatalf("result=%+v", result)
	}
	if strings.Contains(evidence.String(), "progress must not become result") || !strings.Contains(evidence.String(), `"type":"final"`) {
		t.Fatalf("evidence=%s", evidence.String())
	}
}

func TestEngineParitySuccessResultAndEventSequence(t *testing.T) {
	cliConfig, cliRequest := fixture(t)
	cliConfig.Profile = "fixture"
	cliRequest.PermissionMode = "default"
	t.Setenv("OFFLOAD_TEST_TEXT", "Final answer")
	var cliEvents bytes.Buffer
	cliResult, cliErr := Run(context.Background(), cliConfig, cliRequest, nil, &cliEvents, nil)
	if cliErr != nil {
		t.Fatal(cliErr)
	}
	libraryConfig, libraryRequest := libraryFixture(t, `cat >/dev/null
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"session_id":"native-actual","result":"Final answer"}'
`)
	libraryRequest.RequestID = cliRequest.RequestID
	var libraryEvents bytes.Buffer
	libraryResult, libraryErr := Run(context.Background(), libraryConfig, libraryRequest, nil, &libraryEvents, nil)
	if libraryErr != nil {
		t.Fatal(libraryErr)
	}
	project := func(r Result) []any {
		return []any{r.Status, r.Text, r.Model, r.NativeSessionID, r.Profile, r.Backend, r.CallerRunID, r.ExecutionPolicy, r.ExitCode, r.ErrorKind, r.Error}
	}
	if !reflect.DeepEqual(project(cliResult), project(libraryResult)) {
		t.Fatalf("cli=%+v library=%+v", cliResult, libraryResult)
	}
	eventTypes := func(raw []byte) []string {
		var types []string
		for _, line := range bytes.Split(bytes.TrimSpace(raw), []byte("\n")) {
			var event Event
			if json.Unmarshal(line, &event) == nil {
				types = append(types, event.Type)
			}
		}
		return types
	}
	if cli, library := eventTypes(cliEvents.Bytes()), eventTypes(libraryEvents.Bytes()); !reflect.DeepEqual(cli, library) {
		t.Fatalf("cli events=%v library events=%v", cli, library)
	}
}

func TestLibraryCheckpointPersistsEngineRunAndDurableResult(t *testing.T) {
	c, req := libraryFixture(t, `cat >/dev/null
printf '%s\n' '{"type":"system","subtype":"init","session_id":"native-actual","model":"fixture"}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"session_id":"native-actual","result":"Final answer"}'
`)
	req.PermissionMode = "bypassPermissions"
	checkpoint := filepath.Join(t.TempDir(), "execution.json")
	result, err := Run(WithCheckpoint(context.Background(), checkpoint, false), c, req, nil, nil, nil)
	if err != nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	cp, err := ReadCheckpoint(checkpoint)
	if err != nil || cp.Engine != EngineLibrary || cp.Command != "" || cp.RunID != result.RunID || !cp.Completed {
		t.Fatalf("checkpoint=%+v err=%v", cp, err)
	}
	path := filepath.Join(cp.ArtifactDir, "result.json")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("result file=%v err=%v", info, err)
	}
	var persisted Result
	raw, _ := os.ReadFile(path)
	if json.Unmarshal(raw, &persisted) != nil || persisted.RunID != result.RunID || persisted.Text != result.Text {
		t.Fatalf("persisted=%+v raw=%s", persisted, raw)
	}
}

func TestLibraryDefaultProfileAndResume(t *testing.T) {
	c, req := libraryFixture(t, `cat >/dev/null
printf '%s\n' '{"type":"system","subtype":"init","session_id":"native-actual","model":"fixture"}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"session_id":"native-actual","result":"resumed"}'
`)
	c.Profile = ""
	req.Resume = true
	req.SessionKey = "native-actual"
	result, err := Run(context.Background(), c, req, nil, nil, nil)
	if err != nil || result.Profile != "fixture" || result.NativeSessionID != req.SessionKey || result.Text != "resumed" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestLibraryMissingProfileIsConfigurationFailure(t *testing.T) {
	c, req := libraryFixture(t, "exit 99\n")
	c.Profile = "missing"
	result, err := Run(context.Background(), c, req, nil, nil, nil)
	if err == nil || !IsKind(err, "configuration") || result.ErrorKind != "configuration" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestLibraryNativeFailureClassification(t *testing.T) {
	c, req := libraryFixture(t, `cat >/dev/null
printf '%s\n' '{"type":"system","subtype":"init","session_id":"native-actual","model":"profile-model"}'
printf '%s\n' '{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":"native-actual","result":"boom"}'
`)
	result, err := Run(context.Background(), c, req, nil, nil, nil)
	if err == nil || !IsKind(err, "execution") || result.Status != "error" || result.Text != "boom" || result.ErrorKind != "execution" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestLibraryTimeoutAndCancellation(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "timeout", true: "canceled"}[canceled], func(t *testing.T) {
			c, req := libraryFixture(t, `cat >/dev/null
printf '%s\n' '{"type":"system","subtype":"init","session_id":"native-actual","model":"profile-model"}'
exec sleep 30
`)
			req.TimeoutMS = 80
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if canceled {
				time.AfterFunc(30*time.Millisecond, cancel)
			}
			result, err := Run(ctx, c, req, nil, nil, nil)
			want := context.DeadlineExceeded
			if canceled {
				want = context.Canceled
			}
			if !errors.Is(err, want) {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestLibraryGlobalDisableHasDistinctReason(t *testing.T) {
	c, req := libraryFixture(t, "exit 99\n")
	path := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "offload", "config.json")
	var document map[string]any
	raw, _ := os.ReadFile(path)
	if json.Unmarshal(raw, &document) != nil {
		t.Fatal("invalid fixture config")
	}
	document["disabled"] = true
	raw, _ = json.Marshal(document)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	result, err := Run(context.Background(), c, req, nil, nil, nil)
	if err == nil || !IsKind(err, "globally_disabled") || result.ErrorKind != "globally_disabled" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestLibraryTranscriptUsesMonitorWithoutCLIProbe(t *testing.T) {
	c, req := libraryFixture(t, `cat >/dev/null
printf '%s\n' '{"type":"system","subtype":"init","session_id":"native-actual","model":"profile-model"}'
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"session_id":"native-actual","result":"Final answer"}'
`)
	sink := &recordingSink{}
	result, err := Run(WithTranscriptSink(context.Background(), sink), c, req, nil, nil, nil)
	if err != nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(sink.targets) != 1 || !sink.targets[0].Capture || sink.targets[0].Command != "" || len(sink.ended) != 1 || sink.ended[0] != nil {
		t.Fatalf("sink=%+v", sink)
	}
}

func TestLibraryMonitorFailureRetriesWithoutCapture(t *testing.T) {
	c, req := libraryFixture(t, `cat >/dev/null
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"session_id":"native-actual","result":"Final answer"}'
`)
	sink := &recordingSink{}
	oldRun, oldWarning := runEmbeddedOffload, libraryWarning
	t.Cleanup(func() { runEmbeddedOffload, libraryWarning = oldRun, oldWarning })
	calls := 0
	var warning string
	libraryWarning = func(message string) { warning = message }
	runEmbeddedOffload = func(ctx context.Context, client *offload.Client, request offload.Request) (offload.Result, error) {
		calls++
		if calls == 1 {
			if request.Monitor == nil {
				t.Fatal("first attempt omitted monitor")
			}
			return offload.Result{ErrorKind: "monitor"}, &offload.RunError{Kind: "monitor", Status: "error", ExitCode: 2}
		}
		if request.Monitor != nil {
			t.Fatal("monitor retry retained monitor")
		}
		return oldRun(ctx, client, request)
	}
	result, err := Run(WithTranscriptSink(context.Background(), sink), c, req, nil, nil, nil)
	if err != nil || result.Status != "ok" || calls != 2 || !strings.Contains(warning, "retrying without transcript capture") {
		t.Fatalf("result=%+v err=%v calls=%d warning=%q", result, err, calls, warning)
	}
}

func cannedLibraryResult(t *testing.T, request offload.Request, status string, exit int, kind, cleanup, report string) offload.Result {
	t.Helper()
	record := Result{Status: status, Text: report, Model: "profile-model", NativeSessionID: "native-actual", Profile: "fixture", Backend: "claude",
		RunID: "run-canned", CallerRunID: request.CallerRunID, ExecutionPolicy: "default", ExitCode: exit, ErrorKind: kind, Cleanup: cleanup}
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return offload.Result{RunID: record.RunID, CallerRunID: record.CallerRunID, Profile: record.Profile, Backend: record.Backend, Model: record.Model,
		NativeSessionID: record.NativeSessionID, Status: status, ErrorKind: kind, Cleanup: cleanup, Report: report, ExitCode: exit, RawRecord: raw}
}

func TestLibraryCleanupErrorKeepsSuccessfulResult(t *testing.T) {
	for _, status := range []string{"ok", "error"} {
		t.Run(status, func(t *testing.T) {
			c, req := libraryFixture(t, "exit 99\n")
			oldRun := runEmbeddedOffload
			t.Cleanup(func() { runEmbeddedOffload = oldRun })
			runEmbeddedOffload = func(_ context.Context, _ *offload.Client, request offload.Request) (offload.Result, error) {
				exit, kind := 0, ""
				if status == "error" {
					exit, kind = 1, "execution"
				}
				result := cannedLibraryResult(t, request, status, exit, kind, "native process cleanup remains unresolved", "report")
				return result, &offload.RunError{Kind: "cleanup", Status: status, ExitCode: exit}
			}
			result, err := Run(context.Background(), c, req, nil, nil, nil)
			if status == "ok" {
				if err != nil || result.Cleanup == "" || result.Status != "ok" {
					t.Fatalf("result=%+v err=%v", result, err)
				}
			} else if err == nil || !IsKind(err, "execution") || result.Status != "error" {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}
}

func TestLibraryOversizedResultUsesProtocolOutputLimit(t *testing.T) {
	c, req := libraryFixture(t, "exit 99\n")
	oldRun := runEmbeddedOffload
	t.Cleanup(func() { runEmbeddedOffload = oldRun })
	runEmbeddedOffload = func(_ context.Context, _ *offload.Client, request offload.Request) (offload.Result, error) {
		result := cannedLibraryResult(t, request, "ok", 0, "", "", strings.Repeat("x", MaxOutputBytes))
		return result, nil
	}
	_, err := Run(context.Background(), c, req, nil, nil, nil)
	if !IsKind(err, "protocol_output_limit") {
		t.Fatalf("err=%v", err)
	}
}

func TestLibraryResultWriteFailureKeepsSafeKind(t *testing.T) {
	c, req := libraryFixture(t, "exit 99\n")
	oldRun, oldWrite := runEmbeddedOffload, writeLibraryResult
	t.Cleanup(func() { runEmbeddedOffload, writeLibraryResult = oldRun, oldWrite })
	runEmbeddedOffload = func(_ context.Context, _ *offload.Client, request offload.Request) (offload.Result, error) {
		return cannedLibraryResult(t, request, "ok", 0, "", "", "report"), nil
	}
	writeLibraryResult = func(string, []byte) error { return errors.New("fixture write failure") }
	_, err := Run(context.Background(), c, req, nil, nil, nil)
	if !IsKind(err, "result_write") || safeKind("result_write") != "result_write" {
		t.Fatalf("err=%v", err)
	}
}

func TestEnvironmentDelta(t *testing.T) {
	got := environmentDelta(
		[]string{"KEEP=same", "OVERRIDE=old", "REMOVE=yes", "CLAWFLOW_STATE_DIR=/private", "CLAWFLOW_TEST_DATABASE_DSN=old"},
		[]string{"KEEP=same", "OVERRIDE=new", "ADD=value", "CLAWFLOW_TEST_DATABASE_DSN=replacement"},
	)
	wantSet := map[string]string{"OVERRIDE": "new", "ADD": "value", "CLAWFLOW_TEST_DATABASE_DSN": "replacement"}
	if !reflect.DeepEqual(got.Set, wantSet) || !reflect.DeepEqual(got.Unset, []string{"CLAWFLOW_STATE_DIR", "REMOVE"}) {
		t.Fatalf("delta=%+v", got)
	}
}

func TestDesiredEnvironmentUsesExistingFilters(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "")
	got, err := desiredEnvironment(Config{TestDatabaseDSN: "postgres://replacement"}, Request{PermissionMode: "bypassPermissions"}, []string{
		"PATH=/bin", "CLAWFLOW_STATE_DIR=/private", "CLAWFLOW_TEST_DATABASE_DSN=postgres://old",
	})
	if err != nil {
		t.Fatal(err)
	}
	values := environmentValues(got)
	if _, ok := values["CLAWFLOW_STATE_DIR"]; ok {
		t.Fatal("CLAWFLOW_STATE_DIR was retained")
	}
	if values["CLAWFLOW_TEST_DATABASE_DSN"] != "postgres://replacement" {
		t.Fatalf("environment=%v", values)
	}
}

func TestWriteResultFileAtomicAndPrivate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "result.json")
	raw, _ := json.Marshal(map[string]string{"status": "ok"})
	if err := writeResultFile(path, raw); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("mode=%v err=%v", info, err)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, raw) {
		t.Fatalf("got=%s want=%s", got, raw)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "result.json" {
		t.Fatalf("leftover files: %v", entries)
	}
	if err := writeResultFile(filepath.Join(dir, "bad.json"), []byte("no")); err == nil {
		t.Fatal("invalid result accepted")
	}
}

func TestLibraryEnvironmentConflictIsStartFailure(t *testing.T) {
	c, req := libraryFixture(t, "exit 99\n")
	desired := os.Environ()
	filtered := desired[:0]
	for _, item := range desired {
		if !strings.HasPrefix(item, "OFFLOAD_CLAUDE=") {
			filtered = append(filtered, item)
		}
	}
	result, err := Run(context.Background(), c, req, filtered, nil, nil)
	if err == nil || !IsKind(err, "configuration") || result.ErrorKind != "configuration" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatal("configuration failure mapped as cancellation")
	}
}
