package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// PluginManifest describes a cross-platform subprocess extension. The command
// exchanges one JSON-RPC object per line over stdin/stdout.
type PluginManifest struct {
	SchemaVersion string               `json:"schema_version"`
	Name          string               `json:"name"`
	Version       string               `json:"version"`
	Command       string               `json:"command"`
	Args          []string             `json:"args,omitempty"`
	Env           map[string]string    `json:"env,omitempty"`
	Cwd           string               `json:"cwd,omitempty"`
	Tools         []PluginToolManifest `json:"tools"`
	manifestPath  string
}

// PluginToolManifest is one provider-visible and executable plugin tool.
type PluginToolManifest struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Risk        Risk       `json:"risk"`
	Parameters  ToolSchema `json:"parameters"`
	Version     string     `json:"version,omitempty"`
}

func ReadPluginManifest(path string) (PluginManifest, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return PluginManifest{}, fmt.Errorf("plugin manifest path is required")
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return PluginManifest{}, fmt.Errorf("resolve plugin manifest %s: %w", path, err)
	}
	path = filepath.Clean(absPath)
	data, err := os.ReadFile(path)
	if err != nil {
		return PluginManifest{}, err
	}
	var manifest PluginManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return PluginManifest{}, fmt.Errorf("decode plugin manifest %s: %w", path, err)
	}
	manifest.manifestPath = path
	if err := validatePluginManifest(manifest); err != nil {
		return PluginManifest{}, fmt.Errorf("plugin manifest %s: %w", path, err)
	}
	return manifest, nil
}

func validatePluginManifest(manifest PluginManifest) error {
	if manifest.SchemaVersion != PluginProtocolVersion {
		return fmt.Errorf("schema_version must be %q", PluginProtocolVersion)
	}
	if strings.TrimSpace(manifest.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if strings.TrimSpace(manifest.Command) == "" {
		return fmt.Errorf("command is required")
	}
	if len(manifest.Tools) == 0 {
		return fmt.Errorf("at least one tool is required")
	}
	seen := map[string]bool{}
	for _, tool := range manifest.Tools {
		if strings.TrimSpace(tool.Name) == "" || strings.TrimSpace(tool.Description) == "" {
			return fmt.Errorf("every tool requires name and description")
		}
		if seen[tool.Name] {
			return fmt.Errorf("duplicate tool %q", tool.Name)
		}
		seen[tool.Name] = true
		if tool.Risk != RiskRead && tool.Risk != RiskWrite && tool.Risk != RiskShell {
			return fmt.Errorf("tool %q has unsupported risk %q", tool.Name, tool.Risk)
		}
		if tool.Parameters.Type != "" && tool.Parameters.Type != "object" {
			return fmt.Errorf("tool %q parameters must be an object schema", tool.Name)
		}
	}
	return nil
}

func DiscoverPluginManifests(workspace string, directories, explicit []string) ([]string, []error) {
	seen := map[string]bool{}
	var paths []string
	var errs []error
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(workspace, path)
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			errs = append(errs, err)
			return
		}
		if !seen[abs] {
			seen[abs] = true
			paths = append(paths, abs)
		}
	}
	for _, path := range explicit {
		add(path)
	}
	searchDirs := append([]string(nil), directories...)
	searchDirs = append(searchDirs, filepath.Join(workspace, ".ephemera", "plugins"))
	if userConfig, err := os.UserConfigDir(); err == nil {
		searchDirs = append(searchDirs, filepath.Join(userConfig, "ephemera", "plugins"))
	}
	for _, directory := range searchDirs {
		directory = strings.TrimSpace(directory)
		if directory == "" {
			continue
		}
		if !filepath.IsAbs(directory) {
			directory = filepath.Join(workspace, directory)
		}
		entries, err := os.ReadDir(directory)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("discover plugins in %s: %w", directory, err))
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
				continue
			}
			add(filepath.Join(directory, entry.Name()))
		}
	}
	sort.Strings(paths)
	return paths, errs
}

var startupPluginPaths = struct {
	sync.Mutex
	paths []string
}{}

// LoadPlugin validates and queues a subprocess manifest for every registry
// created later in the process. It replaces platform-specific Go .so loading.
func LoadPlugin(path string) error {
	manifest, err := ReadPluginManifest(path)
	if err != nil {
		return err
	}
	startupPluginPaths.Lock()
	startupPluginPaths.paths = append(startupPluginPaths.paths, manifest.manifestPath)
	startupPluginPaths.Unlock()
	return nil
}

func queuedPluginManifests() []string {
	startupPluginPaths.Lock()
	defer startupPluginPaths.Unlock()
	return append([]string(nil), startupPluginPaths.paths...)
}
