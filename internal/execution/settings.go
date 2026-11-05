package execution

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"time"
)

// DefaultProfile reads or updates Offload's own default; credentials stay in Offload.
func DefaultProfile(ctx context.Context, c Config, selected string) (string, error) {
	if selected != "" && !validProfile(selected) {
		return "", errors.New("invalid Offload profile")
	}
	command := c.Command
	if command == "" {
		command = "offload"
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if selected != "" {
		if err := exec.CommandContext(ctx, command, "default", selected).Run(); err != nil {
			return "", errors.New("Offload could not update its default profile")
		}
	}
	raw, err := exec.CommandContext(ctx, command, "profiles", "--json").Output()
	if err != nil || len(raw) > maxProfilesBytes {
		return "", errors.New("Offload default configuration unavailable")
	}
	var snapshot struct {
		Default string `json:"configured_default"`
	}
	if json.Unmarshal(raw, &snapshot) != nil || !validProfile(snapshot.Default) {
		return "", errors.New("Offload default profile unavailable")
	}
	return snapshot.Default, nil
}
