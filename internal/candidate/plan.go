// Package candidate builds a disposable, per-commit runtime for testing a
// ClawFlow development candidate without touching the installed stable host.
//
// Every resource a candidate owns (directories, HOME, config, locks, listener
// port, database schema, launchd labels, native bundle identifier) is derived
// from the pinned commit. Plan validation refuses any overlap with the stable
// host or another candidate before anything is created, and every destructive
// step re-checks ownership through the manifest written at preparation time.
package candidate

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/zhoushoujianwork/clawflow/internal/devmode"
)

// StableBundleID is the installed macOS app's bundle identifier.
const StableBundleID = "com.clawflow.dashboard"

// BundlePrefix is the namespace reserved for candidate app builds.
const BundlePrefix = StableBundleID + ".dev."

// ErrCollision marks a refused overlap with a stable or foreign resource.
var ErrCollision = errors.New("resource collision")

var commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// ValidCommit reports whether s is a full lowercase SHA-1 object name.
func ValidCommit(s string) bool { return commitPattern.MatchString(s) }

// IDFor derives the stable candidate id for a commit.
func IDFor(commit string) string {
	sum := sha256.Sum256([]byte("clawflow-candidate\x00" + commit))
	return hex.EncodeToString(sum[:6])
}

// Pin is the approved testing contract shared by development and acceptance.
type Pin struct {
	SOPRequired  bool     `json:"sop_required"`
	SOPDigest    string   `json:"sop_digest,omitempty"`
	Capabilities []string `json:"capabilities,omitempty"`
}

// Stable describes what the installed host owns. Plans may not overlap it.
type Stable struct {
	Root        string // stable runtime root (config.RootDir())
	Ports       []int  // stable listeners (dashboard, coordinator, database)
	LabelPrefix string // launchd namespace of stable services
}

// Plan lists every resource owned by one candidate.
type Plan struct {
	ID        string            `json:"id"`
	Commit    string            `json:"commit"`
	Base      string            `json:"base"`
	Root      string            `json:"root"`
	Home      string            `json:"home"`
	State     string            `json:"state"`
	ReviewDir string            `json:"review_state"`
	Config    string            `json:"config"`
	Locks     string            `json:"locks"`
	Worktrees string            `json:"worktrees"`
	Cache     string            `json:"cache"`
	Logs      string            `json:"logs"`
	Bin       string            `json:"bin"`
	Tmp       string            `json:"tmp"`
	Src       string            `json:"src"`
	Port      int               `json:"port"`
	Schema    string            `json:"schema"`
	Labels    map[string]string `json:"labels"`
	BundleID  string            `json:"bundle_id"`
	AppPath   string            `json:"app_path"`
	Pin       Pin               `json:"pin"`
}

// NewPlan derives every owned resource from the commit. Port 0 selects the
// reproducible default for the id.
func NewPlan(commit, base string, port int, pin Pin) (Plan, error) {
	if !ValidCommit(commit) {
		return Plan{}, fmt.Errorf("commit must be a full 40-character lowercase SHA")
	}
	if !filepath.IsAbs(base) {
		return Plan{}, fmt.Errorf("base directory must be absolute")
	}
	id := IDFor(commit)
	if port == 0 {
		sum := sha256.Sum256([]byte("clawflow-candidate-port\x00" + commit))
		port = 20000 + int(binary.BigEndian.Uint16(sum[:2]))%20000
	}
	root := filepath.Join(base, id)
	p := Plan{
		ID: id, Commit: commit, Base: base, Root: root,
		Home:      filepath.Join(root, "home"),
		State:     filepath.Join(root, "state"),
		ReviewDir: filepath.Join(root, "state-review"),
		Config:    filepath.Join(root, "home", ".clawflow", "config"),
		Locks:     filepath.Join(root, "state", "locks"),
		Worktrees: filepath.Join(root, "state", "worktrees"),
		Cache:     filepath.Join(root, "cache"),
		Logs:      filepath.Join(root, "logs"),
		Bin:       filepath.Join(root, "bin"),
		Tmp:       filepath.Join(root, "tmp"),
		Src:       filepath.Join(root, "src"),
		Port:      port,
		Schema:    devmode.SchemaPrefix + id,
		Labels: map[string]string{
			"controller": devmode.ServiceLabel(id, "controller"),
			"dev-worker": devmode.ServiceLabel(id, "worker-dev"),
			"reviewer":   devmode.ServiceLabel(id, "worker-review"),
		},
		BundleID: BundlePrefix + id,
		AppPath:  filepath.Join(root, "app", "ClawFlow Dev.app"),
		Pin:      pin,
	}
	p.Pin.Capabilities = slices.Clone(pin.Capabilities)
	slices.Sort(p.Pin.Capabilities)
	return p, nil
}

// Probe reports whether a loopback TCP port can currently be bound.
type Probe func(port int) bool

// FreePort is the production Probe.
func FreePort(port int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
}

func collide(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrCollision, fmt.Sprintf(format, args...))
}

// Validate refuses overlaps with the stable host or another candidate. It
// reads the file system but never creates or changes anything.
func (p Plan) Validate(stable Stable, probe Probe) error {
	if !ValidCommit(p.Commit) || p.ID != IDFor(p.Commit) {
		return collide("plan identity does not match its commit")
	}
	if stable.Root != "" {
		if devmode.Within(stable.Root, p.Root) || devmode.Within(p.Root, stable.Root) {
			return collide("candidate root overlaps the stable runtime root")
		}
	}
	for _, dir := range []string{p.Home, p.State, p.ReviewDir, p.Config, p.Locks, p.Worktrees, p.Cache, p.Logs, p.Bin, p.Tmp, p.Src, p.AppPath} {
		if !devmode.Within(p.Root, dir) {
			return collide("%s escapes the candidate root", dir)
		}
	}
	if home, err := os.UserHomeDir(); err == nil && devmode.Within(p.Root, filepath.Join(home, ".clawflow")) {
		return collide("candidate root contains the stable home state")
	}
	if p.Port < 1024 || p.Port > 65535 {
		return collide("port %d is outside the unprivileged range", p.Port)
	}
	if slices.Contains(stable.Ports, p.Port) {
		return collide("port %d belongs to the stable host", p.Port)
	}
	if !strings.HasPrefix(p.Schema, devmode.SchemaPrefix) || p.Schema != devmode.SchemaPrefix+p.ID {
		return collide("schema %q is not this candidate's", p.Schema)
	}
	if p.BundleID == StableBundleID || p.BundleID != BundlePrefix+p.ID {
		return collide("bundle identifier %q is not this candidate's", p.BundleID)
	}
	for kind, label := range p.Labels {
		if !strings.HasPrefix(label, devmode.LabelPrefix(p.ID)) {
			return collide("%s label %q is outside the candidate namespace", kind, label)
		}
		if stable.LabelPrefix != "" && strings.HasPrefix(label, stable.LabelPrefix) && !strings.HasPrefix(label, devmode.LabelBase) {
			return collide("%s label %q is a stable service label", kind, label)
		}
	}
	if err := p.checkSiblings(); err != nil {
		return err
	}
	if probe != nil && !probe(p.Port) {
		return collide("port %d is already in use", p.Port)
	}
	return nil
}

// checkSiblings refuses a plan whose identifying resources are claimed by a
// different candidate already prepared under the same base directory.
func (p Plan) checkSiblings() error {
	entries, err := os.ReadDir(p.Base)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if e.Name() == p.ID || !e.IsDir() {
			continue
		}
		other, err := ReadManifest(filepath.Join(p.Base, e.Name()))
		if err != nil {
			continue
		}
		switch {
		case other.Plan.Port == p.Port:
			return collide("port %d is claimed by candidate %s", p.Port, other.Plan.ID)
		case other.Plan.Schema == p.Schema, other.Plan.BundleID == p.BundleID:
			return collide("identity is claimed by candidate %s", other.Plan.ID)
		}
	}
	return nil
}
