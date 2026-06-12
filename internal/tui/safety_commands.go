package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ephemera-ai/ephemera/internal/agent"
	"github.com/ephemera-ai/ephemera/internal/config"
	"github.com/ephemera-ai/ephemera/internal/history"
)

func parseSandboxMode(value string) (config.SandboxMode, bool) {
	switch config.SandboxMode(strings.ToLower(strings.TrimSpace(value))) {
	case config.SandboxNone:
		return config.SandboxNone, true
	case config.SandboxSnapshot:
		return config.SandboxSnapshot, true
	case config.SandboxDocker:
		return config.SandboxDocker, true
	default:
		return "", false
	}
}

func toggleSetting(current bool, value string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "on", "true", "enable", "enabled":
		return true, true
	case "off", "false", "disable", "disabled":
		return false, true
	case "toggle":
		return !current, true
	default:
		return current, false
	}
}

func (m *Model) latestSnapshotPath() string {
	if m.pendingApproval != nil && snapshotAvailable(m.pendingApproval.SnapshotPath) {
		return m.pendingApproval.SnapshotPath
	}
	for i := len(m.session.Events) - 1; i >= 0; i-- {
		metadata := m.session.Events[i].Metadata
		if metadata == nil {
			continue
		}
		path, _ := metadata["snapshot_path"].(string)
		if snapshotAvailable(path) {
			return path
		}
	}
	return ""
}

func snapshotAvailable(directory string) bool {
	if strings.TrimSpace(directory) == "" {
		return false
	}
	info, err := os.Stat(filepath.Join(directory, "manifest.json"))
	return err == nil && !info.IsDir()
}

func (m *Model) rollbackLatestSnapshot() {
	if m.busy {
		m.status = "Stop the active run before rolling back."
		return
	}
	path := m.latestSnapshotPath()
	if path == "" {
		m.notice = m.safetyNotice()
		m.status = "No retained workspace snapshot is available."
		return
	}
	report, err := agent.RollbackWorkspaceSnapshot(path)
	if err != nil {
		m.status = "Rollback failed: " + err.Error()
		return
	}
	if m.pendingApproval != nil && filepath.Clean(m.pendingApproval.SnapshotPath) == filepath.Clean(path) {
		m.pendingApproval = nil
	}
	m.session.Agent.Status = "rolled_back"
	m.session.Agent.Phase = "recovery"
	m.session.Agent.Verified = false
	m.session.Agent.Completed = true
	m.session.Agent.UpdatedAt = time.Now()
	m.session.AppendEvent(history.Event{
		Type:    "recovery",
		Title:   "Workspace rollback",
		Content: report,
		Status:  "done",
		Metadata: map[string]any{
			"snapshot_path": path,
			"rollback":      true,
		},
	})
	_ = m.saveSession()
	m.notice = "### Workspace restored\n\n" + report
	m.status = "Workspace rollback complete."
}

func (m *Model) rebuildSemanticIndex() {
	m.cfg.AgentSemanticIndex = true
	path := filepath.Join(m.workspaceRoot(), ".ephemera", "codebase-index.json")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		m.status = "Index rebuild failed: " + err.Error()
		return
	}
	_ = config.Save(m.cfg)
	m.status = "Semantic codebase index cleared; it will rebuild on the next agent run."
}

func (m Model) safetyNotice() string {
	snapshot := "none retained"
	if path := m.latestSnapshotPath(); path != "" {
		snapshot = "available at `" + escapeMarkdown(path) + "`"
	}
	return fmt.Sprintf(`### Execution safety

- Sandbox mode: %s
- Dry run: %t
- Automatic rollback on failure: %t
- Snapshot size limit: %d MiB
- Latest rollback point: %s

Snapshot mode captures the workspace before the first write. Docker mode runs supported shell commands without network access and requires a compatible local image. Dry-run mode previews mutations without applying them.`,
		m.cfg.SandboxMode,
		m.cfg.AgentDryRun,
		m.cfg.AgentAutoRollback,
		m.cfg.AgentSnapshotMaxMB,
		snapshot,
	)
}

func (m Model) intelligenceNotice() string {
	path := filepath.Join(m.workspaceRoot(), ".ephemera", "codebase-index.json")
	indexState := "not built"
	if info, err := os.Stat(path); err == nil && !info.IsDir() {
		indexState = fmt.Sprintf("built · %s · updated %s", formatByteCount(info.Size()), info.ModTime().Format("2006-01-02 15:04"))
	}
	return fmt.Sprintf(`### Agent intelligence

- Semantic codebase index: %t
- Index state: %s
- Context recall messages: %d
- Context summary budget: %s tokens
- TDD mode: %t
- Episodic learning: %t

The index is refreshed lazily when source files change. Learned task patterns are stored in project-local memory and only written after successful runs.`,
		m.cfg.AgentSemanticIndex,
		indexState,
		m.cfg.AgentContextRecall,
		formatTokenCount(m.cfg.AgentContextSummaryTok),
		m.cfg.AgentTDDMode,
		m.cfg.AgentLearnMemory,
	)
}
