package component

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/zhoushoujianwork/clawflow/internal/filelock"
)

// Load is read-only; absence means an empty inventory, never automatic onboarding.
func Load(path string) (Registry, error) {
	r := Registry{Schema: Schema, Records: map[string]Record{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return r, ErrRefused
	}
	r = Registry{}
	if json.Unmarshal(data, &r) != nil || r.Schema != Schema || r.Records == nil {
		return Registry{}, ErrRefused
	}
	return r, nil
}

// Update serializes mutations and persists failed probes (which disable bindings).
// Errors are fixed strings so neither adapter errors nor local paths are logged.
func Update(path string, fn func(*Registry) error) error {
	if os.MkdirAll(filepath.Dir(path), 0700) != nil {
		return ErrRefused
	}
	lock, err := filelock.Try(path+".lock", map[string]any{"pid": os.Getpid()})
	if err != nil {
		return ErrRefused
	}
	defer filelock.Release(lock)
	r, err := Load(path)
	if err != nil {
		return err
	}
	operationErr := fn(&r)
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return ErrRefused
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".components-*")
	if err != nil {
		return ErrRefused
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return ErrRefused
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return ErrRefused
	}
	if err = f.Close(); err != nil {
		return ErrRefused
	}
	if os.Rename(f.Name(), path) != nil {
		return ErrRefused
	}
	return operationErr
}
