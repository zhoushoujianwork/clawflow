package acceptance

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"
)

const maxFileBytes = 4 << 20

// Store is private local reference storage, not an authorization ledger. Keep
// its explicit root outside all model-accessible workspaces.
type Store struct {
	root *os.Root
	path string
}

func OpenStore(path string) (*Store, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("acceptance store root must be absolute")
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return nil, err
	}
	st, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return nil, errors.New("acceptance store root must be a private directory, not a symlink")
	}
	r, err := os.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		r.Close()
		return nil, err
	}
	return &Store{root: r, path: canonical}, nil
}

func (s *Store) Close() error { return s.root.Close() }

func key(c Contract) (string, error) {
	d, err := c.Digest()
	if err != nil {
		return "", err
	}
	return digest(struct {
		Repository string
		PR         int
		Contract   string
	}{c.Repository, c.PRNumber, d}), nil
}

func planName(c Contract, attempt string) (string, error) {
	name, err := recordName(c, attempt)
	return strings.TrimSuffix(name, ".record") + ".plan", err
}

func recordName(c Contract, attempt string) (string, error) {
	if !nonempty(attempt) {
		return "", errors.New("missing acceptance attempt ID")
	}
	k, err := key(c)
	return k + "." + digest(attempt) + ".record", err
}

type envelope struct {
	Version int             `json:"version"`
	Digest  string          `json:"digest"`
	Data    json.RawMessage `json:"data"`
}

func (s *Store) write(name string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	b, err := json.Marshal(envelope{Version, digest(json.RawMessage(data)), data})
	if err != nil {
		return err
	}
	if len(b) > maxFileBytes {
		return errors.New("acceptance file exceeds size limit")
	}
	// Publish a complete nonempty directory by rename. A second rename cannot
	// replace a nonempty directory, so even concurrent writers cannot overwrite
	// an immutable reference. Renaming a directory over a symlink also fails.
	tmp := ".tmp-" + randomID()
	if err := s.root.Mkdir(tmp, 0700); err != nil {
		return err
	}
	defer s.root.RemoveAll(tmp)
	f, err := s.root.OpenFile(filepath.Join(tmp, "data.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	staging, err := s.root.Open(tmp)
	if err != nil {
		return err
	}
	err = staging.Sync()
	closeErr = staging.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := s.root.Rename(tmp, name); err != nil {
		return err
	}
	dir, err := s.root.Open(".")
	if err != nil {
		_ = s.root.RemoveAll(name)
		return err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		// Do not leave a passing reference visible after a durability failure.
		_ = s.root.RemoveAll(name)
		_ = dir.Sync()
		return err
	}
	return nil
}

func (s *Store) read(name string, value any) error {
	st, err := s.root.Lstat(name)
	if err != nil {
		return err
	}
	if !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return errors.New("acceptance reference must be a private directory, not a symlink")
	}
	slot, err := s.root.OpenRoot(name)
	if err != nil {
		return err
	}
	defer slot.Close()
	dir, err := slot.Open(".")
	if err != nil {
		return err
	}
	openedDir, statErr := dir.Stat()
	_ = dir.Close()
	if statErr != nil {
		return statErr
	}
	if !os.SameFile(st, openedDir) {
		return errors.New("acceptance reference changed during open")
	}
	st, err = slot.Lstat("data.json")
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() > maxFileBytes {
		return errors.New("acceptance file is not a bounded private regular file")
	}
	f, err := slot.Open("data.json")
	if err != nil {
		return err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(st, opened) {
		return errors.New("acceptance file changed during open")
	}
	b, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		return err
	}
	if len(b) > maxFileBytes {
		return errors.New("acceptance file exceeds size limit")
	}
	var e envelope
	if err := decodeStrict(b, &e); err != nil {
		return err
	}
	if e.Version != Version || e.Digest != digest(e.Data) {
		return errors.New("acceptance file digest mismatch")
	}
	return decodeStrict(e.Data, value)
}

func (s *Store) SavePlan(c Contract, p Plan) error {
	if err := p.validate(c); err != nil {
		return err
	}
	name, err := planName(c, p.AttemptID)
	if err != nil {
		return err
	}
	return s.write(name, p)
}

func (s *Store) LoadPlan(c Contract, attempt string) (Plan, error) {
	var p Plan
	name, err := planName(c, attempt)
	if err != nil {
		return p, err
	}
	if err := s.read(name, &p); err != nil {
		return p, err
	}
	if p.AttemptID != attempt {
		return p, errors.New("stored acceptance plan attempt mismatch")
	}
	return p, p.validate(c)
}

func (s *Store) SaveRecord(r Record) error {
	d, err := r.Contract.Digest()
	if err != nil {
		return err
	}
	if r.Version != Version || r.ContractDigest != d || r.Role != "acceptance" {
		return errors.New("invalid acceptance record identity")
	}
	name, err := recordName(r.Contract, r.AttemptID)
	if err != nil {
		return err
	}
	return s.write(name, r)
}

func (s *Store) LoadRecord(c Contract, attempt string) (Record, error) {
	name, err := recordName(c, attempt)
	if err != nil {
		return Record{}, err
	}
	return s.loadRecord(c, name)
}

func (s *Store) loadRecord(c Contract, name string) (Record, error) {
	var r Record
	if err := s.read(name, &r); err != nil {
		return r, err
	}
	d, _ := c.Digest()
	actual, err := r.Contract.Digest()
	expectedName, nameErr := recordName(c, r.AttemptID)
	if err != nil || nameErr != nil || expectedName != name || r.Version != Version || r.ContractDigest != d || actual != d || r.Role != "acceptance" {
		return r, errors.New("stored acceptance record identity mismatch")
	}
	if r.Plan.Text != "" {
		if err := r.Plan.validate(c); err != nil {
			return r, err
		}
		if r.Plan.AttemptID != r.AttemptID {
			return r, errors.New("stored acceptance plan belongs to another attempt")
		}
	}
	return r, nil
}

// LatestRecord scans local references for exactly c. It returns the latest
// complete attempt, regardless of verdict, and never searches backward for a
// pass. This is a reference scan, not a scalable index or an atomic ledger view.
// Start and completion order must agree strictly; ties or reversed completion
// order are ambiguous and fail closed. No complete record returns os.ErrNotExist.
func (s *Store) LatestRecord(c Contract) (Record, error) {
	complete, err := s.completeRecords(c)
	if err != nil {
		return Record{}, err
	}
	if len(complete) == 0 {
		return Record{}, os.ErrNotExist
	}
	return complete[len(complete)-1], nil
}

// IsInfrastructure reports whether a completed record is an infrastructure
// attempt rather than a reviewer verdict. Evidence is only populated after a
// typed reviewer report was accepted, so a completed record without it ended in
// a setup, provider, inspection, storage or cancellation error. A passing report
// later rejected by the gate keeps its evidence and stays a verdict.
func IsInfrastructure(r Record) bool { return r.Completed && len(r.Evidence) == 0 }

// InfrastructureAttempts is a read-only summary of the completed infrastructure
// attempts for exactly one contract.
// Count is how many completed attempts ended without a reviewer verdict, LastAt
// is the latest of their CompletedAt times, and LatestIsInfra reports whether
// the newest completed record of any kind is one of them. Corruption and
// ambiguous ordering fail closed with an error, exactly as in LatestRecord.
type InfrastructureAttempts struct {
	Count         int
	LastAt        time.Time
	LatestIsInfra bool
}

// InfrastructureAttempts scans the contract's records without writing.
func (s *Store) InfrastructureAttempts(c Contract) (InfrastructureAttempts, error) {
	var out InfrastructureAttempts
	complete, err := s.completeRecords(c)
	if err != nil {
		return out, err
	}
	for _, r := range complete {
		if IsInfrastructure(r) {
			out.Count++
			if r.CompletedAt.After(out.LastAt) {
				out.LastAt = r.CompletedAt
			}
		}
	}
	if len(complete) > 0 {
		out.LatestIsInfra = IsInfrastructure(complete[len(complete)-1])
	}
	return out, nil
}

// CompleteRecords returns every completed record for exactly c, oldest first,
// with the same fail-closed corruption and ordering checks as LatestRecord. It
// is read-only and intended for status displays.
func (s *Store) CompleteRecords(c Contract) ([]Record, error) { return s.completeRecords(c) }

// completeRecords loads every completed record for the exact contract, ordered
// by start time. Start and completion order must agree strictly; ties or
// reversed completion order are ambiguous and fail closed.
func (s *Store) completeRecords(c Contract) ([]Record, error) {
	k, err := key(c)
	if err != nil {
		return nil, err
	}
	dir, err := s.root.Open(".")
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	var complete []Record
	for {
		entries, readErr := dir.ReadDir(128)
		if readErr != nil && readErr != io.EOF {
			return nil, readErr
		}
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasPrefix(name, k+".") || !strings.HasSuffix(name, ".record") {
				continue
			}
			r, err := s.loadRecord(c, name)
			if err != nil {
				return nil, fmt.Errorf("invalid acceptance reference %s: %w", name, err)
			}
			if !r.Completed {
				continue
			}
			if err := completeRecord(r); err != nil {
				return nil, err
			}
			complete = append(complete, r)
		}
		if readErr == io.EOF {
			break
		}
	}
	sort.Slice(complete, func(i, j int) bool { return complete[i].StartedAt.Before(complete[j].StartedAt) })
	for i := 1; i < len(complete); i++ {
		if !complete[i-1].StartedAt.Before(complete[i].StartedAt) || !complete[i-1].CompletedAt.Before(complete[i].CompletedAt) {
			return nil, errors.New("ambiguous acceptance attempt ordering")
		}
	}
	return complete, nil
}

func completeRecord(r Record) error {
	if r.StartedAt.IsZero() || r.CompletedAt.IsZero() || r.CompletedAt.Before(r.StartedAt) || !nonempty(r.Report) ||
		(r.Verdict != Passed && r.Verdict != Failed && r.Verdict != Blocked) {
		return errors.New("incoherent complete acceptance record")
	}
	if r.Plan.Text != "" && (r.Plan.StartedAt.Before(r.StartedAt) || r.Plan.CompletedAt.After(r.CompletedAt)) {
		return errors.New("incoherent acceptance plan timestamps")
	}
	if !r.ReviewStartedAt.IsZero() && (r.Plan.Text == "" || r.ReviewStartedAt.Before(r.Plan.CompletedAt) || r.ReviewStartedAt.After(r.CompletedAt)) {
		return errors.New("incoherent acceptance review timestamps")
	}
	if r.Verdict == Passed {
		return Evaluate(r.Contract, r)
	}
	return nil
}

// decodeStrict rejects duplicate keys, nonexact schema keys and trailing JSON
// values before encoding/json can apply its case-insensitive struct matching.
// RawMessage bodies receive their own typed validation when loaded below.
func decodeStrict(b []byte, value any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	var walk func(reflect.Type) error
	walk = func(typ reflect.Type) error {
		for typ != nil && typ.Kind() == reflect.Pointer {
			typ = typ.Elem()
		}
		if typ == reflect.TypeOf(json.RawMessage{}) {
			typ = nil
		}
		t, err := d.Token()
		if err != nil {
			return err
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			var fields map[string]reflect.Type
			if typ != nil {
				if typ.Kind() != reflect.Struct {
					return errors.New("JSON object does not match schema")
				}
				fields = exactFields(typ)
			}
			seen := map[string]bool{}
			for d.More() {
				k, err := d.Token()
				if err != nil {
					return err
				}
				key, ok := k.(string)
				folded := strings.ToLower(key)
				if !ok || seen[folded] {
					return errors.New("duplicate or invalid JSON key")
				}
				seen[folded] = true
				var child reflect.Type
				if fields != nil {
					var exists bool
					child, exists = fields[key]
					if !exists {
						return fmt.Errorf("unknown or nonexact JSON key %q", key)
					}
				}
				if err := walk(child); err != nil {
					return err
				}
			}
		case '[':
			var elem reflect.Type
			if typ != nil {
				if typ.Kind() != reflect.Array && typ.Kind() != reflect.Slice {
					return errors.New("JSON array does not match schema")
				}
				elem = typ.Elem()
			}
			for d.More() {
				if err := walk(elem); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("unexpected JSON delimiter %q", delim)
		}
		_, err = d.Token()
		return err
	}
	if err := walk(reflect.TypeOf(value)); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing JSON data")
	}
	d = json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	return d.Decode(value)
}

// exactFields mirrors the JSON tags used by our fixed schemas, including
// exported fields promoted from anonymous structs such as Native.Identity.
func exactFields(typ reflect.Type) map[string]reflect.Type {
	fields := make(map[string]reflect.Type)
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "-" {
			continue
		}
		if name == "" && field.Anonymous && field.Type.Kind() == reflect.Struct {
			for key, child := range exactFields(field.Type) {
				fields[key] = child
			}
			continue
		}
		if name == "" {
			name = field.Name
		}
		fields[name] = field.Type
	}
	return fields
}

// DevelopmentAtHead recovers historical development bindings for runs written
// before head_sha was recorded in run metadata. Only validated local records
// for this exact task and head are evidence; verdict and requirements may differ.
func (s *Store) DevelopmentAtHead(repo string, pr int, task, head string) (map[Identity]bool, error) {
	dir, err := s.root.Open(".")
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(10001)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if len(entries) > 10000 {
		return nil, errors.New("too many acceptance records")
	}
	identities := map[Identity]bool{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".record") {
			continue
		}
		var record Record
		if err := s.read(entry.Name(), &record); err != nil {
			return nil, err
		}
		c := record.Contract
		if c.Repository != repo || c.PRNumber != pr || c.TaskID != task || c.HeadSHA != head {
			continue
		}
		record, err = s.loadRecord(c, entry.Name())
		if err != nil {
			return nil, err
		}
		if !record.Completed {
			continue
		}
		if err := completeRecord(record); err != nil {
			return nil, err
		}
		identities[c.Development] = true
	}
	return identities, nil
}
