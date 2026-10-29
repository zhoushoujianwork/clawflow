package execution

import (
	"context"
	"strings"
)

type effectSpoolKey struct{}

// WithEffectSpool scopes the child environment to one concurrent worker task.
func WithEffectSpool(ctx context.Context, path string) context.Context {
	return context.WithValue(ctx, effectSpoolKey{}, path)
}
func effectEnvironment(ctx context.Context, env []string) []string {
	path, _ := ctx.Value(effectSpoolKey{}).(string)
	if path == "" {
		return env
	}
	out := make([]string, 0, len(env)+1)
	for _, v := range env {
		if !strings.HasPrefix(v, "CLAWFLOW_EFFECT_SPOOL=") {
			out = append(out, v)
		}
	}
	return append(out, "CLAWFLOW_EFFECT_SPOOL="+path)
}
