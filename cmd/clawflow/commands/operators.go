package commands

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// NewOperatorsCmd wires `clawflow operators …` — registry introspection.
func NewOperatorsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "operators",
		Short: "Inspect registered operators",
	}
	cmd.AddCommand(newOperatorsListCmd())
	cmd.AddCommand(newOperatorsValidateCmd())
	return cmd
}

// newOperatorsValidateCmd parses every embedded + user SKILL.md and exits
// non-zero on any frontmatter error. Wire it into CI so a corrupted
// SKILL.md never ships in a release binary — the runtime would fail
// silently otherwise (the registry would simply not load that operator).
func newOperatorsValidateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Parse every operator SKILL.md and report frontmatter errors",
		Long: `Walks the embedded skills/ tree and ~/.clawflow/skills/, parsing each
SKILL.md. Exits with status 0 if every operator's frontmatter is valid,
or status 1 with a per-file diagnostic if any fail to parse. Useful in
CI to catch broken operators before they ship.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := loadRegistry()
			if err != nil {
				// loadRegistry returns the first parse error it sees from
				// either the embedded set or the user dir, which is what
				// validation cares about.
				return fmt.Errorf("operator validation failed: %w", err)
			}
			ops := reg.All()
			if len(ops) == 0 {
				return fmt.Errorf("no operators registered (embed.FS empty?)")
			}
			fmt.Printf("✓ %d operator(s) parsed cleanly\n", len(ops))
			for _, op := range ops {
				fmt.Printf("  %s  %s\n", op.Name, op.Source)
			}
			return nil
		},
	}
}

func newOperatorsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List registered operators (built-in + user overrides)",
		RunE: func(cmd *cobra.Command, args []string) error {
			reg, err := loadRegistry()
			if err != nil {
				return err
			}
			ops := reg.All()
			if len(ops) == 0 {
				fmt.Println("no operators registered")
				return nil
			}

			home, _ := os.UserHomeDir()
			w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tTARGET\tTRIGGER LABELS\tSOURCE")
			for _, op := range ops {
				src := op.Source
				if home != "" && strings.HasPrefix(src, home) {
					src = "~" + strings.TrimPrefix(src, home)
				}
				labels := strings.Join(op.Trigger.LabelsRequired, ",")
				if labels == "" {
					labels = "-"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", op.Name, op.Trigger.Target, labels, src)
			}
			if err := w.Flush(); err != nil {
				return err
			}

			// Also print descriptions below the table so users see what each op does.
			fmt.Println()
			for _, op := range ops {
				fmt.Printf("  %s — %s\n", op.Name, op.Description)
			}
			return nil
		},
	}
}
