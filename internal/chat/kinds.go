package chat

import (
	"fmt"
	"sort"
	"sync"
)

// ChatKind describes one variety of project-level chat we support
// from the dashboard. Keep additions to the registry small — each new
// kind adds a system-prompt builder and a fenced-block tag we extract
// from the model's output to surface as a "draft" the user can save.
type ChatKind struct {
	Name        string
	Description string
	DraftTag    string                                                                   // fenced info string, e.g. "goals.md"
	Builder     func(projectName string, repos []ProjectChatRepo, current string) string // system prompt
}

// kindRegistry is keyed by Name. Lookups happen on every chat-stream
// request so a sync.RWMutex keeps reads cheap; writes only happen
// from init() so contention is effectively nil.
var (
	kindRegistry   = map[string]ChatKind{}
	kindRegistryMu sync.RWMutex
)

// RegisterKind installs k under k.Name. Panics on duplicate name —
// duplicates would silently shadow each other otherwise, and every
// call site in this package is from init() where a panic is the
// right loud failure mode.
func RegisterKind(k ChatKind) {
	kindRegistryMu.Lock()
	defer kindRegistryMu.Unlock()
	if _, dup := kindRegistry[k.Name]; dup {
		panic(fmt.Sprintf("chat: duplicate ChatKind name %q", k.Name))
	}
	kindRegistry[k.Name] = k
}

// GetKind returns the registered kind under name. The bool follows the
// idiomatic comma-ok shape so handlers can map a missing kind to an
// HTTP 400 cleanly.
func GetKind(name string) (ChatKind, bool) {
	kindRegistryMu.RLock()
	defer kindRegistryMu.RUnlock()
	k, ok := kindRegistry[name]
	return k, ok
}

// ListKinds returns every registered kind, sorted by Name. Stable
// order is what the dashboard wants when rendering its chat-kind
// picker; sorting at read-time keeps the registry write-path simple.
func ListKinds() []ChatKind {
	kindRegistryMu.RLock()
	defer kindRegistryMu.RUnlock()
	out := make([]ChatKind, 0, len(kindRegistry))
	for _, k := range kindRegistry {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func init() {
	RegisterKind(ChatKind{
		Name:        "goals",
		Description: "Co-draft the project's goals.md (user requirements).",
		DraftTag:    "goals.md",
		Builder:     BuildGoalsChatContext,
	})
	// "context" reuses BuildProjectChatContext, which takes both
	// contextMD and testingMD. The dashboard's context kind only
	// edits context.md, so testingMD is passed empty.
	RegisterKind(ChatKind{
		Name:        "context",
		Description: "Co-draft the project's context.md (Pilot's own memory).",
		DraftTag:    "context.md",
		Builder: func(projectName string, repos []ProjectChatRepo, current string) string {
			return BuildProjectChatContext(projectName, repos, current, "")
		},
	})
}
