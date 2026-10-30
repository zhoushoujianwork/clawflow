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
	_, err := Run(WithCheckpoint(context.Background(), os.Getenv("CLAWFLOW_CHECKPOINT_PATH"), false), Config{Command: os.Getenv("CLAWFLOW_CHECKPOINT_COMMAND"), Profile: "fixture"}, Request{Version: 1, RequestID: "caller", Workspace: os.Getenv("CLAWFLOW_CHECKPOINT_WORKSPACE"), PermissionMode: "bypassPermissions", TimeoutMS: 15000}, nil, nil, nil)
	if err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestCheckpointLockSurvivesExecutorCrash(t *testing.T) {
	c, req, gate := streamingFixture(t)
	path := filepath.Join(t.TempDir(), "execution.json")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	helper := exec.Command(executable, "-test.run=^TestCheckpointExecutorHelper$")
	helper.Env = append(os.Environ(), "CLAWFLOW_CHECKPOINT_HELPER=1", "CLAWFLOW_CHECKPOINT_PATH="+path, "CLAWFLOW_CHECKPOINT_COMMAND="+c.Command, "CLAWFLOW_CHECKPOINT_WORKSPACE="+req.Workspace)
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
