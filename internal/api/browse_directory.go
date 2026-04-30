// Package api: directory browser endpoint for the settings page.
package api

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// directoryBrowserResponse is what GET /api/browse-directory returns.
type directoryBrowserResponse struct {
	CurrentPath  string      `json:"current_path"`
	Parent       string      `json:"parent"`
	Directories  []dirEntry  `json:"directories"`
	Error        string      `json:"error,omitempty"`
}

type dirEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// HandleBrowseDirectory serves GET /api/browse-directory?path=<path>.
// Returns a list of subdirectories in the requested path, plus the
// parent directory for navigation. Used by the settings page's
// directory picker.
func HandleBrowseDirectory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	path := r.URL.Query().Get("path")
	if path == "" {
		// Default to user's home directory
		home, err := os.UserHomeDir()
		if err != nil {
			writeJSON(w, 500, directoryBrowserResponse{Error: "cannot determine home directory"})
			return
		}
		path = home
	}

	// Expand ~ to home directory
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, path[2:])
	}

	// Clean the path to prevent directory traversal issues
	path = filepath.Clean(path)

	// Check if path exists and is a directory
	info, err := os.Stat(path)
	if err != nil {
		writeJSON(w, 200, directoryBrowserResponse{
			CurrentPath: path,
			Error:       "path does not exist or is not accessible",
		})
		return
	}
	if !info.IsDir() {
		writeJSON(w, 200, directoryBrowserResponse{
			CurrentPath: path,
			Error:       "path is not a directory",
		})
		return
	}

	// Read directory contents
	entries, err := os.ReadDir(path)
	if err != nil {
		writeJSON(w, 200, directoryBrowserResponse{
			CurrentPath: path,
			Error:       "cannot read directory: " + err.Error(),
		})
		return
	}

	// Filter to only directories, excluding hidden ones
	var dirs []dirEntry
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		// Skip hidden directories (starting with .)
		if strings.HasPrefix(name, ".") {
			continue
		}
		dirs = append(dirs, dirEntry{
			Name: name,
			Path: filepath.Join(path, name),
		})
	}

	// Sort directories alphabetically
	sort.Slice(dirs, func(i, j int) bool {
		return dirs[i].Name < dirs[j].Name
	})

	// Determine parent directory
	parent := filepath.Dir(path)
	if parent == path {
		// We're at the root, no parent
		parent = ""
	}

	writeJSON(w, 200, directoryBrowserResponse{
		CurrentPath: path,
		Parent:      parent,
		Directories: dirs,
	})
}
