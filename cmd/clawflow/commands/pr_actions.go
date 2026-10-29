package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/actions"
)

// reconcileActionsObservations runs outside the worker lease/session pool. Its
// persisted request is the only source of policy; restart never invents one.
func reconcileActionsObservations(ctx context.Context, ledger actions.Ledger) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	requests, err := ledger.Pending(ctx)
	if err != nil {
		return err
	}
	for _, request := range requests {
		client, _, _, err := newVCSClientForRepo(request.Contract.Repository)
		if err != nil {
			return err
		}
		provider, ok := client.(actions.Provider)
		if !ok {
			return errors.New("persisted Actions request has an unsupported provider")
		}
		if _, err := ledger.Reconcile(ctx, provider, request); err != nil {
			return err
		}
	}
	return nil
}

func newPRActionsObserveCmd() *cobra.Command {
	var requestPath string
	cmd := &cobra.Command{
		Use:   "actions-observe",
		Short: "Observe and persist one existing Actions test run (never dispatch or approve)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// A worker cannot supply a new trust policy or hold its agent session
			// while waiting for external tests. This is a control-plane operation.
			if os.Getenv("CLAWFLOW_EFFECT_SPOOL") != "" {
				return errors.New("Actions observation belongs to the control plane, outside worker sessions")
			}
			b, err := acceptanceRead(requestPath, 1<<20)
			if err != nil {
				return err
			}
			var request actions.Request
			if err := json.Unmarshal(b, &request); err != nil {
				return err
			}
			if _, err := request.Key(); err != nil {
				return err
			}
			client, _, _, err := newVCSClientForRepo(request.Contract.Repository)
			if err != nil {
				return err
			}
			provider, ok := client.(actions.Provider)
			if !ok {
				return errors.New("provider does not support GitHub Actions observation")
			}
			db, err := requireStore(cmd.Context())
			if err != nil {
				return err
			}
			defer db.Close()
			o, err := (actions.Ledger{DB: db.DB()}).Reconcile(cmd.Context(), provider, request)
			if err != nil {
				return err
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(o)
		},
	}
	cmd.Flags().StringVar(&requestPath, "request", "", "JSON containing an independently pinned contract and candidate policy")
	_ = cmd.MarkFlagRequired("request")
	return cmd
}
