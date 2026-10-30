package commands

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

func newWorkerJoinCmd() *cobra.Command {
	var o workerOptions
	cmd := &cobra.Command{Use: "join", Short: "Exchange a one-use invitation read from stdin", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if o.TokenFile == "" {
			return fmt.Errorf("--token-file is required")
		}
		path, err := filepath.Abs(expandHomeStr(o.TokenFile))
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		// Reserve the destination before consuming the invitation; never overwrite credentials.
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		keep := false
		defer func() {
			_ = f.Close()
			if !keep {
				_ = os.Remove(path)
			}
		}()
		fmt.Fprintln(cmd.ErrOrStderr(), "Paste the invitation, then press Return (input is not a shell command):")
		secret, err := bufio.NewReader(io.LimitReader(cmd.InOrStdin(), 4096)).ReadString('\n')
		if err != nil && err != io.EOF {
			return err
		}
		secret = strings.TrimSpace(secret)
		if len(secret) != 64 {
			return fmt.Errorf("invalid invitation")
		}
		api, err := newWorkerAPIWithSecret(o, "invitation")
		if err != nil {
			return err
		}
		defer api.client.CloseIdleConnections()
		var reply struct {
			Name  string `json:"name"`
			Token string `json:"token"`
		}
		// Never retry a one-use exchange automatically after an ambiguous response.
		if err := api.postOnce(cmd.Context(), "join", map[string]string{"secret": secret}, &reply); err != nil {
			return fmt.Errorf("join failed; invitation may have been consumed: %w", err)
		}
		if len(reply.Token) != 64 {
			return fmt.Errorf("invalid join response; request a new invitation")
		}
		if _, err := f.WriteString(reply.Token + "\n"); err != nil {
			return err
		}
		if err := f.Sync(); err != nil {
			return err
		}
		keep = true
		fmt.Fprintf(cmd.OutOrStdout(), "Joined as %s. Token saved to %s. Start the worker with this token file; assignments remain paused until resumed in the dashboard.\n", reply.Name, path)
		return nil
	}}
	cmd.Flags().StringVar(&o.Coordinator, "coordinator", "", "Controller HTTPS origin")
	cmd.Flags().StringVar(&o.Fingerprint, "fingerprint", "", "SHA-256 certificate fingerprint")
	cmd.Flags().StringVar(&o.TokenFile, "token-file", "", "New private worker token file")
	for _, name := range []string{"coordinator", "fingerprint", "token-file"} {
		_ = cmd.MarkFlagRequired(name)
	}
	return cmd
}
