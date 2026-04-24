// Package clawflow is a thin root-level package whose only job is to expose
// assets to the Go embed machinery. The actual CLI lives under cmd/clawflow,
// and all operator logic lives under internal/operator — this file exists
// solely so `//go:embed` directives can be attached to a package declaration
// at the repo root.
package clawflow

import "embed"

// EmbeddedSkills holds the built-in operator SKILL.md files. Loaded into the
// operator registry at binary startup; user operators in ~/.clawflow/skills/
// override same-named built-ins at load time.
//
//go:embed all:skills
var EmbeddedSkills embed.FS

// EmbeddedDashboard holds the static dashboard assets. `clawflow web`
// extracts this into ~/.clawflow/dashboard/ on first launch, then serves
// the directory via http.FileServer. Shipping it in the binary means
// `curl | bash` users get the UI without a separate Node/pnpm build.
//
//go:embed all:web/dashboard
var EmbeddedDashboard embed.FS
