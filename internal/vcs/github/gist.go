package github

import (
	"encoding/json"
	"fmt"
)

// GistFile represents a single file inside a GitHub Gist.
type GistFile struct {
	Filename string `json:"filename"`
	Content  string `json:"content"`
}

// Gist is a minimal representation of a GitHub Gist.
type Gist struct {
	ID          string              `json:"id"`
	Description string              `json:"description"`
	Public      bool                `json:"public"`
	Files       map[string]GistFile `json:"files"`
}

// ListGists returns all Gists owned by the authenticated user.
func (c *Client) ListGists() ([]Gist, error) {
	data, status, err := c.do("GET", "/gists?per_page=100", nil)
	if err != nil {
		return nil, err
	}
	if status == 403 || status == 404 {
		return nil, fmt.Errorf("cannot list gists: HTTP %d — token may be missing the 'gist' scope", status)
	}
	if status != 200 {
		return nil, fmt.Errorf("github list gists: HTTP %d: %s", status, data)
	}
	var gists []Gist
	if err := json.Unmarshal(data, &gists); err != nil {
		return nil, err
	}
	return gists, nil
}

// FindGistByDescription returns the first Gist whose description matches, or nil if none found.
func (c *Client) FindGistByDescription(description string) (*Gist, error) {
	gists, err := c.ListGists()
	if err != nil {
		return nil, err
	}
	for i := range gists {
		if gists[i].Description == description {
			return &gists[i], nil
		}
	}
	return nil, nil
}

// GetGist fetches a single Gist by ID, including full file contents.
func (c *Client) GetGist(id string) (*Gist, error) {
	data, status, err := c.do("GET", "/gists/"+id, nil)
	if err != nil {
		return nil, err
	}
	if status == 404 {
		return nil, fmt.Errorf("gist %q not found", id)
	}
	if status != 200 {
		return nil, fmt.Errorf("github get gist: HTTP %d: %s", status, data)
	}
	var g Gist
	if err := json.Unmarshal(data, &g); err != nil {
		return nil, err
	}
	return &g, nil
}

// buildAPIFiles converts a filename→content map into the nested shape the
// Gist API expects, dropping any entry whose content is empty.
//
// GitHub treats a file entry with empty content as a request to DELETE that
// file. ClawFlow only ever pushes real content, so an empty entry is always
// unintended — and worse, if every entry is empty the resulting body becomes
// {"files":{}}, which GitHub rejects with 422 missing_field "files" (see
// issue #195). Stripping empties here prevents both the accidental deletion
// and the malformed-payload death loop.
func buildAPIFiles(files map[string]string) map[string]map[string]string {
	apiFiles := make(map[string]map[string]string, len(files))
	for name, content := range files {
		if content == "" {
			continue
		}
		apiFiles[name] = map[string]string{"content": content}
	}
	return apiFiles
}

// CreateGist creates a new private Gist with the given description and files.
// files maps filename → content.
func (c *Client) CreateGist(description string, files map[string]string) (*Gist, error) {
	apiFiles := buildAPIFiles(files)
	if len(apiFiles) == 0 {
		return nil, fmt.Errorf("refusing to create gist with no non-empty files")
	}
	body := map[string]any{
		"description": description,
		"public":      false,
		"files":       apiFiles,
	}
	data, status, err := c.do("POST", "/gists", body)
	if err != nil {
		return nil, err
	}
	if status == 403 || status == 404 {
		return nil, fmt.Errorf("cannot create gist: HTTP %d — token may be missing the 'gist' scope", status)
	}
	if status != 201 {
		return nil, fmt.Errorf("github create gist: HTTP %d: %s", status, data)
	}
	var g Gist
	if err := json.Unmarshal(data, &g); err != nil {
		return nil, err
	}
	return &g, nil
}

// UpdateGist updates the files in an existing Gist.
// files maps filename → content.
func (c *Client) UpdateGist(id string, files map[string]string) (*Gist, error) {
	apiFiles := buildAPIFiles(files)
	if len(apiFiles) == 0 {
		// Sending {"files":{}} would either delete every file or be rejected
		// with 422 missing_field "files". Refuse explicitly instead.
		return nil, fmt.Errorf("refusing to update gist %s with no non-empty files (would delete all content)", id)
	}
	body := map[string]any{"files": apiFiles}
	data, status, err := c.do("PATCH", "/gists/"+id, body)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("github update gist: HTTP %d: %s", status, data)
	}
	var g Gist
	if err := json.Unmarshal(data, &g); err != nil {
		return nil, err
	}
	return &g, nil
}

// GetAuthenticatedUser returns the login name of the authenticated user.
// It calls GET /user and is used to validate a token during login.
func (c *Client) GetAuthenticatedUser() (string, error) {
	data, status, err := c.do("GET", "/user", nil)
	if err != nil {
		return "", err
	}
	if status == 401 {
		return "", fmt.Errorf("invalid or expired token (HTTP 401)")
	}
	if status != 200 {
		return "", fmt.Errorf("github get user: HTTP %d: %s", status, data)
	}
	var raw struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return "", err
	}
	return raw.Login, nil
}
