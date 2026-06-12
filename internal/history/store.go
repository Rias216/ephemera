// Package history persists named chat sessions.
package history

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ephemera-ai/ephemera/internal/config"
	"github.com/ephemera-ai/ephemera/internal/debuglog"
	"github.com/ephemera-ai/ephemera/internal/reasoning"
	_ "modernc.org/sqlite"
)

var unsafeName = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// Message is the provider-neutral persisted conversation unit.
type Message struct {
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

// Event is a structured agent timeline item. Chat messages remain the compact
// compatibility transcript; events preserve tool use, approvals, and plans.
type Event struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	Title     string         `json:"title,omitempty"`
	Content   string         `json:"content,omitempty"`
	Tool      string         `json:"tool,omitempty"`
	Status    string         `json:"status,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

const (
	EventDecision         = "decision"
	EventReasoningTrace   = "reasoning_trace"
	EventReasoningSummary = "reasoning_summary"
	EventPlanUpdate       = "plan_update"
	EventToolCall         = "tool_call"
	EventToolResult       = "tool_result"
	EventVerification     = "verification"
	EventApprovalRequest  = "approval_request"
	EventFinal            = "final"
)

// AgentTrace is the public, structured version of the agent's working state.
// It is a concise rationale surface, not private chain-of-thought.
type AgentTrace struct {
	Goal          string   `json:"goal,omitempty"`
	CurrentState  string   `json:"current_state,omitempty"`
	Assumptions   []string `json:"assumptions,omitempty"`
	Approach      []string `json:"approach,omitempty"`
	Evidence      []string `json:"evidence,omitempty"`
	Risks         []string `json:"risks,omitempty"`
	ToolRationale string   `json:"tool_rationale,omitempty"`
	Verification  string   `json:"verification,omitempty"`
	NextStep      string   `json:"next_step,omitempty"`
}

// Empty reports whether the trace contains any user-visible content.
func (t AgentTrace) Empty() bool {
	return strings.TrimSpace(t.Goal) == "" &&
		strings.TrimSpace(t.CurrentState) == "" &&
		len(t.Assumptions) == 0 &&
		len(t.Approach) == 0 &&
		len(t.Evidence) == 0 &&
		len(t.Risks) == 0 &&
		strings.TrimSpace(t.ToolRationale) == "" &&
		strings.TrimSpace(t.Verification) == "" &&
		strings.TrimSpace(t.NextStep) == ""
}

// AgentSnapshot is the persisted, user-visible state of the latest agent run.
// It stores concise decision summaries and verification evidence, never hidden
// chain-of-thought.
type AgentSnapshot struct {
	RunID         string     `json:"run_id,omitempty"`
	Status        string     `json:"status,omitempty"`
	Phase         string     `json:"phase,omitempty"`
	Iteration     int        `json:"iteration,omitempty"`
	Goal          string     `json:"goal,omitempty"`
	Summary       string     `json:"summary,omitempty"`
	Reasoning     string     `json:"reasoning,omitempty"`
	Trace         AgentTrace `json:"trace,omitempty"`
	Plan          string     `json:"plan,omitempty"`
	Verification  string     `json:"verification,omitempty"`
	LastTool      string     `json:"last_tool,omitempty"`
	ContextTokens int        `json:"context_tokens,omitempty"`
	OutputTokens  int        `json:"output_tokens,omitempty"`
	Verified      bool       `json:"verified,omitempty"`
	Completed     bool       `json:"completed,omitempty"`
	UpdatedAt     time.Time  `json:"updated_at,omitempty"`
}

// Session is a named conversation and the settings that produced it.
type Session struct {
	Name      string         `json:"name"`
	Provider  string         `json:"provider"`
	Model     string         `json:"model"`
	Mode      reasoning.Mode `json:"mode"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	Messages  []Message      `json:"messages"`
	Events    []Event        `json:"events,omitempty"`
	Agent     AgentSnapshot  `json:"agent,omitempty"`
}

// SearchResult is a compact session search hit.
type SearchResult struct {
	Name      string
	Provider  string
	Model     string
	UpdatedAt time.Time
	Match     string
}

// New creates an empty session.
func New(name, provider, model string, mode reasoning.Mode) Session {
	now := time.Now()
	if strings.TrimSpace(name) == "" {
		name = "session-" + now.Format("20060102-150405")
	}
	return Session{
		Name:      Sanitize(name),
		Provider:  provider,
		Model:     model,
		Mode:      mode,
		CreatedAt: now,
		UpdatedAt: now,
		Messages:  make([]Message, 0, 16),
		Events:    make([]Event, 0, 32),
	}
}

// Append adds a user or assistant message.
func (s *Session) Append(role, content string) {
	s.Messages = append(s.Messages, Message{
		Role:      role,
		Content:   content,
		CreatedAt: time.Now(),
	})
	s.UpdatedAt = time.Now()
}

// AppendEvent adds a structured agent event.
func (s *Session) AppendEvent(event Event) {
	if event.ID == "" {
		event.ID = fmt.Sprintf("evt-%d", time.Now().UnixNano())
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now()
	}
	s.Events = append(s.Events, event)
	s.UpdatedAt = time.Now()
}

// Sanitize converts a session label to a safe filename stem.
func Sanitize(name string) string {
	name = strings.TrimSpace(name)
	name = unsafeName.ReplaceAllString(name, "-")
	name = strings.Trim(name, ".-")
	if name == "" {
		name = "session-" + time.Now().Format("20060102-150405")
	}
	if len(name) > 80 {
		name = name[:80]
	}
	return name
}

// Store reads and writes sessions.
type Store struct {
	dir    string
	dbPath string

	mu sync.Mutex
	db *sql.DB

	auditMu    sync.Mutex
	saveAudits map[string]saveAuditState
}

type saveAuditState struct {
	Messages int
	Status   string
	RunID    string
	LoggedAt time.Time
}

// NewStore creates the sessions directory when necessary.
func NewStore() (*Store, error) {
	dir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	sessionDir := debuglog.SessionRoot()
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		return nil, err
	}
	return &Store{
		dir:        sessionDir,
		dbPath:     filepath.Join(dir, "history.sqlite"),
		saveAudits: make(map[string]saveAuditState),
	}, nil
}

// Save persists the crash-recovery snapshot first. SQLite is maintained as a
// searchable index, but an index outage must never prevent session recovery.
func (s *Store) Save(session Session) error {
	session.Name = Sanitize(session.Name)
	session.UpdatedAt = time.Now()
	if session.CreatedAt.IsZero() {
		session.CreatedAt = session.UpdatedAt
	}

	if err := s.writeBundleSnapshot(session); err != nil {
		return err
	}
	if filepath.Clean(debuglog.SessionDirectory(session.Name)) == filepath.Clean(s.bundleDir(session.Name)) && s.shouldAuditSave(session) {
		_ = debuglog.WriteSession(session.Name, "info", "history", "session checkpoint", "crash-recovery snapshot persisted", map[string]any{
			"messages": len(session.Messages),
			"events":   len(session.Events),
			"provider": session.Provider,
			"model":    session.Model,
			"run_id":   session.Agent.RunID,
			"status":   session.Agent.Status,
		})
	}

	if err := s.saveSQL(session); err != nil {
		// The bundle is the recovery source of truth. Keep the session usable and
		// record that only the optional SQLite search index failed.
		_ = debuglog.WriteSession(session.Name, "warning", "history", "session index update failed", err.Error(), map[string]any{
			"snapshot": s.bundleSnapshotPath(session.Name),
			"database": s.dbPath,
		})
		return nil
	}
	return nil
}

func (s *Store) shouldAuditSave(session Session) bool {
	now := time.Now()
	current := saveAuditState{
		Messages: len(session.Messages),
		Status:   strings.TrimSpace(session.Agent.Status),
		RunID:    strings.TrimSpace(session.Agent.RunID),
		LoggedAt: now,
	}
	s.auditMu.Lock()
	defer s.auditMu.Unlock()
	if s.saveAudits == nil {
		s.saveAudits = make(map[string]saveAuditState)
	}
	previous, ok := s.saveAudits[session.Name]
	logCheckpoint := !ok || previous.Messages != current.Messages || previous.Status != current.Status || previous.RunID != current.RunID || now.Sub(previous.LoggedAt) >= 5*time.Second
	if logCheckpoint {
		s.saveAudits[session.Name] = current
	}
	return logCheckpoint
}

func (s *Store) saveSQL(session Session) error {
	db, err := s.ensureDB()
	if err != nil {
		return err
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	agentJSON, err := json.Marshal(session.Agent)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`
		INSERT INTO sessions (name, provider, model, mode, created_at, updated_at, agent_json)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			provider = excluded.provider,
			model = excluded.model,
			mode = excluded.mode,
			created_at = excluded.created_at,
			updated_at = excluded.updated_at,
			agent_json = excluded.agent_json
	`, session.Name, session.Provider, session.Model, string(session.Mode), encodeTime(session.CreatedAt), encodeTime(session.UpdatedAt), string(agentJSON)); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM messages WHERE session_name = ?`, session.Name); err != nil {
		return err
	}
	for i, message := range session.Messages {
		if _, err := tx.Exec(`
			INSERT INTO messages (session_name, idx, role, content, created_at)
			VALUES (?, ?, ?, ?, ?)
		`, session.Name, i, message.Role, message.Content, encodeTime(message.CreatedAt)); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM events WHERE session_name = ?`, session.Name); err != nil {
		return err
	}
	for i, event := range session.Events {
		metadataJSON, err := json.Marshal(event.Metadata)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`
			INSERT INTO events (session_name, idx, id, type, title, content, tool, status, metadata_json, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, session.Name, i, event.ID, event.Type, event.Title, event.Content, event.Tool, event.Status, string(metadataJSON), encodeTime(event.CreatedAt)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Load opens a named session.
func (s *Store) Load(name string) (Session, error) {
	session, sqlErr := s.loadSQL(name)
	if sqlErr == nil {
		return session, nil
	}
	if bundle, bundleErr := s.loadBundleSnapshot(name); bundleErr == nil {
		return bundle, nil
	}
	if !errors.Is(sqlErr, sql.ErrNoRows) {
		return Session{}, sqlErr
	}
	return s.loadJSON(name)
}

// List returns session names, newest first.
func (s *Store) List() ([]string, error) {
	type item struct {
		name string
		mod  time.Time
	}
	items := make([]item, 0, 16)
	seen := map[string]struct{}{}
	var sqlErr error

	if db, err := s.ensureDB(); err != nil {
		sqlErr = err
	} else if rows, err := db.Query(`SELECT name, updated_at FROM sessions ORDER BY updated_at DESC, name ASC`); err != nil {
		sqlErr = err
	} else {
		for rows.Next() {
			var name, updated string
			if err := rows.Scan(&name, &updated); err != nil {
				sqlErr = err
				break
			}
			items = append(items, item{name: name, mod: decodeTime(updated)})
			seen[name] = struct{}{}
		}
		if err := rows.Err(); err != nil && sqlErr == nil {
			sqlErr = err
		}
		_ = rows.Close()
	}

	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		name := ""
		var info os.FileInfo
		if entry.IsDir() {
			snapshot := filepath.Join(s.dir, entry.Name(), "session.json")
			snapshotInfo, statErr := os.Stat(snapshot)
			if statErr != nil {
				continue
			}
			name = entry.Name()
			info = snapshotInfo
		} else if filepath.Ext(entry.Name()) == ".json" {
			name = strings.TrimSuffix(entry.Name(), ".json")
			entryInfo, infoErr := entry.Info()
			if infoErr != nil {
				continue
			}
			info = entryInfo
		} else {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		items = append(items, item{name: name, mod: info.ModTime()})
	}
	if len(items) == 0 && sqlErr != nil {
		return nil, sqlErr
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].mod.Equal(items[j].mod) {
			return items[i].name < items[j].name
		}
		return items[i].mod.After(items[j].mod)
	})

	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.name)
	}
	return names, nil
}

// Search finds sessions whose name, transcript, timeline, or agent snapshot
// contains query. SQLite-backed sessions are searched in SQL; legacy JSON files
// remain searchable for compatibility.
func (s *Store) Search(query string, limit int) ([]SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		names, err := s.List()
		if err != nil {
			return nil, err
		}
		results := make([]SearchResult, 0, len(names))
		for _, name := range names {
			results = append(results, SearchResult{Name: name})
			if limit > 0 && len(results) >= limit {
				break
			}
		}
		return results, nil
	}
	if limit <= 0 {
		limit = 20
	}

	results := make([]SearchResult, 0, limit)
	seen := map[string]struct{}{}
	var sqlErr error
	if db, err := s.ensureDB(); err != nil {
		sqlErr = err
	} else {
		pattern := "%" + strings.ToLower(query) + "%"
		rows, err := db.Query(`
			SELECT name
			FROM sessions
			WHERE lower(name) LIKE ?
				OR lower(provider) LIKE ?
				OR lower(model) LIKE ?
				OR lower(agent_json) LIKE ?
				OR EXISTS (
					SELECT 1 FROM messages
					WHERE messages.session_name = sessions.name
						AND (lower(role) LIKE ? OR lower(content) LIKE ?)
				)
				OR EXISTS (
					SELECT 1 FROM events
					WHERE events.session_name = sessions.name
						AND (lower(type) LIKE ? OR lower(title) LIKE ? OR lower(content) LIKE ? OR lower(tool) LIKE ? OR lower(status) LIKE ? OR lower(metadata_json) LIKE ?)
				)
			ORDER BY updated_at DESC, name ASC
			LIMIT ?
		`, pattern, pattern, pattern, pattern, pattern, pattern, pattern, pattern, pattern, pattern, pattern, pattern, limit)
		if err != nil {
			sqlErr = err
		} else {
			for rows.Next() {
				var name string
				if err := rows.Scan(&name); err != nil {
					sqlErr = err
					break
				}
				session, err := s.loadSQL(name)
				if err != nil {
					sqlErr = err
					break
				}
				results = append(results, sessionSearchResult(session, query))
				seen[name] = struct{}{}
			}
			if err := rows.Err(); err != nil && sqlErr == nil {
				sqlErr = err
			}
			_ = rows.Close()
		}
	}

	bundleResults, err := s.searchLegacyJSON(query, seen)
	if err != nil {
		return nil, err
	}
	results = append(results, bundleResults...)
	if len(results) == 0 && sqlErr != nil {
		return nil, sqlErr
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].UpdatedAt.Equal(results[j].UpdatedAt) {
			return results[i].Name < results[j].Name
		}
		return results[i].UpdatedAt.After(results[j].UpdatedAt)
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

// Close releases the underlying database handle.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

func (s *Store) loadSQL(name string) (Session, error) {
	db, err := s.ensureDB()
	if err != nil {
		return Session{}, err
	}

	name = Sanitize(name)
	var session Session
	var mode, created, updated, agentJSON string
	err = db.QueryRow(`
		SELECT name, provider, model, mode, created_at, updated_at, agent_json
		FROM sessions
		WHERE name = ?
	`, name).Scan(&session.Name, &session.Provider, &session.Model, &mode, &created, &updated, &agentJSON)
	if err != nil {
		return Session{}, err
	}
	session.Mode = reasoning.Mode(mode)
	session.CreatedAt = decodeTime(created)
	session.UpdatedAt = decodeTime(updated)
	if strings.TrimSpace(agentJSON) != "" {
		if err := json.Unmarshal([]byte(agentJSON), &session.Agent); err != nil {
			return Session{}, err
		}
	}

	rows, err := db.Query(`
		SELECT role, content, created_at
		FROM messages
		WHERE session_name = ?
		ORDER BY idx ASC
	`, name)
	if err != nil {
		return Session{}, err
	}
	for rows.Next() {
		var message Message
		var created string
		if err := rows.Scan(&message.Role, &message.Content, &created); err != nil {
			_ = rows.Close()
			return Session{}, err
		}
		message.CreatedAt = decodeTime(created)
		session.Messages = append(session.Messages, message)
	}
	if err := rows.Close(); err != nil {
		return Session{}, err
	}
	if err := rows.Err(); err != nil {
		return Session{}, err
	}

	rows, err = db.Query(`
		SELECT id, type, title, content, tool, status, metadata_json, created_at
		FROM events
		WHERE session_name = ?
		ORDER BY idx ASC
	`, name)
	if err != nil {
		return Session{}, err
	}
	for rows.Next() {
		var event Event
		var metadataJSON, created string
		if err := rows.Scan(&event.ID, &event.Type, &event.Title, &event.Content, &event.Tool, &event.Status, &metadataJSON, &created); err != nil {
			_ = rows.Close()
			return Session{}, err
		}
		if strings.TrimSpace(metadataJSON) != "" {
			if err := json.Unmarshal([]byte(metadataJSON), &event.Metadata); err != nil {
				_ = rows.Close()
				return Session{}, err
			}
		}
		event.CreatedAt = decodeTime(created)
		session.Events = append(session.Events, event)
	}
	if err := rows.Close(); err != nil {
		return Session{}, err
	}
	if err := rows.Err(); err != nil {
		return Session{}, err
	}

	return session, nil
}

func (s *Store) bundleDir(name string) string {
	return filepath.Join(s.dir, Sanitize(name))
}

func (s *Store) bundleSnapshotPath(name string) string {
	return filepath.Join(s.bundleDir(name), "session.json")
}

func (s *Store) writeBundleSnapshot(session Session) error {
	dir := s.bundleDir(session.Name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := s.bundleSnapshotPath(session.Name)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := replaceSnapshotFile(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	for _, fileName := range []string{"debug.jsonl", "context.jsonl", "tools.jsonl"} {
		file, err := os.OpenFile(filepath.Join(dir, fileName), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_ = file.Chmod(0o600)
		if err := file.Close(); err != nil {
			return err
		}
	}
	// Keep debuglog's independently resolved path in sync for normal config
	// stores and portable installs that override EPHEMERA_SESSION_LOG_DIR.
	if filepath.Clean(debuglog.SessionDirectory(session.Name)) == filepath.Clean(dir) {
		_ = debuglog.EnsureSession(session.Name)
	}
	return nil
}

func replaceSnapshotFile(source, destination string) error {
	if err := os.Rename(source, destination); err == nil {
		return nil
	}
	if err := os.Remove(destination); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(source, destination)
}

func (s *Store) loadBundleSnapshot(name string) (Session, error) {
	data, err := os.ReadFile(s.bundleSnapshotPath(name))
	if err != nil {
		return Session{}, err
	}
	var session Session
	if err := json.Unmarshal(data, &session); err != nil {
		return Session{}, err
	}
	if session.Name == "" {
		session.Name = Sanitize(name)
	}
	return session, nil
}

func (s *Store) loadJSON(name string) (Session, error) {
	path := s.path(name)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Session{}, fmt.Errorf("session %q not found", Sanitize(name))
	}
	if err != nil {
		return Session{}, err
	}

	var session Session
	if err := json.Unmarshal(data, &session); err != nil {
		return Session{}, err
	}
	if session.Name == "" {
		session.Name = Sanitize(name)
	}
	return session, nil
}

func (s *Store) searchLegacyJSON(query string, seen map[string]struct{}) ([]SearchResult, error) {
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var results []SearchResult
	for _, entry := range entries {
		name := ""
		load := s.loadJSON
		if entry.IsDir() {
			name = entry.Name()
			load = s.loadBundleSnapshot
		} else if filepath.Ext(entry.Name()) == ".json" {
			name = strings.TrimSuffix(entry.Name(), ".json")
		} else {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		session, err := load(name)
		if err != nil {
			continue
		}
		result, ok := matchSession(session, query)
		if ok {
			results = append(results, result)
		}
	}
	return results, nil
}

func (s *Store) ensureDB() (*sql.DB, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		return s.db, nil
	}

	if strings.TrimSpace(s.dbPath) == "" {
		s.dbPath = filepath.Join(s.dir, "history.sqlite")
	}
	if err := os.MkdirAll(filepath.Dir(s.dbPath), 0o700); err != nil {
		return nil, err
	}
	if _, err := os.Stat(s.dbPath); errors.Is(err, os.ErrNotExist) {
		file, err := os.OpenFile(s.dbPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, err
		}
		if err := file.Close(); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", s.dbPath)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := initDB(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	s.db = db
	return s.db, nil
}

func (s *Store) path(name string) string {
	return filepath.Join(s.dir, Sanitize(name)+".json")
}

func initDB(db *sql.DB) error {
	statements := []string{
		`PRAGMA foreign_keys = ON`,
		`PRAGMA busy_timeout = 5000`,
		`PRAGMA journal_mode = WAL`,
		`CREATE TABLE IF NOT EXISTS sessions (
			name TEXT PRIMARY KEY,
			provider TEXT NOT NULL,
			model TEXT NOT NULL,
			mode TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			agent_json TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS messages (
			session_name TEXT NOT NULL,
			idx INTEGER NOT NULL,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			created_at TEXT NOT NULL,
			PRIMARY KEY (session_name, idx),
			FOREIGN KEY (session_name) REFERENCES sessions(name) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS events (
			session_name TEXT NOT NULL,
			idx INTEGER NOT NULL,
			id TEXT NOT NULL,
			type TEXT NOT NULL,
			title TEXT NOT NULL,
			content TEXT NOT NULL,
			tool TEXT NOT NULL,
			status TEXT NOT NULL,
			metadata_json TEXT NOT NULL,
			created_at TEXT NOT NULL,
			PRIMARY KEY (session_name, idx),
			FOREIGN KEY (session_name) REFERENCES sessions(name) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS sessions_updated_at_idx ON sessions(updated_at DESC)`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}

func encodeTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func decodeTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err == nil {
		return t
	}
	t, err = time.Parse(time.RFC3339, value)
	if err == nil {
		return t
	}
	return time.Time{}
}

func sessionSearchResult(session Session, query string) SearchResult {
	result, ok := matchSession(session, query)
	if ok {
		return result
	}
	return SearchResult{
		Name:      session.Name,
		Provider:  session.Provider,
		Model:     session.Model,
		UpdatedAt: session.UpdatedAt,
	}
}

func matchSession(session Session, query string) (SearchResult, bool) {
	query = strings.ToLower(strings.TrimSpace(query))
	result := SearchResult{
		Name:      session.Name,
		Provider:  session.Provider,
		Model:     session.Model,
		UpdatedAt: session.UpdatedAt,
	}
	if query == "" {
		return result, true
	}
	for _, item := range []struct {
		label string
		value string
	}{
		{"name", session.Name},
		{"provider", session.Provider},
		{"model", session.Model},
		{"mode", string(session.Mode)},
		{"agent goal", session.Agent.Goal},
		{"agent summary", session.Agent.Summary},
		{"agent verification", session.Agent.Verification},
	} {
		if containsFold(item.value, query) {
			result.Match = item.label + ": " + compactSearchText(item.value)
			return result, true
		}
	}
	for _, message := range session.Messages {
		if containsFold(message.Role, query) || containsFold(message.Content, query) {
			result.Match = message.Role + ": " + compactSearchText(message.Content)
			return result, true
		}
	}
	for _, event := range session.Events {
		for _, item := range []struct {
			label string
			value string
		}{
			{event.Type, event.Title},
			{event.Type, event.Content},
			{"tool", event.Tool},
			{"status", event.Status},
		} {
			if containsFold(item.value, query) {
				result.Match = item.label + ": " + compactSearchText(item.value)
				return result, true
			}
		}
		if event.Metadata != nil {
			data, _ := json.Marshal(event.Metadata)
			if containsFold(string(data), query) {
				result.Match = "metadata: " + compactSearchText(string(data))
				return result, true
			}
		}
	}
	return result, false
}

func containsFold(value, lowerQuery string) bool {
	return strings.Contains(strings.ToLower(value), lowerQuery)
}

func compactSearchText(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= 120 {
		return value
	}
	return value[:117] + "..."
}
