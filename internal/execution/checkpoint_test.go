package execution

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/filelock"
)

func TestRecoveryCheckpointPinsSessionWorkspaceAndProfile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "execution.json")
	cp := Checkpoint{Version: 1, Workspace: root, Profile: "fixture", Command: "/fixture/offload", Session: "native-1"}
	if err := writeCheckpoint(path, cp); err != nil {
		t.Fatal(err)
	}
	req := Request{Workspace: root, PermissionMode: "bypassPermissions", Prompt: "Continue"}
	ctx := WithCheckpoint(context.Background(), path, true)
	_, _, err := recoveryCheckpoint(ctx, Config{Profile: "fixture", Command: cp.Command}, &req)
	if err != nil || !req.Resume || req.SessionKey != "native-1" {
		t.Fatal(req, err)
	}
	for _, change := range []string{"workspace", "profile", "completed"} {
		altered := cp
		config := Config{Profile: cp.Profile, Command: cp.Command}
		request := Request{Workspace: root, PermissionMode: "bypassPermissions"}
		switch change {
		case "workspace":
			request.Workspace = root + "/other"
		case "profile":
			config.Profile = "other"
		case "completed":
			altered.Completed = true
		}
		if err := writeCheckpoint(path, altered); err != nil {
			t.Fatal(err)
		}
		if _, _, err := recoveryCheckpoint(ctx, config, &request); err == nil {
			t.Fatal("unsafe recovery accepted", change)
		}
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("checkpoint not private", err)
	}
}

func TestCheckpointPersistsLiveSession(t *testing.T) {
	c, req, _ := streamingFixture(t)
	req.PermissionMode = "bypassPermissions"
	req.TimeoutMS = 10000
	path := filepath.Join(t.TempDir(), "execution.json")
	ctx, cancel := context.WithCancel(WithCheckpoint(context.Background(), path, false))
	defer cancel()
	observed := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		_, err := Run(ctx, c, req, nil, nil, func(Observation) {
			select {
			case observed <- struct{}{}:
			default:
			}
		})
		done <- err
	}()
	select {
	case <-observed:
	case err := <-done:
		t.Fatal("ended before checkpoint", err)
	case <-time.After(8 * time.Second):
		t.Fatal("missing checkpoint")
	}
	cp, err := ReadCheckpoint(path)
	if err != nil || cp.Session != "native-actual" || cp.Completed || !filelock.Held(path+".lock") {
		t.Fatal(cp, err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	cp, err = ReadCheckpoint(path)
	if err != nil || cp.Completed || filelock.Held(path+".lock") {
		t.Fatal("interrupted checkpoint lost or ownership leaked", cp, err)
	}
}

func TestCheckpointExecutorHelper(t *testing.T) {
	if os.Getenv("CLAWFLOW_CHECKPOINT_HELPER") != "1" {
		return
	}
	ctx := WithCheckpoint(context.Background(), os.Getenv("CLAWFLOW_CHECKPOINT_PATH"), false)
	observation := os.Getenv("CLAWFLOW_OBSERVATION_HELPER") == "1"
	if observation {
		ctx = WithObservationCheckpoint(context.Background(), os.Getenv("CLAWFLOW_CHECKPOINT_PATH"))
	}
	_, err := Run(ctx, Config{Command: os.Getenv("CLAWFLOW_CHECKPOINT_COMMAND"), Profile: "fixture"}, Request{Version: 1, RequestID: "caller", Workspace: os.Getenv("CLAWFLOW_CHECKPOINT_WORKSPACE"), PermissionMode: "bypassPermissions", ToolsDisabled: observation, TimeoutMS: 15000}, nil, nil, nil)
	if err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestCheckpointLockSurvivesExecutorCrash(t *testing.T) {
	t.Run("development", func(t *testing.T) { checkpointLockSurvivesExecutorCrash(t, false) })
	t.Run("observation", func(t *testing.T) { checkpointLockSurvivesExecutorCrash(t, true) })
}

func checkpointLockSurvivesExecutorCrash(t *testing.T, observation bool) {
	c, req, gate := streamingFixture(t)
	path := filepath.Join(t.TempDir(), "execution.json")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	helper := exec.Command(executable, "-test.run=^TestCheckpointExecutorHelper$")
	helper.Env = append(os.Environ(), "CLAWFLOW_CHECKPOINT_HELPER=1", "CLAWFLOW_CHECKPOINT_PATH="+path, "CLAWFLOW_CHECKPOINT_COMMAND="+c.Command, "CLAWFLOW_CHECKPOINT_WORKSPACE="+req.Workspace)
	if observation {
		helper.Env = append(helper.Env, "CLAWFLOW_OBSERVATION_HELPER=1")
	}
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.WriteFile(gate, nil, 0600); _ = helper.Process.Kill(); _ = helper.Wait() }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		cp, err := ReadCheckpoint(path)
		if err == nil && cp.Session != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper did not publish session")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := helper.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = helper.Wait()
	if !filelock.Held(path + ".lock") {
		t.Fatal("Offload lost ownership when its executor died")
	}
	if err := os.WriteFile(gate, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for filelock.Held(path + ".lock") {
		if time.Now().After(deadline) {
			t.Fatal("Offload ownership did not release on completion")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCheckpointResumeUsesNativeSession(t *testing.T) {
	c, req, gate := streamingFixture(t)
	req.PermissionMode = "bypassPermissions"
	req.TimeoutMS = 10000
	canonical, err := filepath.EvalSymlinks(req.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "execution.json")
	if err := writeCheckpoint(path, Checkpoint{Version: 1, Workspace: canonical, Profile: c.Profile, Command: c.Command, Session: "native-actual", Backend: "claude"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAWFLOW_EXPECT_RESUME", "native-actual")
	if err := os.WriteFile(gate, nil, 0600); err != nil {
		t.Fatal(err)
	}
	result, err := Run(WithCheckpoint(context.Background(), path, true), c, req, nil, nil, nil)
	if err != nil || result.NativeSessionID != "native-actual" {
		t.Fatal(result, err)
	}
	cp, err := ReadCheckpoint(path)
	if err != nil || !cp.Completed {
		t.Fatal(cp, err)
	}
}

func TestObservationCheckpointRetainsIdentityAndRefusesReuse(t *testing.T) {
	c, req, gate := streamingFixture(t)
	req.ToolsDisabled = true
	path := filepath.Join(t.TempDir(), "preflight.json")
	if err := os.WriteFile(gate, nil, 0600); err != nil {
		t.Fatal(err)
	}
	ctx := WithObservationCheckpoint(context.Background(), path)
	result, err := Run(ctx, c, req, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	cp, err := ReadCheckpoint(path)
	if err != nil || !cp.Completed || !cp.ObservationOnly || cp.RunID != result.RunID || cp.Session != result.NativeSessionID || cp.CallerRunID != req.RequestID {
		t.Fatalf("checkpoint=%+v err=%v", cp, err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Run(ctx, c, req, nil, nil, nil); err == nil {
		t.Fatal("observation path reused")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(original) {
		t.Fatal("original identity overwritten")
	}
	// Observation metadata must not authorize development/session recovery.
	cp.Completed = false
	if err = writeCheckpoint(path, cp); err != nil {
		t.Fatal(err)
	}
	req.PermissionMode = "bypassPermissions"
	req.ToolsDisabled = false
	if _, _, err = recoveryCheckpoint(WithCheckpoint(context.Background(), path, true), c, &req); err == nil {
		t.Fatal("read-only checkpoint resumed as development")
	}
}

func TestObservationCheckpointRejectsToolsAndResume(t *testing.T) {
	for _, mode := range []string{"tools", "resume"} {
		t.Run(mode, func(t *testing.T) {
			c, req, _ := streamingFixture(t)
			req.ToolsDisabled = mode != "tools"
			req.Resume = mode == "resume"
			if _, _, err := recoveryCheckpoint(WithObservationCheckpoint(context.Background(), filepath.Join(t.TempDir(), "execution.json")), c, &req); err == nil {
				t.Fatal("unsafe observation accepted")
			}
		})
	}
}

func TestObservationCheckpointDoesNotReplaceDanglingLink(t *testing.T) {
	c, req, _ := streamingFixture(t)
	req.ToolsDisabled = true
	path := filepath.Join(t.TempDir(), "execution.json")
	if err := os.Symlink("missing-evidence.json", path); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(WithObservationCheckpoint(context.Background(), path), c, req, nil, nil, nil); err == nil {
		t.Fatal("existing evidence path replaced")
	}
	target, err := os.Readlink(path)
	if err != nil || target != "missing-evidence.json" {
		t.Fatal(target, err)
	}
}

func TestCheckpointArtifactsSurviveTemporaryCleanupAndResume(t *testing.T) {
	c, req, gate := streamingFixture(t)
	req.PermissionMode = "bypassPermissions"
	req.TimeoutMS = 10000
	req.Prompt = "retained task input"
	req.Instructions = "retained operator instructions"
	checkpoint := filepath.Join(t.TempDir(), "execution.json")
	ephemeral := t.TempDir()
	t.Setenv("TMPDIR", ephemeral)
	ctx, cancel := context.WithCancel(WithCheckpoint(context.Background(), checkpoint, false))
	defer cancel()
	_, err := Run(ctx, c, req, nil, nil, func(o Observation) {
		if o.NativeSessionID != "" {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("interruption: %v", err)
	}
	before, err := ReadCheckpoint(checkpoint)
	if err != nil || before.Completed || before.Session != "native-actual" {
		t.Fatalf("checkpoint: %+v %v", before, err)
	}
	expectedParent := filepath.Join(filepath.Dir(checkpoint), "execution-artifacts")
	if filepath.Dir(before.ArtifactDir) != expectedParent {
		t.Fatalf("durable evidence in temporary storage: %s", before.ArtifactDir)
	}
	for _, p := range []string{expectedParent, before.ArtifactDir} {
		info, err := os.Stat(p)
		if err != nil || info.Mode().Perm() != 0700 {
			t.Fatalf("artifact directory permissions: %s %v", p, err)
		}
	}
	saved := map[string][]byte{}
	for _, name := range []string{"instructions.md", "task.txt", "stdout.json", "events.ndjson"} {
		raw, err := os.ReadFile(filepath.Join(before.ArtifactDir, name))
		if err != nil {
			t.Fatal(name, err)
		}
		saved[name] = raw
	}
	// Model the reboot cleanup that removed the original real task's /tmp output.
	if err := os.RemoveAll(ephemeral); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(ephemeral, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAWFLOW_EXPECT_RESUME", before.Session)
	if err := os.WriteFile(gate, nil, 0600); err != nil {
		t.Fatal(err)
	}
	result, err := Run(WithCheckpoint(context.Background(), checkpoint, true), c, req, nil, nil, nil)
	if err != nil || result.NativeSessionID != before.Session {
		t.Fatalf("resume: %+v %v", result, err)
	}
	after, err := ReadCheckpoint(checkpoint)
	if err != nil || !after.Completed || after.ArtifactDir == before.ArtifactDir || filepath.Dir(after.ArtifactDir) != expectedParent {
		t.Fatalf("new invocation did not retain separate evidence: %+v %v", after, err)
	}
	for name, want := range saved {
		raw, err := os.ReadFile(filepath.Join(before.ArtifactDir, name))
		if err != nil || string(raw) != string(want) {
			t.Fatalf("prior evidence lost or overwritten: %s %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(after.ArtifactDir, "stdout.json")); err != nil {
		t.Fatal(err)
	}
}
