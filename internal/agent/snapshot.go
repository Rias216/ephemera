package agent

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type workspaceSnapshot struct {
	Version   int                      `json:"version"`
	Root      string                   `json:"root"`
	Directory string                   `json:"directory"`
	CreatedAt time.Time                `json:"created_at"`
	Entries   map[string]snapshotEntry `json:"entries"`
	Bytes     int64                    `json:"bytes"`
}

type snapshotEntry struct {
	Mode      fs.FileMode `json:"mode"`
	Directory bool        `json:"directory,omitempty"`
	Symlink   string      `json:"symlink,omitempty"`
}

type rollbackReport struct {
	RestoredFiles int
	RemovedFiles  int
	Bytes         int64
}

func createWorkspaceSnapshot(root string, maxBytes int64) (*workspaceSnapshot, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	directory, err := os.MkdirTemp("", "ephemera-snapshot-*")
	if err != nil {
		return nil, err
	}
	snapshot := &workspaceSnapshot{
		Version:   1,
		Root:      root,
		Directory: directory,
		CreatedAt: time.Now().UTC(),
		Entries:   map[string]snapshotEntry{},
	}
	cleanup := func(snapshotErr error) (*workspaceSnapshot, error) {
		_ = os.RemoveAll(directory)
		return nil, snapshotErr
	}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if entry.IsDir() && skipSnapshotDir(entry.Name()) {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		item := snapshotEntry{Mode: info.Mode(), Directory: info.IsDir()}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			item.Symlink = target
			snapshot.Entries[rel] = item
			return nil
		}
		snapshot.Entries[rel] = item
		target := filepath.Join(directory, "files", filepath.FromSlash(rel))
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if maxBytes > 0 && snapshot.Bytes+info.Size() > maxBytes {
			return fmt.Errorf("workspace snapshot exceeds configured limit (%d MiB)", maxBytes/(1024*1024))
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		if err := copySnapshotFile(path, target, info.Mode().Perm()); err != nil {
			return err
		}
		snapshot.Bytes += info.Size()
		return nil
	})
	if err != nil {
		return cleanup(err)
	}
	if err := snapshot.persist(); err != nil {
		return cleanup(err)
	}
	return snapshot, nil
}

func loadWorkspaceSnapshot(directory string) (*workspaceSnapshot, error) {
	directory = strings.TrimSpace(directory)
	if directory == "" {
		return nil, fmt.Errorf("snapshot directory is empty")
	}
	data, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		return nil, err
	}
	var snapshot workspaceSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, err
	}
	if snapshot.Version != 1 || snapshot.Root == "" || snapshot.Directory == "" {
		return nil, fmt.Errorf("invalid workspace snapshot manifest")
	}
	return &snapshot, nil
}

func (s *workspaceSnapshot) persist() error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(s.Directory, "manifest.json"), data, 0o600)
}

func (s *workspaceSnapshot) Restore() (rollbackReport, error) {
	var report rollbackReport
	if s == nil {
		return report, fmt.Errorf("snapshot is unavailable")
	}
	root, err := filepath.Abs(s.Root)
	if err != nil {
		return report, err
	}
	var current []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if path == root {
			return nil
		}
		if entry.IsDir() && skipSnapshotDir(entry.Name()) {
			return filepath.SkipDir
		}
		current = append(current, path)
		return nil
	})
	if err != nil {
		return report, err
	}
	sort.Slice(current, func(i, j int) bool { return len(current[i]) > len(current[j]) })
	for _, path := range current {
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			continue
		}
		rel = filepath.ToSlash(rel)
		if _, existed := s.Entries[rel]; existed {
			continue
		}
		if removeErr := os.RemoveAll(path); removeErr == nil {
			report.RemovedFiles++
		}
	}

	paths := make([]string, 0, len(s.Entries))
	for rel := range s.Entries {
		paths = append(paths, rel)
	}
	sort.Slice(paths, func(i, j int) bool {
		leftDepth := strings.Count(paths[i], "/")
		rightDepth := strings.Count(paths[j], "/")
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return paths[i] < paths[j]
	})
	for _, rel := range paths {
		entry := s.Entries[rel]
		target := filepath.Join(root, filepath.FromSlash(rel))
		if entry.Directory {
			if err := os.MkdirAll(target, entry.Mode.Perm()); err != nil {
				return report, err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return report, err
		}
		_ = os.RemoveAll(target)
		if entry.Symlink != "" {
			if err := os.Symlink(entry.Symlink, target); err != nil {
				return report, err
			}
			report.RestoredFiles++
			continue
		}
		source := filepath.Join(s.Directory, "files", filepath.FromSlash(rel))
		info, err := os.Stat(source)
		if err != nil {
			return report, err
		}
		if err := copySnapshotFile(source, target, entry.Mode.Perm()); err != nil {
			return report, err
		}
		report.RestoredFiles++
		report.Bytes += info.Size()
	}
	return report, nil
}

func (s *workspaceSnapshot) Cleanup() {
	if s != nil && strings.TrimSpace(s.Directory) != "" {
		_ = os.RemoveAll(s.Directory)
	}
}

func snapshotPath(snapshot *workspaceSnapshot) string {
	if snapshot == nil {
		return ""
	}
	return snapshot.Directory
}

// RollbackWorkspaceSnapshot restores a retained snapshot created by snapshot
// sandbox mode and removes its temporary data after a successful restore.
func RollbackWorkspaceSnapshot(directory string) (string, error) {
	snapshot, err := loadWorkspaceSnapshot(directory)
	if err != nil {
		return "", err
	}
	report, err := snapshot.Restore()
	if err != nil {
		return "", err
	}
	snapshot.Cleanup()
	return fmt.Sprintf("Restored %d file(s), removed %d new path(s), and copied %d bytes.", report.RestoredFiles, report.RemovedFiles, report.Bytes), nil
}

func copySnapshotFile(source, target string, mode fs.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}

func skipSnapshotDir(name string) bool {
	switch strings.ToLower(name) {
	case ".git", "node_modules", "vendor", "dist", "build", "target", ".next", ".cache", "coverage", "__pycache__", ".venv", "venv":
		return true
	default:
		return false
	}
}
