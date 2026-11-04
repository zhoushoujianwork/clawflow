package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrConfigNewer reports that the file holds settings this binary cannot
// represent. Saving would silently delete them, so the save is refused.
var ErrConfigNewer = errors.New("config contains settings unknown to this clawflow")

// Save atomically replaces the config file. It refuses to run when the existing
// file was written by a newer binary, because an older binary would drop every
// field it does not know, including the database and coordinator settings.
func (c *Config) Save() error {
	if c.Repos == nil {
		c.Repos = make(map[string]Repo)
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("cannot marshal config: %w", err)
	}
	path := ConfigPath()
	mode := os.FileMode(0o600)
	existing, err := os.ReadFile(path)
	switch {
	case err == nil:
		if unknown := unknownConfigKeys(existing); len(unknown) > 0 {
			// Key names only: values can contain credentials.
			return fmt.Errorf("refusing to save %s: %w (%s); run the current clawflow binary", path, ErrConfigNewer, strings.Join(unknown, ", "))
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		mode = info.Mode().Perm()
	case !os.IsNotExist(err):
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return replaceFile(path, data, mode)
}

// unknownConfigKeys lists keys in data that Config does not declare. Malformed
// input yields none: Load reports it, and this check only guards against loss.
func unknownConfigKeys(data []byte) []string {
	var doc yaml.Node
	if yaml.Unmarshal(data, &doc) != nil || len(doc.Content) == 0 {
		return nil
	}
	found := map[string]bool{}
	collectUnknownKeys(doc.Content[0], reflect.TypeOf(Config{}), "", found)
	out := make([]string, 0, len(found))
	for key := range found {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func collectUnknownKeys(node *yaml.Node, t reflect.Type, path string, found map[string]bool) {
	for node.Kind == yaml.AliasNode && node.Alias != nil {
		node = node.Alias
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		if node.Kind != yaml.MappingNode {
			return
		}
		fields := yamlFields(t)
		for i := 0; i+1 < len(node.Content); i += 2 {
			name := node.Content[i].Value
			if name == "<<" {
				collectUnknownKeys(node.Content[i+1], t, path, found)
				continue
			}
			key := joinKey(path, name)
			if field, ok := fields[name]; ok {
				collectUnknownKeys(node.Content[i+1], field, key, found)
			} else if !retiredConfigKeys[key] {
				found[key] = true
			}
		}
	case reflect.Map:
		if node.Kind != yaml.MappingNode {
			return
		}
		for i := 0; i+1 < len(node.Content); i += 2 {
			collectUnknownKeys(node.Content[i+1], t.Elem(), joinKey(path, "*"), found)
		}
	case reflect.Slice, reflect.Array:
		if node.Kind != yaml.SequenceNode {
			return
		}
		for _, item := range node.Content {
			collectUnknownKeys(item, t.Elem(), path, found)
		}
	}
}

// yamlFields maps the keys yaml.v3 accepts for t to their field types.
func yamlFields(t reflect.Type) map[string]reflect.Type {
	fields := map[string]reflect.Type{}
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		name, options, _ := strings.Cut(field.Tag.Get("yaml"), ",")
		if name == "-" {
			continue
		}
		if strings.Contains(options, "inline") {
			for key, value := range yamlFields(field.Type) {
				fields[key] = value
			}
			continue
		}
		if name == "" {
			name = strings.ToLower(field.Name)
		}
		fields[name] = field.Type
	}
	return fields
}

func joinKey(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

// replaceFile leaves either the old or the new content, never a partial file.
// A symlinked config stays a symlink: the rename happens at its target.
func replaceFile(path string, data []byte, mode os.FileMode) error {
	if target, err := filepath.EvalSymlinks(path); err == nil {
		path = target
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err = tmp.Chmod(mode); err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	handle, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer handle.Close()
	return handle.Sync()
}
