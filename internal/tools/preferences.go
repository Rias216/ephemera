package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type preferenceFile struct {
	Version     int                `json:"version"`
	Preferences []storedPreference `json:"preferences"`
}

type storedPreference struct {
	Text          string    `json:"text"`
	UpdatedAt     time.Time `json:"updated_at"`
	Reinforcement int       `json:"reinforcement"`
}

func (r Registry) recordPreference(call Call) Result {
	preference := strings.Join(strings.Fields(strings.TrimSpace(argString(call, "preference"))), " ")
	if preference == "" {
		return fail(call.Name, "preference is required")
	}
	if len([]rune(preference)) > 500 {
		return fail(call.Name, "preference must be 500 characters or fewer")
	}
	scope := strings.ToLower(strings.TrimSpace(argStringDefault(call, "scope", "global")))
	var path string
	switch scope {
	case "global", "user":
		home, err := os.UserHomeDir()
		if err != nil {
			return fail(call.Name, err.Error())
		}
		path = filepath.Join(home, ".ephemera", "global-memory.json")
		scope = "global"
	case "project", "workspace":
		path = filepath.Join(r.WorkspaceRoot, ".ephemera", "preferences.json")
		scope = "project"
	default:
		return fail(call.Name, "scope must be global or project")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fail(call.Name, err.Error())
	}
	memory := preferenceFile{Version: 1}
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &memory)
	}
	now := time.Now().UTC()
	found := false
	for index := range memory.Preferences {
		if strings.EqualFold(memory.Preferences[index].Text, preference) {
			memory.Preferences[index].Text = preference
			memory.Preferences[index].UpdatedAt = now
			memory.Preferences[index].Reinforcement++
			found = true
			break
		}
	}
	if !found {
		memory.Preferences = append(memory.Preferences, storedPreference{Text: preference, UpdatedAt: now, Reinforcement: 1})
	}
	data, err := json.MarshalIndent(memory, "", "  ")
	if err != nil {
		return fail(call.Name, err.Error())
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return fail(call.Name, err.Error())
	}
	if err := os.Rename(tmp, path); err != nil {
		return fail(call.Name, err.Error())
	}
	result := ok(call.Name, fmt.Sprintf("recorded %s preference", scope))
	result.Metadata = map[string]any{"scope": scope, "path": path, "changed": true, "reinforced": found}
	return result
}
