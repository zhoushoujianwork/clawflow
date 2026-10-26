package config

import (
	"strings"
	"testing"
)

func TestExecutionSettingsStayMachineLocal(t *testing.T) {
	local := &Config{Repos: map[string]Repo{}, Settings: Settings{Execution: ExecutionSettings{Command: "/local/agh", ConfigPath: "/local/config", NativeCommand: "/local/native", NativeProfile: "/local/profile"}}}
	raw, err := MarshalForGist(local)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "/local/") || strings.Contains(string(raw), "execution:") {
		t.Fatalf("runtime settings leaked into sync: %s", raw)
	}
	merged, err := MergeConfigs(local, []byte("settings:\n  execution:\n    command: /remote/agh\n"))
	if err != nil {
		t.Fatal(err)
	}
	if merged.Settings.Execution != local.Settings.Execution {
		t.Fatal("remote execution settings overrode local paths")
	}
}
