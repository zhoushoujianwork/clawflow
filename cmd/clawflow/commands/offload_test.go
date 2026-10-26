package commands

import (
	"strings"
	"testing"
)

func TestOffloadProviderCommandRetired(t *testing.T) {
	cmd := newConfigProviderCmd()
	cmd.SetArgs([]string{"list"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "offload config") {
		t.Fatal(err)
	}
}
