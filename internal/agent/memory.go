package agent

import (
	"os"
	"path/filepath"
	"strings"
)

func (r Runner) projectMemory() string {
	paths := []string{
		filepath.Join(r.Tools.WorkspaceRoot, ".ephemera", "instructions.md"),
		filepath.Join(r.Tools.WorkspaceRoot, ".ephemera", "memory.json"),
		filepath.Join(r.Tools.WorkspaceRoot, "CLAUDE.md"),
		filepath.Join(r.Tools.WorkspaceRoot, "AGENTS.md"),
	}
	var b strings.Builder
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil || len(data) == 0 {
			continue
		}
		rel, _ := filepath.Rel(r.Tools.WorkspaceRoot, path)
		b.WriteString("## ")
		b.WriteString(filepath.ToSlash(rel))
		b.WriteString("\n")
		b.WriteString(strings.TrimSpace(string(data)))
		b.WriteString("\n\n")
	}
	return compact(b.String(), 6000)
}
