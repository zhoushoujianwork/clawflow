package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"github.com/zhoushoujianwork/clawflow/internal/pilot/budget"
	"github.com/zhoushoujianwork/clawflow/internal/vcs/gitlab"
)

func newCIVariableCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "variable", Aliases: []string{"variables"}, Short: "Manage project CI/CD variables (Settings > CI/CD)"}
	for _, action := range []string{"list", "view", "create", "update", "delete"} {
		cmd.AddCommand(newCIVariableActionCmd(action))
	}
	return cmd
}

func newCIVariableActionCmd(action string) *cobra.Command {
	var repo, key, scope, value, valueFile, variableType, description string
	var asJSON, showValues, protected, masked, raw bool
	write := action == "create" || action == "update"
	cmd := &cobra.Command{
		Use: action, Short: action + " project CI/CD variables", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var opts gitlab.VariableOptions
			if cmd.Flags().Changed("scope") && scope == "" {
				return fmt.Errorf("scope must not be empty")
			}
			if write {
				if !cmd.Flags().Changed("value") && !cmd.Flags().Changed("value-file") {
					return fmt.Errorf("provide --value or --value-file (use - for stdin)")
				}
				if cmd.Flags().Changed("value-file") {
					var data []byte
					var err error
					if valueFile == "-" {
						data, err = io.ReadAll(cmd.InOrStdin())
					} else {
						data, err = os.ReadFile(valueFile)
					}
					if err != nil {
						return fmt.Errorf("read variable value: %w", err)
					}
					value = string(data)
				}
				opts.Value = &value
				if cmd.Flags().Changed("type") {
					if variableType != "env_var" && variableType != "file" {
						return fmt.Errorf("type must be env_var or file")
					}
					opts.VariableType = &variableType
				}
				if cmd.Flags().Changed("protected") {
					opts.Protected = &protected
				}
				if cmd.Flags().Changed("masked") {
					opts.Masked = &masked
				}
				if cmd.Flags().Changed("raw") {
					opts.Raw = &raw
				}
				if cmd.Flags().Changed("description") {
					opts.Description = &description
				}
				if action == "create" && cmd.Flags().Changed("scope") {
					opts.EnvironmentScope = &scope
				}
			}
			client, canonical, err := newGitLabClientForRepo(repo)
			if err != nil {
				return err
			}
			if write || action == "delete" {
				if err := budget.Reserve("ci variable " + action); err != nil {
					return err
				}
			}
			var variables []gitlab.Variable
			var v gitlab.Variable
			switch action {
			case "list":
				variables, err = client.ListProjectVariables(canonical)
			case "view":
				v, err = client.GetProjectVariable(canonical, key, scope)
			case "create":
				v, err = client.CreateProjectVariable(canonical, key, opts)
			case "update":
				v, err = client.UpdateProjectVariable(canonical, key, scope, opts)
			case "delete":
				err = client.DeleteProjectVariable(canonical, key, scope)
			}
			if err != nil {
				return err
			}
			if action == "delete" {
				if asJSON {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{"key": key, "deleted": true, "environment_scope": scope})
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Deleted variable %s\n", key)
				return nil
			}
			if action != "list" {
				variables = []gitlab.Variable{v}
			}
			for i := range variables {
				if !showValues {
					variables[i].Value = "[REDACTED]"
				}
			}
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if action == "list" {
					return enc.Encode(variables)
				}
				return enc.Encode(variables[0])
			}
			fmt.Fprintln(cmd.OutOrStdout(), "KEY\tSCOPE\tTYPE\tPROTECTED\tMASKED\tVALUE")
			for _, variable := range variables {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\t%t\t%t\t%s\n", variable.Key, variable.EnvironmentScope, variable.VariableType, variable.Protected, variable.Masked, variable.Value)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "GitLab group/project or project URL (required)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "output as JSON (values remain redacted unless --show-values)")
	_ = cmd.MarkFlagRequired("repo")
	if action != "list" {
		cmd.Flags().StringVar(&key, "key", "", "variable key (required)")
		cmd.Flags().StringVar(&scope, "scope", "", "exact environment scope (create: new scope; other actions: select existing scope)")
		_ = cmd.MarkFlagRequired("key")
	}
	if action != "delete" {
		cmd.Flags().BoolVar(&showValues, "show-values", false, "include actual variable values in output")
	}
	if write {
		cmd.Flags().StringVar(&value, "value", "", "variable value; explicit empty string is supported")
		cmd.Flags().StringVar(&valueFile, "value-file", "", "read value from a file, or - for stdin; preserves newlines")
		cmd.MarkFlagsMutuallyExclusive("value", "value-file")
		cmd.Flags().StringVar(&variableType, "type", "", "env_var or file; omitted settings are preserved on update")
		cmd.Flags().StringVar(&description, "description", "", "variable description (requires server support)")
		cmd.Flags().BoolVar(&protected, "protected", false, "restrict variable to protected refs")
		cmd.Flags().BoolVar(&masked, "masked", false, "mask variable in job logs (GitLab value constraints apply)")
		cmd.Flags().BoolVar(&raw, "raw", false, "disable variable expansion (requires server support)")
	}
	return cmd
}
