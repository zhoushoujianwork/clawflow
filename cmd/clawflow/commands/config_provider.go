package commands

import (
	"fmt"
	"github.com/spf13/cobra"
)

func newConfigProviderCmd() *cobra.Command {
	retired := func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("ClawFlow provider configuration is retired; use offload config, then set settings.execution.profile or CLAWFLOW_OFFLOAD_PROFILE")
	}
	cmd := &cobra.Command{Use: "provider", Short: "Retired: configure execution through Offload profiles", RunE: retired}
	for _, name := range []string{"list", "add", "remove", "enable", "disable", "move", "test"} {
		sub := &cobra.Command{Use: name, Short: "Retired: use offload config", DisableFlagParsing: true, RunE: retired}
		cmd.AddCommand(sub)
	}
	return cmd
}
