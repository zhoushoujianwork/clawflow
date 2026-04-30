package commands

import (
	"fmt"
	"net"
	"os"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

// NewRespawnCmd registers `clawflow __respawn`, an internal helper used by
// the dashboard's Upgrade button to relaunch `clawflow web` after the old
// process releases its port. Hidden from --help; users have no reason to
// invoke it directly.
//
// Flow on a successful upgrade:
//  1. Old web process spawns this helper detached (Setsid) with --addr.
//  2. Helper polls until the addr is free (parent has shut down).
//  3. Helper syscall.Execs `clawflow web --host H --port P`, which now
//     runs the freshly-replaced binary.
func NewRespawnCmd() *cobra.Command {
	var addr string
	cmd := &cobra.Command{
		Use:    "__respawn",
		Hidden: true,
		Short:  "Internal: wait for the parent web process to exit, then re-exec `clawflow web`",
		RunE: func(cmd *cobra.Command, args []string) error {
			host, portStr, err := net.SplitHostPort(addr)
			if err != nil {
				return fmt.Errorf("invalid --addr %q: %w", addr, err)
			}
			// Poll the bind until the parent has freed it. 10s is well
			// past the parent's 3s graceful-shutdown budget; if we still
			// can't bind we exec anyway and let the new web log the bind
			// error to the user's terminal.
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				ln, err := net.Listen("tcp", addr)
				if err == nil {
					_ = ln.Close()
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			self, err := os.Executable()
			if err != nil {
				return err
			}
			return syscall.Exec(self, []string{"clawflow", "web", "--host", host, "--port", portStr}, os.Environ())
		},
	}
	cmd.Flags().StringVar(&addr, "addr", "127.0.0.1:8090", "host:port the parent web is bound to (and that the new web will rebind)")
	return cmd
}
