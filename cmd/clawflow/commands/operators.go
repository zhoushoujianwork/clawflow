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
	return cmd
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
