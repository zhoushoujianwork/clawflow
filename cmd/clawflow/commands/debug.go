package commands

import (
	"fmt"
	"os"
)

// Debug is set by the root command's --debug persistent flag. When true,
// debugf prints to stderr; otherwise it's a no-op. Kept as a package var
// (not threaded through every call site) because it's a global verbosity
// switch, not per-call config.
var Debug bool

// debugf prints a "[debug] "-prefixed line to stderr when --debug is on.
// Format/args follow fmt.Printf conventions; a trailing newline is added.
func debugf(format string, args ...any) {
	if !Debug {
		return
	}
	fmt.Fprintf(os.Stderr, "[debug] "+format+"\n", args...)
}
