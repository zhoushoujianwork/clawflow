// Package component manages opt-in bindings without changing component ownership
// or the existing runner's configuration. Adapters observe; they never dispatch.
package component

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"
)

const Schema = 1

var ErrRefused = errors.New("component operation refused: identity, version, capability, health or project scope mismatch")

// Descriptor contains public identity and reference data only. Reference names
// identify commands/configuration; they are never URLs with embedded credentials.
type Descriptor struct {
	ID           string   `json:"id"`
	Adapter      string   `json:"adapter"`
	Version      string   `json:"version"`
	Reference    string   `json:"reference"`
	Capabilities []string `json:"capabilities"`
}

type Observation struct {
	Resource     string   `json:"resource,omitempty"`
	Identity     string   `json:"identity"`
	Version      string   `json:"version"`
	Protocol     int      `json:"protocol"`
	Health       string   `json:"health"`
	Capabilities []string `json:"capabilities"`
}

// Adapter implementations are trusted code. Discover and Probe must be read-only
// and must not return credentials, subprocess output or provider response bodies.
type Adapter interface {
	Discover() Descriptor
	Probe(context.Context) (Observation, error)
}

type Binding struct {
	Project       string   `json:"project"`
	Role          string   `json:"role"`
	Resource      string   `json:"resource"`
	CredentialRef string   `json:"credential_ref,omitempty"`
	Capabilities  []string `json:"capabilities"`
	State         string   `json:"state"`
}

type Record struct {
	Descriptor
	State     string       `json:"state"`
	Health    string       `json:"health"`
	Pin       *Observation `json:"pin,omitempty"`
	CheckedAt string       `json:"checked_at,omitempty"`
	Bindings  []Binding    `json:"bindings,omitempty"`
}

type Registry struct {
	Credentials CredentialResolver `json:"-"`
	Schema      int                `json:"schema"`
	Records     map[string]Record  `json:"records"`
}

var namePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/-]{0,199}$`)

func validName(s string) bool {
	if !namePattern.MatchString(s) {
		return false
	}
	for _, part := range strings.Split(s, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}
func validCaps(c []string) bool {
	if len(c) == 0 {
		return false
	}
	for _, s := range c {
		if !validName(s) {
			return false
		}
	}
	return true
}
func contains(all, needed []string) bool {
	for _, s := range needed {
		if !slices.Contains(all, s) {
			return false
		}
	}
	return true
}
func validDescriptor(d Descriptor) bool {
	return validName(d.ID) && validName(d.Adapter) && validName(d.Version) && validName(d.Reference) && validCaps(d.Capabilities)
}

// Inventory is pure discovery: it does not probe, persist, bind or activate.
func Inventory(adapters []Adapter) ([]Record, error) {
	records := make([]Record, 0, len(adapters))
	seen := map[string]bool{}
	for _, a := range adapters {
		d := a.Discover()
		if !validDescriptor(d) || seen[d.ID] {
			return nil, ErrRefused
		}
		seen[d.ID] = true
		d.Capabilities = slices.Clone(d.Capabilities)
		records = append(records, Record{Descriptor: d, State: "discovered", Health: "unknown"})
	}
	return records, nil
}

// Register never upgrades or replaces an existing identity or authorization.
func (r *Registry) Register(a Adapter) error {
	if r.Schema != Schema {
		return ErrRefused
	}
	records, err := Inventory([]Adapter{a})
	if err != nil {
		return err
	}
	if r.Records == nil {
		r.Records = map[string]Record{}
	}
	d := records[0]
	if _, ok := r.Records[d.ID]; ok {
		return ErrRefused
	}
	r.Records[d.ID] = d
	return nil
}

func (r *Registry) probe(ctx context.Context, id string, a Adapter) (Record, Observation, error) {
	rec, ok := r.Records[id]
	d := a.Discover()
	if r.Schema != Schema || !ok || !validDescriptor(d) || rec.ID != d.ID || rec.Adapter != d.Adapter || rec.Version != d.Version || rec.Reference != d.Reference || !slices.Equal(rec.Capabilities, d.Capabilities) {
		return rec, Observation{}, ErrRefused
	}
	o, err := a.Probe(ctx)
	if err != nil || ctx.Err() != nil || !validName(o.Identity) || !validName(o.Version) || o.Protocol != Schema || o.Health != "available" || !validCaps(o.Capabilities) || !contains(o.Capabilities, rec.Capabilities) {
		return rec, Observation{}, ErrRefused
	}
	o.Capabilities = slices.Clone(o.Capabilities)
	return rec, o, nil
}

func (r *Registry) block(id string) {
	rec, ok := r.Records[id]
	if !ok {
		return
	}
	rec.State = "blocked"
	rec.Health = "unavailable"
	rec.CheckedAt = time.Now().UTC().Format(time.RFC3339)
	for i := range rec.Bindings {
		rec.Bindings[i].State = "disabled"
	}
	r.Records[id] = rec
}

// Verify invalidates previous bindings, even when verification succeeds.
func (r *Registry) Verify(ctx context.Context, id string, a Adapter) error {
	rec, o, err := r.probe(ctx, id, a)
	if err != nil {
		r.block(id)
		return err
	}
	rec.Pin = &o
	rec.Health = o.Health
	rec.State = "verified"
	rec.CheckedAt = time.Now().UTC().Format(time.RFC3339)
	rec.Bindings = nil
	r.Records[id] = rec
	return nil
}

func (r *Registry) Bind(id string, b Binding) error {
	rec, ok := r.Records[id]
	if r.Schema != Schema || !ok || rec.Pin == nil || (rec.State != "verified" && rec.State != "bound" && rec.State != "active") || !validName(b.Project) || strings.Contains(b.Project, "/") || !validName(b.Role) || strings.Contains(b.Role, "/") || !validName(b.Resource) || !validCaps(b.Capabilities) || !contains(rec.Pin.Capabilities, b.Capabilities) || !contains(rec.Capabilities, b.Capabilities) {
		return ErrRefused
	}
	if rec.Pin.Resource != "" && rec.Pin.Resource != b.Resource {
		return ErrRefused
	}
	// Credentials are opaque project-local slots, never secret values or URLs.
	if r.credential(id, b) != nil {
		return ErrRefused
	}
	for _, existing := range rec.Bindings {
		if existing.Project == b.Project && existing.Role == b.Role {
			return ErrRefused
		}
	}
	b.State = "bound"
	b.Capabilities = slices.Clone(b.Capabilities)
	rec.Bindings = append(rec.Bindings, b)
	if rec.State != "active" {
		rec.State = "bound"
	}
	r.Records[id] = rec
	return nil
}

// Activate requires an explicit call and a fresh matching observation. It grants
// registry authorization only; it never starts work or updates legacy settings.
func (r *Registry) Activate(ctx context.Context, id, project, role string, a Adapter) error {
	if _, err := r.scopedBinding(id, project, role, "bound"); err != nil {
		return err
	}
	rec, o, err := r.probe(ctx, id, a)
	if err != nil {
		r.block(id)
		return ErrRefused
	}
	if rec.Pin == nil || rec.Pin.Identity != o.Identity || rec.Pin.Version != o.Version || rec.Pin.Resource != o.Resource || !slices.Equal(rec.Pin.Capabilities, o.Capabilities) {
		r.block(id)
		return ErrRefused
	}
	for i, b := range rec.Bindings {
		if b.Project == project && b.Role == role && b.State == "bound" {
			rec.Bindings[i].State = "active"
			rec.State = "active"
			rec.Health = o.Health
			rec.CheckedAt = time.Now().UTC().Format(time.RFC3339)
			r.Records[id] = rec
			return nil
		}
	}
	return ErrRefused
}

func (r *Registry) Disable(id, project, role string) error {
	rec, ok := r.Records[id]
	if !ok || r.Schema != Schema {
		return ErrRefused
	}
	found := false
	for i, b := range rec.Bindings {
		if b.Project == project && b.Role == role {
			rec.Bindings[i].State = "disabled"
			found = true
		}
	}
	if !found {
		return ErrRefused
	}
	rec.State = "disabled"
	for _, b := range rec.Bindings {
		if b.State == "active" {
			rec.State = "active"
			break
		}
		if b.State == "bound" {
			rec.State = "bound"
		}
	}
	r.Records[id] = rec
	return nil
}

// Authorize rechecks identity and health before a future consumer may dispatch.
// Credentials are re-resolved before probing; callers still own execution policies.
func (r *Registry) Authorize(ctx context.Context, id, project, role, capability string, a Adapter) error {
	b, err := r.scopedBinding(id, project, role, "active")
	if err != nil || !slices.Contains(b.Capabilities, capability) {
		return ErrRefused
	}
	rec, o, err := r.probe(ctx, id, a)
	if err != nil || rec.Pin == nil || rec.Pin.Identity != o.Identity || rec.Pin.Version != o.Version || rec.Pin.Resource != o.Resource || !slices.Equal(rec.Pin.Capabilities, o.Capabilities) {
		r.block(id)
		return ErrRefused
	}
	for _, b := range rec.Bindings {
		if b.Project == project && b.Role == role && b.State == "active" && slices.Contains(b.Capabilities, capability) {
			return nil
		}
	}
	return ErrRefused
}
