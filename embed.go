// Package clawflow is a thin root-level package whose only job is to expose
// the built-in operator skills to the Go embed machinery. The actual CLI
// lives under cmd/clawflow, and all operator logic lives under
// internal/operator — this file exists solely so `//go:embed all:skills`
// can be attached to a package declaration at repo root.
package clawflow

import "embed"

// EmbeddedSkills holds the built-in operator SKILL.md files. Loaded into the
// operator registry at binary startup; user operators in ~/.clawflow/skills/
// override same-named built-ins at load time.
//
//go:embed all:skills
var EmbeddedSkills embed.FS
