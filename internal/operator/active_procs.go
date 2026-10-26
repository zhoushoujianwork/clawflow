package operator

import "github.com/zhoushoujianwork/clawflow/internal/execution"

// TerminateAllActiveCmds shuts down all shared AGH executions before watchdog exit.
func TerminateAllActiveCmds() { execution.TerminateAllActiveCmds() }
