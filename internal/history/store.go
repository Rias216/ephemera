// Package history persists named chat sessions as portable JSON files.
package history

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/ephemera-ai/ephemera/internal/config"
	"github.com/ephemera-ai/ephemera/internal/reasoning"
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
	dir string
}

// NewStore creates the sessions directory when necessary.
func NewStore() (*Store, error) {
	dir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	dir = filepath.Join(dir, "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

// Save atomically persists a session.
func (s *Store) Save(session Session) error {
	session.Name = Sanitize(session.Name)
	session.UpdatedAt = time.Now()
	if session.CreatedAt.IsZero() {
		session.CreatedAt = session.UpdatedAt
	}

	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	path := s.path(session.Name)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Load opens a named session.
func (s *Store) Load(name string) (Session, error) {
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

// List returns session names, newest first.
func (s *Store) List() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	type item struct {
		name string
		mod  time.Time
	}
	items := make([]item, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		items = append(items, item{
			name: strings.TrimSuffix(entry.Name(), ".json"),
			mod:  info.ModTime(),
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].mod.After(items[j].mod) })

	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.name)
	}
	return names, nil
}

func (s *Store) path(name string) string {
	return filepath.Join(s.dir, Sanitize(name)+".json")
}
