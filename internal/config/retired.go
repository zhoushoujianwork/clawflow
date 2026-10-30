package config

import (
	"fmt"
	"os"
	"slices"
	"strings"
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
	for name, repo := range c.Repos {
		switch repo.AcceptanceWorker {
		case "":
			if c.Settings.Coordinator.Enabled {
				repo.AcceptanceWorker = "different"
				c.Repos[name] = repo
			}
		case "any", "different":
		default:
			return fmt.Errorf("invalid acceptance_worker: expected any or different")
		}
	}
	warnRetiredWorkflow(node)
	warnRetiredQueue(node)
	warnRetiredBinding(node)
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
	warnRetiredBinding(node)
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
			fmt.Fprintln(os.Stderr, "clawflow: retired queue selectors are ignored; open leaf issues are selected unless labeled clawflow-skip; obsolete keys will be dropped on save")
		})
	}
}

var retiredQueueNotice sync.Once

var retiredBindingNotice sync.Once

func warnRetiredBinding(node *yaml.Node) {
	var input struct {
		Settings struct {
			Binding yaml.Node `yaml:"require_binding"`
		} `yaml:"settings"`
	}
	if node.Decode(&input) == nil && input.Settings.Binding.Kind != 0 {
		retiredBindingNotice.Do(func() { fmt.Fprintln(os.Stderr, "clawflow: require_binding is retired and ignored; omitted on save") })
	}
}

// HostTag normalizes the hostname using the worker registration convention.
func HostTag(hostname string) string {
	if hostname == "" {
		return ""
	}
	return "host-" + strings.TrimSuffix(strings.ToLower(hostname), ".local")
}

// WithHostTag replaces host affinity while preserving other capabilities.
func (r Repo) WithHostTag(hostname string) Repo {
	tags := []string(nil)
	for _, tag := range r.WorkerTags {
		if !strings.HasPrefix(tag, "host-") {
			tags = append(tags, tag)
		}
	}
	if tag := HostTag(hostname); tag != "" {
		tags = append(tags, tag)
	}
	r.WorkerTags, r.BoundMachine = tags, ""
	return r
}

var retiredHostNotice sync.Once

func migrateHostTag(node *yaml.Node, tags []string) ([]string, error) {
	var input struct {
		Host string `yaml:"bound_machine"`
	}
	if err := node.Decode(&input); err != nil {
		return nil, err
	}
	if input.Host != "" {
		tag := HostTag(input.Host)
		if !slices.Contains(tags, tag) {
			tags = append(tags, tag)
		}
		retiredHostNotice.Do(func() {
			fmt.Fprintln(os.Stderr, "clawflow: bound_machine migrated to a required host tag in worker_tags; omitted on save")
		})
	}
	return tags, nil
}

func (r Repo) MarshalYAML() (any, error) {
	type current Repo
	if tag := HostTag(r.BoundMachine); tag != "" && !slices.Contains(r.WorkerTags, tag) {
		r.WorkerTags = append(append([]string(nil), r.WorkerTags...), tag)
	}
	return current(r), nil
}

func (r syncableRepo) MarshalYAML() (any, error) {
	type current syncableRepo
	if tag := HostTag(r.BoundMachine); tag != "" && !slices.Contains(r.WorkerTags, tag) {
		r.WorkerTags = append(append([]string(nil), r.WorkerTags...), tag)
	}
	return current(r), nil
}
