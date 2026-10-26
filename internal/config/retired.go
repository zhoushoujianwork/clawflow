package config

import (
	"fmt"
	"os"
	"sync"

	"gopkg.in/yaml.v3"
)

var retiredWorkflowNotice sync.Once

// UnmarshalYAML accepts retired configuration keys without persisting them.
func (c *Config) UnmarshalYAML(node *yaml.Node) error {
	type current Config
	if err := node.Decode((*current)(c)); err != nil {
		return err
	}
	warnRetiredWorkflow(node)
	return nil
}

// Sync accepts the same retired input as local configuration. Normalizing at
// decode time also prevents retired fields from creating LWW conflicts.
func (p *gistPayload) UnmarshalYAML(node *yaml.Node) error {
	type current gistPayload
	if err := node.Decode((*current)(p)); err != nil {
		return err
	}
	warnRetiredWorkflow(node)
	return nil
}

func warnRetiredWorkflow(node *yaml.Node) {
	var input struct {
		Repos map[string]struct {
			Workflow yaml.Node `yaml:"workflow"`
		} `yaml:"repos"`
	}
	if err := node.Decode(&input); err != nil {
		return
	}
	for _, repo := range input.Repos {
		workflow := repo.Workflow
		for workflow.Kind == yaml.AliasNode && workflow.Alias != nil {
			workflow = *workflow.Alias
		}
		if workflow.Kind != 0 && workflow.Tag != "!!null" && !(workflow.Kind == yaml.ScalarNode && (workflow.Value == "" || workflow.Value == "autonomous")) {
			retiredWorkflowNotice.Do(func() {
				// Never echo input values: configuration can contain credentials.
				fmt.Fprintln(os.Stderr, "clawflow: retired workflow setting migrated to autonomous dispatch; enablement, CI and auto-merge settings are unchanged")
			})
			return
		}
	}
}
