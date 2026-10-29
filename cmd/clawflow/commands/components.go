package commands

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/component"
	"github.com/zhoushoujianwork/clawflow/internal/project"
	"github.com/zhoushoujianwork/clawflow/internal/vcs/github"
)

func NewComponentsCmd() *cobra.Command {
	home, _ := os.UserHomeDir()
	return newComponentsCmd(filepath.Join(home, ".clawflow", "data", "components.json"), component.Builtins(loadExecutionCheckConfig), func(name string) bool { _, err := project.Get(name); return err == nil }, componentCommandAccess{
		credentials: component.FileCredentials(filepath.Join(home, ".clawflow", "config", "component-access.json")),
		github: func(ctx context.Context, token string) component.GitHubEvidenceSource {
			c := github.New(token, "")
			c.SetRequestContext(ctx)
			return c
		},
	})
}

type componentCommandAccess struct {
	credentials component.CredentialResolver
	github      func(context.Context, string) component.GitHubEvidenceSource
}

func newComponentsCmd(path string, adapters []component.Adapter, projectExists func(string) bool, access componentCommandAccess) *cobra.Command {
	root := &cobra.Command{Use: "components", Short: "Manage opt-in component inventory and project bindings"}
	root.AddCommand(&cobra.Command{Use: "discover", Short: "Read-only inventory; does not probe or register", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		records, err := component.Inventory(adapters)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(records)
	}})
	root.AddCommand(&cobra.Command{Use: "list", Short: "Read the saved component inventory", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		r, err := component.Load(path)
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(r)
	}})
	descriptions := map[string]string{
		"register": "Register a known component without enabling it",
		"verify":   "Probe identity and compatibility; invalidate previous bindings",
		"bind":     "Pin approved capabilities to an existing project and role",
		"activate": "Explicitly activate one binding after a fresh probe",
		"disable":  "Disable one project and role binding",
		"check":    "Recheck an active binding, credentials and provider health",
	}
	for _, action := range []string{"register", "verify", "bind", "activate", "disable", "check"} {
		var binding component.Binding
		var pr int
		var head string
		c := &cobra.Command{Use: action + " <component>", Short: descriptions[action], Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			var adapter component.Adapter
			for _, a := range adapters {
				if a.Discover().ID == args[0] {
					adapter = a
					break
				}
			}
			if adapter == nil {
				return component.ErrRefused
			}
			if action == "bind" || action == "activate" || action == "disable" || action == "check" {
				// Check the project name before using project.Get's filesystem path.
				if !validComponentProject(binding.Project) || !projectExists(binding.Project) {
					return component.ErrRefused
				}
			}
			return component.Update(path, func(r *component.Registry) error {
				r.Credentials = access.credentials
				if args[0] == "github-actions" && (action == "verify" || action == "activate" || action == "check") {
					scope := binding
					if action != "verify" {
						found := false
						for _, b := range r.Records[args[0]].Bindings {
							if b.Project == binding.Project && b.Role == binding.Role {
								scope = b
								found = true
								break
							}
						}
						if !found {
							return component.ErrRefused
						}
					}
					scope.Capabilities = []string{"exact-revision-ci"}
					descriptor := adapter.Discover()
					adapter = component.ReadAdapter{Descriptor: descriptor, Observe: func(ctx context.Context) (component.Observation, error) {
						if !validComponentProject(scope.Project) || !projectExists(scope.Project) || access.credentials == nil || access.github == nil {
							return component.Observation{}, component.ErrRefused
						}
						token, err := access.credentials(args[0], scope)
						if err != nil || token == "" {
							return component.Observation{}, component.ErrRefused
						}
						probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
						defer cancel()
						return component.GitHubActions(access.github(probeCtx, token), scope.Resource, pr, head).Probe(probeCtx)
					}}
				}

				switch action {
				case "register":
					return r.Register(adapter)
				case "verify":
					return r.Verify(cmd.Context(), args[0], adapter)
				case "bind":
					return r.Bind(args[0], binding)
				case "activate":
					return r.Activate(cmd.Context(), args[0], binding.Project, binding.Role, adapter)
				case "check":
					if len(binding.Capabilities) != 1 {
						return component.ErrRefused
					}
					return r.Authorize(cmd.Context(), args[0], binding.Project, binding.Role, binding.Capabilities[0], adapter)
				case "disable":
					return r.Disable(args[0], binding.Project, binding.Role)
				}
				return component.ErrRefused
			})
		}}
		if action == "bind" || action == "activate" || action == "disable" || action == "check" || action == "verify" {
			c.Flags().StringVar(&binding.Project, "project", "", "Existing project name")
			c.Flags().StringVar(&binding.Role, "role", "", "Project-local role")
			if action != "verify" {
				_ = c.MarkFlagRequired("project")
				_ = c.MarkFlagRequired("role")
			}
		}
		if action == "bind" || action == "verify" {
			c.Flags().StringVar(&binding.Resource, "resource", "", "Opaque resource reference")
			c.Flags().StringVar(&binding.CredentialRef, "credential-ref", "", "Opaque projects/<project>/credentials/<role> slot; never a secret")
			c.Flags().StringSliceVar(&binding.Capabilities, "capability", nil, "Approved capabilities")
			if action == "bind" {
				_ = c.MarkFlagRequired("resource")
				_ = c.MarkFlagRequired("capability")
				_ = c.MarkFlagRequired("credential-ref")
			}
		}
		if action == "verify" || action == "activate" || action == "check" {
			c.Flags().IntVar(&pr, "pr", 0, "Known GitHub PR for a read-only health probe")
			c.Flags().StringVar(&head, "sha", "", "Exact PR head SHA for the GitHub evidence probe")
		}
		if action == "check" {
			c.Flags().StringSliceVar(&binding.Capabilities, "capability", nil, "Single capability to authorize")
			_ = c.MarkFlagRequired("capability")
		}
		root.AddCommand(c)
	}
	return root
}

func validComponentProject(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.') {
			return false
		}
	}
	return true
}
