package commands

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/coordinator"
	"github.com/zhoushoujianwork/clawflow/internal/snapshot"
)

// NewWorkerCmd runs workers and administers coordinator tokens.
func NewWorkerCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "worker", Short: "管理 worker"}
	token := &cobra.Command{Use: "token", Short: "管理 coordinator worker token"}
	for _, action := range []string{"create", "list", "revoke"} {
		action := action
		var name string
		var scope string
		sub := &cobra.Command{Use: action, Args: cobra.NoArgs, SilenceUsage: true, RunE: func(cmd *cobra.Command, args []string) error {
			db, err := requireStore(cmd.Context())
			if err != nil {
				return err
			}
			defer db.Close()
			s := coordinator.New(db.DB())
			switch action {
			case "create":
				_, pin, err := coordinator.LoadCertificate(filepath.Join(snapshot.DataDir(), "coordinator"))
				if err != nil {
					return err
				}
				secret, err := s.CreateToken(cmd.Context(), name, scope)
				if err != nil {
					return err
				}
				_, err = fmt.Fprintln(cmd.OutOrStdout(), secret)
				if err == nil {
					_, err = fmt.Fprintf(cmd.ErrOrStderr(), "SHA-256 fingerprint: %s\n", pin)
				}
				return err
			case "revoke":
				return s.RevokeToken(cmd.Context(), name)
			default:
				tokens, err := s.ListTokens(cmd.Context())
				if err != nil {
					return err
				}
				if name != "" {
					filtered := tokens[:0]
					for _, t := range tokens {
						if t.Name == name {
							filtered = append(filtered, t)
						}
					}
					tokens = filtered
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(tokens)
			}
		}}
		sub.Flags().StringVar(&name, "name", "", "Token 名称（create/revoke 必填）")
		if action == "create" {
			sub.Flags().StringVar(&scope, "scope", "worker", "Token 权限：admin 或 worker")
		}
		if action != "list" {
			_ = sub.MarkFlagRequired("name")
		}
		token.AddCommand(sub)
	}
	configureWorker(cmd)
	cmd.AddCommand(token, newWorkerJoinCmd(), newWorkerExecuteCmd())
	cmd.AddCommand(newWorkerGenerationCommands()...)
	cmd.AddCommand(newWorkerRecoveryCommands()...)
	cmd.AddCommand(newWorkerLegacyImportCmd())
	return cmd
}
