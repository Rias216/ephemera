// Package debuglog provides best-effort, privacy-conscious persistent diagnostics.
// Logging must never crash or block the main application path.
package debuglog

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	fileName     = "debug.log"
	maxLogBytes  = 5 << 20
	rotatedFiles = 3
)

var (
	mu sync.Mutex
	// activePath is set when the configured directory is unavailable and the
	// logger successfully falls back to the system temporary directory.
	activePath atomic.Value

	bearerPattern = regexp.MustCompile(`(?i)(authorization\s*[:=]\s*bearer\s+)[^\s"']+`)
	keyPattern    = regexp.MustCompile(`(?i)((?:api[_-]?key|token|secret|password)\s*[:=]\s*)[^\s,"']+`)
	openAIKey     = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{12,}\b`)
)

// Entry is one JSON-lines diagnostic record.
type Entry struct {
	Time      time.Time      `json:"time"`
	Level     string         `json:"level"`
	Component string         `json:"component"`
	Event     string         `json:"event"`
	Message   string         `json:"message"`
	Fields    map[string]any `json:"fields,omitempty"`
}

// Path returns the active diagnostic log path. EPHEMERA_DEBUG_LOG can override
// the default for portable installations and tests.
func Path() string {
	if explicit := strings.TrimSpace(os.Getenv("EPHEMERA_DEBUG_LOG")); explicit != "" {
		return filepath.Clean(explicit)
	}
	if active, ok := activePath.Load().(string); ok && strings.TrimSpace(active) != "" {
		return active
	}
	return configuredPath()
}

func configuredPath() string {
	if root, err := os.UserConfigDir(); err == nil && strings.TrimSpace(root) != "" {
		return filepath.Join(root, "ephemera", "logs", fileName)
	}
	if root, err := os.UserCacheDir(); err == nil && strings.TrimSpace(root) != "" {
		return filepath.Join(root, "ephemera", "logs", fileName)
	}
	return filepath.Join(os.TempDir(), "ephemera", fileName)
}

// Error records an encountered error. It intentionally returns no error so the
// diagnostic path cannot mask the original failure.
func Error(component, event string, err error, fields map[string]any) {
	if err == nil {
		return
	}
	_ = Write("error", component, event, err.Error(), fields)
}

// Failure records a non-error failure state such as a failed verification or
// an unexpectedly closed stream.
func Failure(component, event, message string, fields map[string]any) {
	_ = Write("error", component, event, message, fields)
}

// Warning records a recoverable failure, retry, or degraded fallback.
func Warning(component, event, message string, fields map[string]any) {
	_ = Write("warning", component, event, message, fields)
}

// Write appends one redacted JSON-lines entry and rotates bounded log files.
func Write(level, component, event, message string, fields map[string]any) error {
	entry := Entry{
		Time:      time.Now().UTC(),
		Level:     fallback(strings.ToLower(strings.TrimSpace(level)), "error"),
		Component: fallback(strings.TrimSpace(component), "ephemera"),
		Event:     fallback(strings.TrimSpace(event), "failure"),
		Message:   redact(strings.TrimSpace(message)),
		Fields:    sanitizeMap(fields, 0),
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	mu.Lock()
	defer mu.Unlock()

	path := configuredPath()
	if explicit := strings.TrimSpace(os.Getenv("EPHEMERA_DEBUG_LOG")); explicit != "" {
		path = filepath.Clean(explicit)
	}
	if err := writeAt(path, data); err == nil {
		activePath.Store(path)
		mirrorSessionEntry(entry, data)
		return nil
	} else {
		primaryErr := err
		fallback := filepath.Join(os.TempDir(), "ephemera", fileName)
		if filepath.Clean(fallback) == filepath.Clean(path) {
			return primaryErr
		}
		if fallbackErr := writeAt(fallback, data); fallbackErr != nil {
			return fmt.Errorf("write debug log: %v; fallback: %w", primaryErr, fallbackErr)
		}
		activePath.Store(fallback)
		mirrorSessionEntry(entry, data)
		return nil
	}
}

func writeAt(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := rotateIfNeeded(path, int64(len(data))); err != nil {
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

// Recent returns the newest entries in chronological order.
func Recent(limit int) ([]Entry, error) {
	if limit < 1 {
		limit = 20
	}
	mu.Lock()
	defer mu.Unlock()

	file, err := os.Open(Path())
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

// Clear removes the active and rotated diagnostic logs.
func Clear() error {
	mu.Lock()
	defer mu.Unlock()
	path := Path()
	var first error
	for index := 0; index <= rotatedFiles; index++ {
		candidate := path
		if index > 0 {
			candidate = fmt.Sprintf("%s.%d", path, index)
		}
		if err := os.Remove(candidate); err != nil && !errors.Is(err, os.ErrNotExist) && first == nil {
			first = err
		}
	}
	return first
}

func rotateIfNeeded(path string, incoming int64) error {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Size()+incoming <= maxLogBytes {
		return nil
	}
	_ = os.Remove(fmt.Sprintf("%s.%d", path, rotatedFiles))
	for index := rotatedFiles - 1; index >= 1; index-- {
		oldPath := fmt.Sprintf("%s.%d", path, index)
		newPath := fmt.Sprintf("%s.%d", path, index+1)
		if err := os.Rename(oldPath, newPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return os.Rename(path, path+".1")
}

func sanitizeFields(fields map[string]any) map[string]any {
	return sanitizeMap(fields, 0)
}

func sanitizeMap(fields map[string]any, depth int) map[string]any {
	if len(fields) == 0 {
		return nil
	}
	if depth > 4 {
		return map[string]any{"value": "[TRUNCATED]"}
	}
	out := make(map[string]any, len(fields))
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if sensitiveKey(key) {
			out[key] = "[REDACTED]"
			continue
		}
		out[key] = sanitizeValue(fields[key], depth+1)
	}
	return out
}

func sanitizeValue(value any, depth int) any {
	if depth > 4 {
		return "[TRUNCATED]"
	}
	switch typed := value.(type) {
	case string:
		return redact(limitString(typed, 4000))
	case error:
		return redact(limitString(typed.Error(), 4000))
	case map[string]any:
		return sanitizeMap(typed, depth+1)
	case []string:
		out := make([]string, 0, min(len(typed), 32))
		for _, item := range typed {
			if len(out) == cap(out) {
				break
			}
			out = append(out, redact(limitString(item, 1000)))
		}
		return out
	case []any:
		limit := min(len(typed), 32)
		out := make([]any, 0, limit)
		for _, item := range typed[:limit] {
			out = append(out, sanitizeValue(item, depth+1))
		}
		return out
	default:
		if _, err := json.Marshal(value); err == nil {
			return value
		}
		return redact(limitString(fmt.Sprint(value), 1000))
	}
}

func sensitiveKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	for _, marker := range []string{"key", "secret", "password", "authorization", "credential"} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	// Token counts and budgets are diagnostics, not credentials. Only redact
	// fields whose name identifies an actual bearer/access token.
	if strings.Contains(key, "tokens") || strings.Contains(key, "token_count") || strings.Contains(key, "token_budget") {
		return false
	}
	switch key {
	case "token", "access_token", "refresh_token", "auth_token", "bearer_token", "api_token", "id_token":
		return true
	}
	return strings.HasSuffix(key, "_access_token") || strings.HasSuffix(key, "_refresh_token")
}

func redact(value string) string {
	value = bearerPattern.ReplaceAllString(value, `${1}[REDACTED]`)
	value = keyPattern.ReplaceAllString(value, `${1}[REDACTED]`)
	return openAIKey.ReplaceAllString(value, "[REDACTED]")
}

func limitString(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max]) + "…"
}

func fallback(value, defaultValue string) string {
	if value == "" {
		return defaultValue
	}
	return value
}
