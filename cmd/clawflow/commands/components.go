package commands

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/component"
	"github.com/zhoushoujianwork/clawflow/internal/project"
)

func NewComponentsCmd() *cobra.Command {
	home, _ := os.UserHomeDir()
	return newComponentsCmd(filepath.Join(home, ".clawflow", "data", "components.json"), component.Builtins(loadExecutionCheckConfig), func(name string) bool { _, err := project.Get(name); return err == nil })
}

func newComponentsCmd(path string, adapters []component.Adapter, projectExists func(string) bool) *cobra.Command {
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
	}
	for _, action := range []string{"register", "verify", "bind", "activate", "disable"} {
		var binding component.Binding
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
			if action == "bind" || action == "activate" || action == "disable" {
				// Check the project name before using project.Get's filesystem path.
				if !validComponentProject(binding.Project) || !projectExists(binding.Project) {
					return component.ErrRefused
				}
			}
			return component.Update(path, func(r *component.Registry) error {
				switch action {
				case "register":
					return r.Register(adapter)
				case "verify":
					return r.Verify(cmd.Context(), args[0], adapter)
				case "bind":
					return r.Bind(args[0], binding)
				case "activate":
					return r.Activate(cmd.Context(), args[0], binding.Project, binding.Role, adapter)
				case "disable":
					return r.Disable(args[0], binding.Project, binding.Role)
				}
				return component.ErrRefused
			})
		}}
		if action == "bind" || action == "activate" || action == "disable" {
			c.Flags().StringVar(&binding.Project, "project", "", "Existing project name")
			c.Flags().StringVar(&binding.Role, "role", "", "Project-local role")
			_ = c.MarkFlagRequired("project")
			_ = c.MarkFlagRequired("role")
		}
		if action == "bind" {
			c.Flags().StringVar(&binding.Resource, "resource", "", "Opaque resource reference")
			c.Flags().StringVar(&binding.CredentialRef, "credential-ref", "", "Opaque projects/<project>/credentials/<role> slot; never a secret")
			c.Flags().StringSliceVar(&binding.Capabilities, "capability", nil, "Approved capabilities")
			_ = c.MarkFlagRequired("resource")
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
