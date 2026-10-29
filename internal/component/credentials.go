package component

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
)

// CredentialResolver resolves an explicitly granted project/role/resource scope.
// Secrets are transient and must never be included in observations or records.
type CredentialResolver func(string, Binding) (string, error)

type CredentialGrant struct {
	Component     string   `json:"component"`
	Project       string   `json:"project"`
	Role          string   `json:"role"`
	Resource      string   `json:"resource"`
	CredentialRef string   `json:"credential_ref"`
	Capabilities  []string `json:"capabilities"`
	Environment   string   `json:"environment"`
}

type CredentialGrants struct {
	Schema int               `json:"schema"`
	Grants []CredentialGrant `json:"grants"`
}

var environmentName = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)

// FileCredentials rereads grants and environment on every use, so removal,
// narrowing and revocation take effect even for already-active bindings. There
// is deliberately no global VCS token fallback and no configuration migration.
func FileCredentials(path string) CredentialResolver {
	return func(id string, b Binding) (string, error) {
		if !validCredentialScope(b) {
			return "", ErrRefused
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", ErrRefused
		}
		var grants CredentialGrants
		if json.Unmarshal(data, &grants) != nil || grants.Schema != Schema {
			return "", ErrRefused
		}
		var match *CredentialGrant
		for _, g := range grants.Grants {
			if g.Component == id && g.Project == b.Project && g.Role == b.Role && g.Resource == b.Resource && g.CredentialRef == b.CredentialRef {
				if match != nil || !contains(g.Capabilities, b.Capabilities) || !environmentName.MatchString(g.Environment) {
					return "", ErrRefused
				}
				copy := g
				match = &copy
			}
		}
		if match == nil {
			return "", ErrRefused
		}
		secret := os.Getenv(match.Environment)
		if secret == "" {
			return "", ErrRefused
		}
		return secret, nil
	}
}

func validCredentialScope(b Binding) bool {
	return validName(b.Project) && !strings.Contains(b.Project, "/") && validName(b.Role) && !strings.Contains(b.Role, "/") && validName(b.Resource) && validCaps(b.Capabilities) &&
		b.CredentialRef == "projects/"+b.Project+"/credentials/"+b.Role
}

func (r *Registry) credential(id string, b Binding) error {
	if !validCredentialScope(b) || r.Credentials == nil {
		return ErrRefused
	}
	secret, err := r.Credentials(id, b)
	if err != nil || secret == "" {
		return ErrRefused
	}
	return nil
}

func (r *Registry) scopedBinding(id, project, role, state string) (Binding, error) {
	for _, b := range r.Records[id].Bindings {
		if b.Project == project && b.Role == role && b.State == state {
			if r.credential(id, b) != nil {
				_ = r.Disable(id, project, role)
				return Binding{}, ErrRefused
			}
			return b, nil
		}
	}
	return Binding{}, ErrRefused
}
