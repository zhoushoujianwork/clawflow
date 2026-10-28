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
	warnRetiredQueue(node)
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
	warnRetiredQueue(node)
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

// warnRetiredQueue reports ignored legacy queue selectors once per process.
// Decode nodes rather than typed values so even obsolete malformed values
// remain harmless. Never echo repository names or user-provided values.
func warnRetiredQueue(node *yaml.Node) {
	var input struct {
		Repos map[string]struct {
			Issue yaml.Node `yaml:"queue_issue"`
			Scope yaml.Node `yaml:"queue_scope"`
		} `yaml:"repos"`
		Settings struct {
			Queue struct {
				Scope yaml.Node `yaml:"scope"`
			} `yaml:"queue"`
		} `yaml:"settings"`
	}
	if err := node.Decode(&input); err != nil {
		return
	}
	found := input.Settings.Queue.Scope.Kind != 0
	for _, repo := range input.Repos {
		found = found || repo.Issue.Kind != 0 || repo.Scope.Kind != 0
	}
	if found {
		retiredQueueNotice.Do(func() {
			fmt.Fprintln(os.Stderr, "clawflow: retired queue selectors are ignored; only the clawflow label enrolls issues; obsolete keys will be dropped on save")
		})
	}
}

var retiredQueueNotice sync.Once
