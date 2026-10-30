package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/config"
	"github.com/zhoushoujianwork/clawflow/internal/execution"
)

// errExecutionBlocked is returned after the report was already printed, so the
// process exits nonzero without a second message.
var errExecutionBlocked = errors.New("execution capability check blocked")

// NewExecutionCmd groups local execution-engine diagnostics.
func NewExecutionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "execution",
		Short: "Inspect the configured Offload execution engine",
	}
	cmd.AddCommand(newExecutionCheckCmd(), newExecutionStatusCmd())
	return cmd
}

func newExecutionCheckCmd() *cobra.Command {
	var jsonOutput bool
	var role string
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Check that the configured Offload CLI declares the flags ClawFlow needs",
		Long: `Run a bounded local probe of the configured Offload command.

The probe validates settings.execution, resolves the command to an absolute path
and runs only '<command> -h' with empty stdin in an empty temporary directory,
then checks that every flag ClawFlow passes at run time is declared. It does not
start a task, read credentials or contact a provider, so a ready result is no
guarantee that live provider execution or permission enforcement works.

Use --role to apply settings.execution.role_profiles for that role; an explicit
CLAWFLOW_OFFLOAD_PROFILE still wins, as it does during execution.`,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			report := runExecutionCheck(cmd, role)
			out := cmd.OutOrStdout()
			if jsonOutput {
				if err := json.NewEncoder(out).Encode(report); err != nil {
					return err
				}
			} else {
				printExecutionReport(out, report)
			}
			if report.Status != execution.StatusReady {
				return errExecutionBlocked
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Print the report as JSON")
	cmd.Flags().StringVar(&role, "role", "", "Apply settings.execution.role_profiles for this role (including pilot)")
	return cmd
}

// loadExecutionCheckConfig reads only the canonical config file, read-only.
// Unlike execution.LoadConfig it never goes through config.Load, whose legacy
// repos.yaml migration writes and renames files. Parser errors are not echoed.
func loadExecutionCheckConfig() (execution.Config, error) {
	var c execution.Config
	path := config.ConfigPath()
	_, statErr := os.Stat(path)
	switch {
	case statErr == nil:
		var cfg config.Config
		if err := readCheckYAML(path, &cfg); err != nil {
			return c, errors.New("Offload settings unreadable: fix or recreate ~/.clawflow/config/config.yaml")
		}
		c = cfg.Settings.Execution
	case os.IsNotExist(statErr):
		if os.Getenv("CLAWFLOW_OFFLOAD_PROFILE") == "" {
			if _, err := os.Stat(filepath.Join(filepath.Dir(path), "repos.yaml")); err == nil {
				return c, errors.New("Only a legacy repos.yaml exists; this check does not migrate it. Create ~/.clawflow/config/config.yaml with settings.execution.profile or set CLAWFLOW_OFFLOAD_PROFILE")
			}
			return c, errors.New("Offload settings unavailable: configure settings.execution.profile or CLAWFLOW_OFFLOAD_PROFILE")
		}
	default:
		return c, errors.New("Offload settings unreadable: ~/.clawflow/config/config.yaml is not accessible")
	}
	return execution.WithEnvironment(c), nil
}

func runExecutionCheck(cmd *cobra.Command, role string) execution.Report {
	c, err := loadExecutionCheckConfig()
	if err != nil {
		return execution.ConfigBlockedReport(err.Error())
	}
	c.Profile = execution.ProfileForRole(c, role)
	return execution.ProbeCapabilities(cmd.Context(), c)
}

func printExecutionReport(out io.Writer, r execution.Report) {
	fmt.Fprintf(out, "Offload execution: %s (local help probe only)\n", r.Status)
	if r.Command != "" {
		fmt.Fprintf(out, "Command: %s\n", r.Command)
	}
	if r.Profile != "" {
		fmt.Fprintf(out, "Profile: %s\n", r.Profile)
	}
	for _, check := range r.Checks {
		fmt.Fprintf(out, "%s %s: %s\n", check.Status, check.ID, check.Message)
	}
	fmt.Fprintf(out, "Unverified: %s\n", strings.Join(r.Unverified, ", "))
}
