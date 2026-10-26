package gitlab

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
)

// Variable is a project-level CI/CD variable from Settings > CI/CD.
type Variable struct {
	Key              string `json:"key"`
	Value            string `json:"value"`
	VariableType     string `json:"variable_type"`
	EnvironmentScope string `json:"environment_scope"`
	Protected        bool   `json:"protected"`
	Masked           bool   `json:"masked"`
	Raw              bool   `json:"raw"`
	Hidden           bool   `json:"hidden"`
	Description      string `json:"description"`
}

// VariableOptions sends only explicitly requested changes, including false and
// empty values. Older GitLab versions can reject newer optional attributes.
type VariableOptions struct {
	Value            *string `json:"value,omitempty"`
	VariableType     *string `json:"variable_type,omitempty"`
	EnvironmentScope *string `json:"environment_scope,omitempty"`
	Protected        *bool   `json:"protected,omitempty"`
	Masked           *bool   `json:"masked,omitempty"`
	Raw              *bool   `json:"raw,omitempty"`
	Description      *string `json:"description,omitempty"`
}

func normalizeVariable(v *Variable) {
	if v.EnvironmentScope == "" {
		v.EnvironmentScope = "*"
	}
	if v.VariableType == "" {
		v.VariableType = "env_var"
	}
}

var variableKey = regexp.MustCompile(`^[A-Za-z0-9_]{1,255}$`)

func validateVariable(key string, opts VariableOptions) error {
	if !variableKey.MatchString(key) {
		return fmt.Errorf("variable key must contain 1-255 letters, digits, or underscores")
	}
	if opts.VariableType != nil && *opts.VariableType != "env_var" && *opts.VariableType != "file" {
		return fmt.Errorf("variable type must be env_var or file")
	}
	if opts.EnvironmentScope != nil && *opts.EnvironmentScope == "" {
		return fmt.Errorf("environment scope must not be empty")
	}
	return nil
}

// ListProjectVariables follows all pages, rather than silently truncating at 100.
func (c *Client) ListProjectVariables(repo string) ([]Variable, error) {
	variables := []Variable{}
	for page := 1; ; page++ {
		path := fmt.Sprintf("/projects/%s/variables?per_page=100&page=%d", projectID(repo), page)
		data, status, err := c.doJSON("GET", path, nil)
		if err != nil {
			return nil, err
		}
		if status != 200 {
			// Do not echo API bodies: they may contain variable values.
			return nil, fmt.Errorf("gitlab list project variables: HTTP %d", status)
		}
		var batch []Variable
		if err := json.Unmarshal(data, &batch); err != nil {
			return nil, fmt.Errorf("decode project variables: %w", err)
		}
		for i := range batch {
			normalizeVariable(&batch[i])
		}
		variables = append(variables, batch...)
		if len(batch) < 100 {
			return variables, nil
		}
	}
}

func variablePath(repo, key, scope string) string {
	path := fmt.Sprintf("/projects/%s/variables/%s", projectID(repo), url.PathEscape(key))
	if scope != "" {
		q := url.Values{"filter[environment_scope]": {scope}}
		path += "?" + q.Encode()
	}
	return path
}

// GetProjectVariable leaves ambiguous keys to GitLab to reject when scope is
// omitted. A returned scope mismatch is rejected for older servers that ignore
// the filter parameter.
func (c *Client) GetProjectVariable(repo, key, scope string) (Variable, error) {
	if err := validateVariable(key, VariableOptions{}); err != nil {
		return Variable{}, err
	}
	data, status, err := c.doJSON("GET", variablePath(repo, key, scope), nil)
	if err != nil {
		return Variable{}, err
	}
	if status != 200 {
		return Variable{}, fmt.Errorf("gitlab get project variable: HTTP %d (check key, scope, and permissions)", status)
	}
	var v Variable
	if err := json.Unmarshal(data, &v); err != nil {
		return Variable{}, fmt.Errorf("decode project variable: %w", err)
	}
	if v.Key != key {
		return Variable{}, fmt.Errorf("GitLab returned a different variable key")
	}
	normalizeVariable(&v)
	if scope != "" && v.EnvironmentScope != scope {
		return Variable{}, fmt.Errorf("GitLab returned a different environment scope; scoped variables may be unsupported by this server")
	}
	return v, nil
}

func (c *Client) CreateProjectVariable(repo, key string, opts VariableOptions) (Variable, error) {
	if err := validateVariable(key, opts); err != nil {
		return Variable{}, err
	}
	if opts.Value == nil {
		return Variable{}, fmt.Errorf("variable value is required")
	}
	body := struct {
		Key string `json:"key"`
		VariableOptions
	}{key, opts}
	v, err := c.writeProjectVariable("POST", fmt.Sprintf("/projects/%s/variables", projectID(repo)), body, 201)
	if err == nil && opts.EnvironmentScope != nil && v.EnvironmentScope != *opts.EnvironmentScope {
		return Variable{}, fmt.Errorf("variable was created with scope %q instead of the requested scope; this server may not support environment scopes", v.EnvironmentScope)
	}
	return v, err
}

func (c *Client) UpdateProjectVariable(repo, key, scope string, opts VariableOptions) (Variable, error) {
	if err := validateVariable(key, opts); err != nil {
		return Variable{}, err
	}
	if opts.Value == nil {
		return Variable{}, fmt.Errorf("variable value is required")
	}
	if _, err := c.GetProjectVariable(repo, key, scope); err != nil {
		return Variable{}, err
	}
	return c.writeProjectVariable("PUT", variablePath(repo, key, scope), opts, 200)
}

func (c *Client) writeProjectVariable(method, path string, body any, wantStatus int) (Variable, error) {
	data, status, err := c.doJSON(method, path, body)
	if err != nil {
		return Variable{}, err
	}
	if status != wantStatus {
		return Variable{}, fmt.Errorf("gitlab %s project variable: HTTP %d (check key, scope, permissions, and variable constraints)", method, status)
	}
	var v Variable
	if err := json.Unmarshal(data, &v); err != nil {
		return Variable{}, fmt.Errorf("decode project variable: %w", err)
	}
	normalizeVariable(&v)
	return v, nil
}

func (c *Client) DeleteProjectVariable(repo, key, scope string) error {
	if _, err := c.GetProjectVariable(repo, key, scope); err != nil {
		return err
	}
	_, status, err := c.doJSON("DELETE", variablePath(repo, key, scope), nil)
	if err != nil {
		return err
	}
	if status != 204 {
		return fmt.Errorf("gitlab delete project variable: HTTP %d", status)
	}
	return nil
}
