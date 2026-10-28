package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// pilotOverride accepts the documented nested/dotted setting and the legacy
// underscore spelling. Conflicting explicit values fail closed.
func pilotOverride(node *yaml.Node) (*bool, error) {
	var fields struct {
		Pilot struct {
			OverrideMerge *bool `yaml:"override_merge"`
		} `yaml:"pilot"`
		Dotted *bool `yaml:"pilot.override_merge"`
		Legacy *bool `yaml:"pilot_override_merge"`
	}
	if err := node.Decode(&fields); err != nil {
		return nil, err
	}
	var value *bool
	for _, candidate := range []*bool{fields.Pilot.OverrideMerge, fields.Dotted, fields.Legacy} {
		if candidate == nil {
			continue
		}
		if value != nil && *value != *candidate {
			return nil, fmt.Errorf("conflicting pilot.override_merge settings")
		}
		value = candidate
	}
	return value, nil
}

func (r *Repo) UnmarshalYAML(node *yaml.Node) error {
	type plain Repo
	decoded := plain(*r)
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	value, err := pilotOverride(node)
	if err != nil {
		return err
	}
	if value != nil {
		decoded.PilotOverrideMerge = *value
	}
	decoded.WorkerTags, err = migrateHostTag(node, decoded.WorkerTags)
	if err != nil {
		return err
	}
	*r = Repo(decoded)
	return nil
}

func (r *syncableRepo) UnmarshalYAML(node *yaml.Node) error {
	type plain syncableRepo
	decoded := plain(*r)
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	value, err := pilotOverride(node)
	if err != nil {
		return err
	}
	if value != nil {
		decoded.PilotOverrideMerge = *value
	}
	decoded.WorkerTags, err = migrateHostTag(node, decoded.WorkerTags)
	if err != nil {
		return err
	}
	*r = syncableRepo(decoded)
	return nil
}
