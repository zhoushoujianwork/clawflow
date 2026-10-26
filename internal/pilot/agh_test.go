package pilot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fake the execution-only protocol; never launch a native AI process.
func stubAGHFailure(t *testing.T, kind, text string) {
	t.Helper()
	dir := t.TempDir()
	command := filepath.Join(dir, "agent-host")
	encoded, _ := json.Marshal(text)
	script := `#!/bin/sh
input=$(cat)
request=$(printf '%s' "$input" | sed -n 's/.*"request_id":"\([^"]*\)".*/\1/p')
session=$(printf '%s' "$input" | sed -n 's/.*"session_key":"\([^"]*\)".*/\1/p')
printf '%s\n' '{"version":1,"request_id":"'"$request"'","seq":1,"type":"observation","observation":{"type":"text_delta","delta":TEXT}}'
printf '%s\n' '{"version":1,"request_id":"'"$request"'","seq":2,"type":"result","result":{"status":"failed","text":TEXT,"model":"test","session_key":"'"$session"'","native_session_id":"native-test","error_kind":"KIND","error":"execution stopped","usage":null}}'
exit 1
`
	script = strings.ReplaceAll(script, "TEXT", string(encoded))
	script = strings.ReplaceAll(script, "KIND", kind)
	if err := os.WriteFile(command, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "execution.yaml")
	if err := os.WriteFile(configPath, []byte("test: true\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAWFLOW_AGH_COMMAND", command)
	t.Setenv("CLAWFLOW_AGH_CONFIG", configPath)
	t.Setenv("CLAWFLOW_AGH_NATIVE", "/bin/sh")
	t.Setenv("CLAWFLOW_AGH_PROFILE", dir)
}
