package debuglog

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	sessionDebugFile   = "debug.jsonl"
	sessionContextFile = "context.jsonl"
	contextMaxBytes    = 32 << 20
	contextRotations   = 5
)

var unsafeSessionName = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

type scopeKey struct{}

// Scope identifies the active session and agent run. It is propagated through
// provider and tool contexts so low-level failures are mirrored into the
// correct per-session diagnostic bundle without relying on process-global state.
type Scope struct {
	Session   string
	RunID     string
	Provider  string
	Model     string
	Workspace string
	Iteration int
	Tool      string
}

// WithScope attaches diagnostic identity to ctx. Non-empty fields replace
// existing values while omitted fields inherit from the parent scope.
func WithScope(ctx context.Context, scope Scope) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	parent := ScopeFromContext(ctx)
	if strings.TrimSpace(scope.Session) != "" {
		parent.Session = sanitizeSessionName(scope.Session)
	}
	if strings.TrimSpace(scope.RunID) != "" {
		parent.RunID = strings.TrimSpace(scope.RunID)
	}
	if strings.TrimSpace(scope.Provider) != "" {
		parent.Provider = strings.TrimSpace(scope.Provider)
	}
	if strings.TrimSpace(scope.Model) != "" {
		parent.Model = strings.TrimSpace(scope.Model)
	}
	if strings.TrimSpace(scope.Workspace) != "" {
		parent.Workspace = filepath.Clean(scope.Workspace)
	}
	if scope.Iteration > 0 {
		parent.Iteration = scope.Iteration
	}
	if strings.TrimSpace(scope.Tool) != "" {
		parent.Tool = strings.TrimSpace(scope.Tool)
	}
	return context.WithValue(ctx, scopeKey{}, parent)
}

// ScopeFromContext returns the diagnostic scope attached to ctx.
func ScopeFromContext(ctx context.Context) Scope {
	if ctx == nil {
		return Scope{}
	}
	scope, _ := ctx.Value(scopeKey{}).(Scope)
	return scope
}

func scopeFields(ctx context.Context, fields map[string]any) map[string]any {
	scope := ScopeFromContext(ctx)
	out := make(map[string]any, len(fields)+7)
	if scope.Session != "" {
		out["session"] = scope.Session
	}
	if scope.RunID != "" {
		out["run_id"] = scope.RunID
	}
	if scope.Provider != "" {
		out["provider"] = scope.Provider
	}
	if scope.Model != "" {
		out["model"] = scope.Model
	}
	if scope.Workspace != "" {
		out["workspace"] = scope.Workspace
	}
	if scope.Iteration > 0 {
		out["iteration"] = scope.Iteration
	}
	if scope.Tool != "" {
		out["tool"] = scope.Tool
	}
	for key, value := range fields {
		out[key] = value
	}
	return out
}

// WriteCtx is Write with session/run identity inherited from ctx.
func WriteCtx(ctx context.Context, level, component, event, message string, fields map[string]any) error {
	return Write(level, component, event, message, scopeFields(ctx, fields))
}

func ErrorCtx(ctx context.Context, component, event string, err error, fields map[string]any) {
	if err == nil {
		return
	}
	_ = WriteCtx(ctx, "error", component, event, err.Error(), fields)
}

func FailureCtx(ctx context.Context, component, event, message string, fields map[string]any) {
	_ = WriteCtx(ctx, "error", component, event, message, fields)
}

func WarningCtx(ctx context.Context, component, event, message string, fields map[string]any) {
	_ = WriteCtx(ctx, "warning", component, event, message, fields)
}

// SessionDirectory is the persistent bundle directory for one chat session.
// EPHEMERA_SESSION_LOG_DIR may override the parent directory for portable runs.
func SessionRoot() string {
	parent := strings.TrimSpace(os.Getenv("EPHEMERA_SESSION_LOG_DIR"))
	if parent == "" {
		if root, err := os.UserConfigDir(); err == nil && strings.TrimSpace(root) != "" {
			parent = filepath.Join(root, "ephemera", "sessions")
		} else {
			parent = filepath.Join(os.TempDir(), "ephemera", "sessions")
		}
	}
	return filepath.Clean(parent)
}

func SessionDirectory(session string) string {
	return filepath.Join(SessionRoot(), sanitizeSessionName(session))
}

func SessionDebugPath(session string) string {
	return filepath.Join(SessionDirectory(session), sessionDebugFile)
}

func SessionContextPath(session string) string {
	return filepath.Join(SessionDirectory(session), sessionContextFile)
}

// EnsureSession creates the diagnostic files eagerly so even a session that
// crashes before its first provider call has a discoverable bundle.
func EnsureSession(session string) error {
	dir := SessionDirectory(session)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for _, path := range []string{SessionDebugPath(session), SessionContextPath(session)} {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_ = file.Chmod(0o600)
		if err := file.Close(); err != nil {
			return err
		}
	}
	return nil
}

// ContextRecord is one exact provider boundary snapshot. Payload is sanitized
// and normalized to valid UTF-8 before persistence. It may contain the request,
// available tool schemas, selection statistics, response, or error details.
type ContextRecord struct {
	Time      time.Time      `json:"time"`
	Stage     string         `json:"stage"`
	Session   string         `json:"session"`
	RunID     string         `json:"run_id,omitempty"`
	Provider  string         `json:"provider,omitempty"`
	Model     string         `json:"model,omitempty"`
	Workspace string         `json:"workspace,omitempty"`
	Tool      string         `json:"tool,omitempty"`
	Iteration int            `json:"iteration,omitempty"`
	Attempt   int            `json:"attempt,omitempty"`
	Transport string         `json:"transport,omitempty"`
	Payload   map[string]any `json:"payload,omitempty"`
	SHA256    string         `json:"sha256,omitempty"`
}

// AppendContext writes a redacted JSONL record to the active session bundle.
// The payload hash makes it possible to correlate retries without storing a
// second copy elsewhere.
func AppendContext(ctx context.Context, stage string, attempt int, transport string, payload map[string]any) error {
	scope := ScopeFromContext(ctx)
	if strings.TrimSpace(scope.Session) == "" {
		return nil
	}
	record := ContextRecord{
		Time:      time.Now().UTC(),
		Stage:     fallback(strings.TrimSpace(stage), "context"),
		Session:   sanitizeSessionName(scope.Session),
		RunID:     scope.RunID,
		Provider:  scope.Provider,
		Model:     scope.Model,
		Workspace: scope.Workspace,
		Tool:      scope.Tool,
		Iteration: scope.Iteration,
		Attempt:   attempt,
		Transport: strings.TrimSpace(transport),
		Payload:   sanitizeContextMap(payload, 0),
	}
	hashInput, err := json.Marshal(record.Payload)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(hashInput)
	record.SHA256 = hex.EncodeToString(sum[:])
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	mu.Lock()
	defer mu.Unlock()
	if err := EnsureSession(record.Session); err != nil {
		return err
	}
	path := SessionContextPath(record.Session)
	if err := rotateBounded(path, int64(len(data)), contextMaxBytes, contextRotations); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	_ = file.Chmod(0o600)
	if _, err := file.Write(data); err != nil {
		return err
	}
	return file.Sync()
}

func mirrorSessionEntry(entry Entry, data []byte) {
	session := ""
	if entry.Fields != nil {
		session = strings.TrimSpace(fmt.Sprint(entry.Fields["session"]))
	}
	if session == "" || session == "<nil>" {
		return
	}
	if err := EnsureSession(session); err != nil {
		return
	}
	_ = writeAt(SessionDebugPath(session), data)
}

func rotateBounded(path string, incoming, maxBytes int64, rotations int) error {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Size()+incoming <= maxBytes {
		return nil
	}
	_ = os.Remove(fmt.Sprintf("%s.%d", path, rotations))
	for index := rotations - 1; index >= 1; index-- {
		oldPath := fmt.Sprintf("%s.%d", path, index)
		newPath := fmt.Sprintf("%s.%d", path, index+1)
		if err := os.Rename(oldPath, newPath); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return os.Rename(path, path+".1")
}

func sanitizeSessionName(name string) string {
	name = strings.TrimSpace(name)
	name = unsafeSessionName.ReplaceAllString(name, "-")
	name = strings.Trim(name, ".-")
	if name == "" {
		name = "session-unknown"
	}
	if len(name) > 80 {
		name = name[:80]
	}
	return name
}

func sanitizeContextMap(fields map[string]any, depth int) map[string]any {
	if len(fields) == 0 {
		return nil
	}
	if depth > 10 {
		return map[string]any{"value": "[TRUNCATED]"}
	}
	out := make(map[string]any, len(fields))
	for key, value := range fields {
		if sensitiveKey(key) {
			out[key] = "[REDACTED]"
			continue
		}
		out[key] = sanitizeContextValue(value, depth+1)
	}
	return out
}

func sanitizeContextValue(value any, depth int) any {
	if depth > 10 {
		return "[TRUNCATED]"
	}
	switch typed := value.(type) {
	case nil:
		return nil
	case bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, json.Number:
		return typed
	case string:
		return redact(limitContextString(strings.ToValidUTF8(typed, "�"), 128<<10))
	case error:
		return redact(limitContextString(strings.ToValidUTF8(typed.Error(), "�"), 64<<10))
	case map[string]any:
		return sanitizeContextMap(typed, depth+1)
	case []string:
		limit := min(len(typed), 512)
		out := make([]string, 0, limit)
		for _, item := range typed[:limit] {
			out = append(out, redact(limitContextString(strings.ToValidUTF8(item, "�"), 64<<10)))
		}
		return out
	case []any:
		limit := min(len(typed), 512)
		out := make([]any, 0, limit)
		for _, item := range typed[:limit] {
			out = append(out, sanitizeContextValue(item, depth+1))
		}
		return out
	default:
		data, err := json.Marshal(value)
		if err != nil {
			return redact(limitContextString(strings.ToValidUTF8(fmt.Sprint(value), "�"), 64<<10))
		}
		var generic any
		if json.Unmarshal(data, &generic) == nil {
			return sanitizeContextValue(generic, depth+1)
		}
		return redact(limitContextString(strings.ToValidUTF8(string(data), "�"), 64<<10))
	}
}

func limitContextString(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut] + "…[TRUNCATED]"
}

// WriteSession appends an informational record only to a session's local
// debug.jsonl. It avoids flooding the global failure log with routine saves.
func WriteSession(session, level, component, event, message string, fields map[string]any) error {
	session = sanitizeSessionName(session)
	merged := make(map[string]any, len(fields)+1)
	merged["session"] = session
	for key, value := range fields {
		merged[key] = value
	}
	entry := Entry{
		Time:      time.Now().UTC(),
		Level:     fallback(strings.ToLower(strings.TrimSpace(level)), "info"),
		Component: fallback(strings.TrimSpace(component), "ephemera"),
		Event:     fallback(strings.TrimSpace(event), "session event"),
		Message:   redact(strings.TrimSpace(message)),
		Fields:    sanitizeMap(merged, 0),
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	mu.Lock()
	defer mu.Unlock()
	if err := EnsureSession(session); err != nil {
		return err
	}
	return writeAt(SessionDebugPath(session), data)
}

// RecentSession returns the newest per-session debug records in chronological order.
func RecentSession(session string, limit int) ([]Entry, error) {
	if limit < 1 {
		limit = 20
	}
	mu.Lock()
	defer mu.Unlock()
	file, err := os.Open(SessionDebugPath(session))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	entries := make([]Entry, 0, limit)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 2<<20)
	for scanner.Scan() {
		var entry Entry
		if json.Unmarshal(scanner.Bytes(), &entry) != nil {
			continue
		}
		if len(entries) == limit {
			copy(entries, entries[1:])
			entries[len(entries)-1] = entry
		} else {
			entries = append(entries, entry)
		}
	}
	return entries, scanner.Err()
}

// ClearSession removes the current session's rotated debug/context logs while
// preserving session.json, then recreates empty files for continued capture.
func ClearSession(session string) error {
	mu.Lock()
	defer mu.Unlock()
	var first error
	for _, base := range []string{SessionDebugPath(session), SessionContextPath(session)} {
		for index := 0; index <= contextRotations; index++ {
			candidate := base
			if index > 0 {
				candidate = fmt.Sprintf("%s.%d", base, index)
			}
			if err := os.Remove(candidate); err != nil && !errors.Is(err, os.ErrNotExist) && first == nil {
				first = err
			}
		}
	}
	if first != nil {
		return first
	}
	return EnsureSession(session)
}

var runScopes sync.Map

// RegisterRunScope makes a run's session identity available to helper paths
// that only receive a run ID (for example generic event constructors).
func RegisterRunScope(scope Scope) {
	if strings.TrimSpace(scope.RunID) == "" {
		return
	}
	scope.Session = sanitizeSessionName(scope.Session)
	runScopes.Store(scope.RunID, scope)
}

func UnregisterRunScope(runID string) {
	if strings.TrimSpace(runID) != "" {
		runScopes.Delete(runID)
	}
}

// ContextForRun reconstructs a diagnostic context from a registered run ID.
func ContextForRun(runID string) context.Context {
	if value, ok := runScopes.Load(strings.TrimSpace(runID)); ok {
		if scope, ok := value.(Scope); ok {
			return WithScope(context.Background(), scope)
		}
	}
	return context.Background()
}
