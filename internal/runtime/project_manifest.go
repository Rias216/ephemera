package runtime

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ephemera-ai/ephemera/internal/util"
)

const ProjectManifestVersion = 1

var ErrManifestNotFound = errors.New("ephemera project manifest not found")

// ProjectManifest is the deterministic execution contract for a workspace.
// It is loaded from .ephemera/project.json when present, otherwise Ephemera
// discovers a conservative in-memory default without mutating the project.
type ProjectManifest struct {
	Version          int               `json:"version"`
	Bootstrap        []string          `json:"bootstrap,omitempty"`
	Build            []string          `json:"build,omitempty"`
	Tests            []string          `json:"tests,omitempty"`
	Lint             []string          `json:"lint,omitempty"`
	Services         []ServiceSpec     `json:"services,omitempty"`
	ProtectedPaths   []string          `json:"protected_paths,omitempty"`
	AcceptanceChecks []AcceptanceCheck `json:"acceptance_checks,omitempty"`
	Metadata         map[string]string `json:"metadata,omitempty"`
}

type ServiceSpec struct {
	Name        string `json:"name"`
	Command     string `json:"command"`
	Healthcheck string `json:"healthcheck,omitempty"`
}

type AcceptanceCheck struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Command     string `json:"command,omitempty"`
	Required    bool   `json:"required"`
}

func ManifestPath(root string) string {
	return filepath.Join(root, ".ephemera", "project.json")
}

func LoadProjectManifest(root string) (ProjectManifest, error) {
	data, err := os.ReadFile(ManifestPath(root))
	if errors.Is(err, os.ErrNotExist) {
		return ProjectManifest{}, ErrManifestNotFound
	}
	if err != nil {
		return ProjectManifest{}, err
	}
	var manifest ProjectManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return ProjectManifest{}, fmt.Errorf("parse project manifest: %w", err)
	}
	manifest = manifest.normalized()
	if err := manifest.Validate(); err != nil {
		return ProjectManifest{}, err
	}
	return manifest, nil
}

// LoadOrDiscoverProjectManifest prefers the explicit project contract and
// falls back to deterministic marker-based discovery.
func LoadOrDiscoverProjectManifest(root, configuredTest string) (ProjectManifest, string, error) {
	manifest, err := LoadProjectManifest(root)
	if err == nil {
		return manifest, "file", nil
	}
	if !errors.Is(err, ErrManifestNotFound) {
		return ProjectManifest{}, "", err
	}
	return DiscoverProjectManifest(root, configuredTest), "discovered", nil
}

func DiscoverProjectManifest(root, configuredTest string) ProjectManifest {
	testsEnabled := strings.TrimSpace(configuredTest) != ""
	manifest := ProjectManifest{
		Version:        ProjectManifestVersion,
		ProtectedPaths: []string{".git", ".env", ".env.*", ".ephemera/secrets"},
		Metadata:       map[string]string{"source": "discovered"},
	}
	addTest := func(command string) {
		command = strings.TrimSpace(command)
		if command != "" && !util.Contains(manifest.Tests, command) {
			manifest.Tests = append(manifest.Tests, command)
		}
	}
	if fileExists(filepath.Join(root, "go.mod")) {
		manifest.Bootstrap = append(manifest.Bootstrap, "go mod download")
		manifest.Build = append(manifest.Build, "go build ./...")
		if testsEnabled {
			addTest("go test ./...")
		}
		manifest.Lint = append(manifest.Lint, "go vet ./...")
		manifest.Metadata["ecosystem"] = "go"
	}
	if fileExists(filepath.Join(root, "package.json")) {
		manager := "npm"
		switch {
		case fileExists(filepath.Join(root, "pnpm-lock.yaml")):
			manager = "pnpm"
		case fileExists(filepath.Join(root, "yarn.lock")):
			manager = "yarn"
		}
		manifest.Bootstrap = append(manifest.Bootstrap, manager+" install")
		if testsEnabled {
			addTest(manager + " test")
		}
		manifest.Metadata["ecosystem"] = firstNonEmpty(manifest.Metadata["ecosystem"], "node")
	}
	if fileExists(filepath.Join(root, "Cargo.toml")) {
		manifest.Build = append(manifest.Build, "cargo build")
		if testsEnabled {
			addTest("cargo test")
		}
		manifest.Lint = append(manifest.Lint, "cargo clippy --all-targets --all-features")
		manifest.Metadata["ecosystem"] = firstNonEmpty(manifest.Metadata["ecosystem"], "rust")
	}
	if fileExists(filepath.Join(root, "pyproject.toml")) || fileExists(filepath.Join(root, "pytest.ini")) {
		if testsEnabled {
			addTest("pytest")
		}
		manifest.Metadata["ecosystem"] = firstNonEmpty(manifest.Metadata["ecosystem"], "python")
	}
	if strings.TrimSpace(configuredTest) != "" && commandApplicable(root, configuredTest) {
		manifest.Tests = nil
		addTest(configuredTest)
	}
	for index, command := range manifest.Tests {
		manifest.AcceptanceChecks = append(manifest.AcceptanceChecks, AcceptanceCheck{
			ID:          fmt.Sprintf("tests-%d", index+1),
			Description: "Verification command passes: " + command,
			Command:     command,
			Required:    true,
		})
	}
	return manifest.normalized()
}

func (m ProjectManifest) Validate() error {
	if m.Version != 0 && m.Version != ProjectManifestVersion {
		return fmt.Errorf("unsupported project manifest version %d", m.Version)
	}
	seen := map[string]bool{}
	for _, check := range m.AcceptanceChecks {
		id := strings.TrimSpace(check.ID)
		if id == "" {
			return errors.New("project manifest acceptance check id is required")
		}
		if seen[id] {
			return fmt.Errorf("duplicate acceptance check id %q", id)
		}
		seen[id] = true
		if strings.TrimSpace(check.Description) == "" {
			return fmt.Errorf("acceptance check %q description is required", id)
		}
	}
	for _, service := range m.Services {
		if strings.TrimSpace(service.Name) == "" || strings.TrimSpace(service.Command) == "" {
			return errors.New("project manifest services require name and command")
		}
	}
	return nil
}

func (m ProjectManifest) normalized() ProjectManifest {
	if m.Version == 0 {
		m.Version = ProjectManifestVersion
	}
	m.Bootstrap = util.DedupStrings(m.Bootstrap)
	m.Build = util.DedupStrings(m.Build)
	m.Tests = util.DedupStrings(m.Tests)
	m.Lint = util.DedupStrings(m.Lint)
	m.ProtectedPaths = util.DedupStrings(m.ProtectedPaths)
	if len(m.ProtectedPaths) == 0 {
		m.ProtectedPaths = []string{".git", ".env", ".env.*", ".ephemera/secrets"}
	}
	if m.Metadata == nil {
		m.Metadata = map[string]string{}
	}
	usedIDs := map[string]bool{}
	for _, check := range m.AcceptanceChecks {
		usedIDs[strings.TrimSpace(check.ID)] = true
	}
	for index, command := range m.Tests {
		found := false
		for _, check := range m.AcceptanceChecks {
			if strings.TrimSpace(check.Command) == command {
				found = true
				break
			}
		}
		if !found {
			id := fmt.Sprintf("tests-%d", index+1)
			for suffix := 2; usedIDs[id]; suffix++ {
				id = fmt.Sprintf("tests-%d-%d", index+1, suffix)
			}
			usedIDs[id] = true
			m.AcceptanceChecks = append(m.AcceptanceChecks, AcceptanceCheck{
				ID:          id,
				Description: "Verification command passes: " + command,
				Command:     command,
				Required:    true,
			})
		}
	}
	return m
}

func (m ProjectManifest) PrimaryTestCommand() string {
	if len(m.Tests) == 0 {
		return ""
	}
	return strings.TrimSpace(m.Tests[0])
}

// WriteProjectManifest writes an explicit project contract atomically.
func WriteProjectManifest(root string, manifest ProjectManifest) error {
	manifest = manifest.normalized()
	if err := manifest.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	path := ManifestPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func (m ProjectManifest) Summary() string {
	var lines []string
	if ecosystem := strings.TrimSpace(m.Metadata["ecosystem"]); ecosystem != "" {
		lines = append(lines, "Ecosystem: "+ecosystem)
	}
	appendCommands := func(label string, commands []string) {
		if len(commands) > 0 {
			lines = append(lines, label+": "+strings.Join(commands, " | "))
		}
	}
	appendCommands("Bootstrap", m.Bootstrap)
	appendCommands("Build", m.Build)
	appendCommands("Tests", m.Tests)
	appendCommands("Lint", m.Lint)
	if len(m.ProtectedPaths) > 0 {
		lines = append(lines, "Protected paths: "+strings.Join(m.ProtectedPaths, ", "))
	}
	return strings.Join(lines, "\n")
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func commandApplicable(root, command string) bool {
	command = strings.ToLower(strings.TrimSpace(command))
	markers := []struct {
		prefix string
		file   string
	}{
		{"go test", "go.mod"},
		{"npm ", "package.json"},
		{"pnpm ", "package.json"},
		{"yarn ", "package.json"},
		{"cargo ", "Cargo.toml"},
		{"pytest", "pyproject.toml"},
	}
	for _, marker := range markers {
		if strings.HasPrefix(command, marker.prefix) {
			return fileExists(filepath.Join(root, marker.file))
		}
	}
	return command != ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
