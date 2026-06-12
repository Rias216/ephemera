package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type multiPatchSpec struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Patch   string `json:"patch,omitempty"`
}

func normalizeMultiPatchArgument(raw any) ([]any, error) {
	if text, ok := raw.(string); ok {
		text = strings.TrimSpace(text)
		if text == "" {
			return nil, fmt.Errorf("patches is empty")
		}
		var decoded any
		decoder := json.NewDecoder(strings.NewReader(text))
		decoder.UseNumber()
		if err := decoder.Decode(&decoded); err != nil {
			return nil, fmt.Errorf("patches JSON is invalid: %w", err)
		}
		raw = decoded
	}
	var values []any
	switch typed := raw.(type) {
	case []any:
		values = typed
	case []map[string]any:
		values = make([]any, len(typed))
		for index := range typed {
			values[index] = typed[index]
		}
	case map[string]any:
		values = []any{typed}
	default:
		return nil, fmt.Errorf("patches must be an array of objects")
	}
	for index, value := range values {
		entry, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("patch %d must be an object", index+1)
		}
		for alias, canonical := range map[string]string{
			"file": "path", "filename": "path", "text": "content", "body": "content", "patch": "content",
		} {
			aliasValue, exists := entry[alias]
			if !exists {
				continue
			}
			if current, conflict := entry[canonical]; conflict && fmt.Sprint(current) != fmt.Sprint(aliasValue) {
				return nil, fmt.Errorf("patch %d received conflicting %q and %q fields", index+1, canonical, alias)
			}
			if _, exists := entry[canonical]; !exists {
				entry[canonical] = aliasValue
			}
			delete(entry, alias)
		}
		values[index] = entry
	}
	return values, nil
}

type multiPatchTarget struct {
	path    string
	rel     string
	content string
	existed bool
	mode    os.FileMode
	before  []byte
}

func parseMultiPatchSpecs(call Call) ([]multiPatchSpec, error) {
	raw, ok := call.Arguments["patches"]
	if !ok || raw == nil {
		return nil, fmt.Errorf("patches is required")
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("encode patches: %w", err)
	}
	var specs []multiPatchSpec
	if err := json.Unmarshal(data, &specs); err != nil {
		return nil, fmt.Errorf("patches must be an array of {path, content} objects: %w", err)
	}
	if len(specs) < 2 {
		return nil, fmt.Errorf("apply_multi_patch requires at least two patches")
	}
	for index := range specs {
		specs[index].Path = strings.TrimSpace(specs[index].Path)
		if specs[index].Content == "" && specs[index].Patch != "" {
			specs[index].Content = specs[index].Patch
		}
		if specs[index].Path == "" {
			return nil, fmt.Errorf("patch %d requires path", index+1)
		}
	}
	return specs, nil
}

func (r Registry) prepareMultiPatch(call Call) ([]multiPatchTarget, error) {
	specs, err := parseMultiPatchSpecs(call)
	if err != nil {
		return nil, err
	}
	targets := make([]multiPatchTarget, 0, len(specs))
	seen := make(map[string]bool, len(specs))
	for index, spec := range specs {
		path, err := r.safePath(spec.Path)
		if err != nil {
			return nil, fmt.Errorf("patch %d: %w", index+1, err)
		}
		key := filepath.Clean(path)
		if seen[key] {
			return nil, fmt.Errorf("patch %d repeats target %q", index+1, spec.Path)
		}
		seen[key] = true
		target := multiPatchTarget{path: path, rel: r.displayPath(path), content: spec.Content}
		info, statErr := os.Stat(path)
		switch {
		case statErr == nil:
			if info.IsDir() {
				return nil, fmt.Errorf("patch %d target is a directory: %s", index+1, spec.Path)
			}
			before, readErr := os.ReadFile(path)
			if readErr != nil {
				return nil, fmt.Errorf("patch %d snapshot: %w", index+1, readErr)
			}
			target.existed = true
			target.mode = info.Mode()
			target.before = before
		case os.IsNotExist(statErr):
			target.mode = 0o600
		default:
			return nil, fmt.Errorf("patch %d snapshot: %w", index+1, statErr)
		}
		targets = append(targets, target)
	}
	return targets, nil
}

func (r Registry) applyMultiPatch(call Call) Result {
	targets, err := r.prepareMultiPatch(call)
	if err != nil {
		return fail(call.Name, err.Error())
	}
	for index, target := range targets {
		if err := os.MkdirAll(filepath.Dir(target.path), 0o700); err != nil {
			rollbackErr := rollbackMultiPatch(r.WorkspaceRoot, targets[:index+1])
			return multiPatchFailure(call.Name, target.rel, err, rollbackErr)
		}
		mode := target.mode.Perm()
		if mode == 0 {
			mode = 0o600
		}
		if err := os.WriteFile(target.path, []byte(target.content), mode); err != nil {
			rollbackErr := rollbackMultiPatch(r.WorkspaceRoot, targets[:index+1])
			return multiPatchFailure(call.Name, target.rel, err, rollbackErr)
		}
	}
	paths := make([]string, 0, len(targets))
	for _, target := range targets {
		paths = append(paths, target.rel)
	}
	result := ok(call.Name, fmt.Sprintf("atomically wrote %d files: %s", len(paths), strings.Join(paths, ", ")))
	result.Metadata = map[string]any{"paths": paths, "changed": true, "atomic": true, "patch_count": len(paths)}
	return result
}

func (r Registry) previewMultiPatch(call Call) Result {
	targets, err := r.prepareMultiPatch(call)
	if err != nil {
		return fail(call.Name, err.Error())
	}
	var previews []string
	paths := make([]string, 0, len(targets))
	for _, target := range targets {
		previews = append(previews, renderDryRunDiff(target.rel, string(target.before), target.content))
		paths = append(paths, target.rel)
	}
	return Result{
		Tool:   call.Name,
		OK:     true,
		Output: strings.Join(previews, "\n\n"),
		Metadata: map[string]any{
			"dry_run": true, "changed": false, "atomic": true, "paths": paths, "patch_count": len(paths),
		},
	}
}

func rollbackMultiPatch(root string, targets []multiPatchTarget) error {
	var failures []string
	for index := len(targets) - 1; index >= 0; index-- {
		target := targets[index]
		if !target.existed {
			if err := os.Remove(target.path); err != nil && !os.IsNotExist(err) {
				failures = append(failures, err.Error())
			}
			removeEmptyToolParents(filepath.Dir(target.path), root)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target.path), 0o700); err != nil {
			failures = append(failures, err.Error())
			continue
		}
		if err := os.WriteFile(target.path, target.before, target.mode.Perm()); err != nil {
			failures = append(failures, err.Error())
			continue
		}
		if err := os.Chmod(target.path, target.mode.Perm()); err != nil {
			failures = append(failures, err.Error())
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("%s", strings.Join(failures, "; "))
	}
	return nil
}

func multiPatchFailure(tool, path string, writeErr, rollbackErr error) Result {
	message := fmt.Sprintf("atomic write failed for %s: %v; all completed targets were rolled back", path, writeErr)
	metadata := map[string]any{"atomic": true, "rolled_back": rollbackErr == nil, "failed_path": path}
	if rollbackErr != nil {
		message += "; rollback error: " + rollbackErr.Error()
	}
	result := fail(tool, message)
	result.Metadata = metadata
	return result
}

func removeEmptyToolParents(directory, root string) {
	root = filepath.Clean(root)
	for directory = filepath.Clean(directory); directory != root && strings.HasPrefix(directory, root+string(os.PathSeparator)); directory = filepath.Dir(directory) {
		if err := os.Remove(directory); err != nil {
			return
		}
	}
}
