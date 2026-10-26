package operator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhoushoujianwork/clawflow/internal/execution"
)

func TestRunAgentHostForwardsBudgetAndRole(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".clawflow", "config")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "credentials.yaml"), []byte("default_provider_seeded: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	command := filepath.Join(home, "agent-host")
	record := filepath.Join(home, "request.json")
	script := `#!/bin/sh
cat > "$CLAWFLOW_TEST_OPERATOR_REQUEST"
request=$(sed -n 's/.*"request_id":"\([^"]*\)".*/\1/p' "$CLAWFLOW_TEST_OPERATOR_REQUEST")
session=$(sed -n 's/.*"session_key":"\([^"]*\)".*/\1/p' "$CLAWFLOW_TEST_OPERATOR_REQUEST")
printf '%s\n' '{"version":1,"request_id":"'"$request"'","seq":1,"type":"result","result":{"status":"failed","text":"partial","model":"operator-slot","session_key":"'"$session"'","native_session_id":"native","error_kind":"budget_exceeded","error":"cap","usage":null}}'
exit 1
`
	if err := os.WriteFile(command, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(home, "execution.yaml")
	if err := os.WriteFile(cfg, []byte("fixture: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAWFLOW_AGH_COMMAND", command)
	t.Setenv("CLAWFLOW_AGH_CONFIG", cfg)
	t.Setenv("CLAWFLOW_AGH_NATIVE", "/bin/sh")
	t.Setenv("CLAWFLOW_AGH_PROFILE", home)
	t.Setenv("CLAWFLOW_TEST_OPERATOR_REQUEST", record)
	t.Setenv("CLAWFLOW_CLAUDE_OPERATOR_MODEL", "operator-slot")
	text, err := RunAgentHost(WithMaxBudgetUSD(context.Background(), 8.75), "prompt", home, 10*time.Second, nil, "operator", "instructions")
	if !errors.Is(err, ErrBudgetExceeded) || text != "partial" {
		t.Fatalf("text=%q err=%v", text, err)
	}
	raw, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	var req execution.Request
	if err = json.Unmarshal(raw, &req); err != nil {
		t.Fatal(err)
	}
	if req.MaxBudgetUSD != 8.75 || req.Model != "operator-slot" || req.Instructions != "instructions" || req.PermissionMode != "bypassPermissions" || req.TimeoutMS != 10000 {
		t.Fatalf("transport: %+v", req)
	}
}
