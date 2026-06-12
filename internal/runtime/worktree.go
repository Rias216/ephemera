package runtime

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var worktreeNamePattern = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// Worktree is an isolated candidate workspace for branch-and-select execution.
type Worktree struct {
	Repository string
	Directory  string
	Branch     string
	created    bool
}

func CreateWorktree(ctx context.Context, repository, baseRef, name string) (*Worktree, error) {
	repository, err := filepath.Abs(repository)
	if err != nil {
		return nil, err
	}
	if baseRef = strings.TrimSpace(baseRef); baseRef == "" {
		baseRef = "HEAD"
	}
	name = strings.Trim(worktreeNamePattern.ReplaceAllString(strings.TrimSpace(name), "-"), "-")
	if name == "" {
		name = fmt.Sprintf("candidate-%d", time.Now().UnixNano())
	}
	parent := filepath.Join(repository, ".ephemera", "worktrees")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, err
	}
	directory := filepath.Join(parent, name)
	branch := "ephemera/" + name
	command := exec.CommandContext(ctx, "git", "worktree", "add", "-b", branch, directory, baseRef)
	command.Dir = repository
	if output, err := command.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("create git worktree: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return &Worktree{Repository: repository, Directory: directory, Branch: branch, created: true}, nil
}

func (w *Worktree) Cleanup(ctx context.Context) error {
	if w == nil || !w.created {
		return nil
	}
	remove := exec.CommandContext(ctx, "git", "worktree", "remove", "--force", w.Directory)
	remove.Dir = w.Repository
	if output, err := remove.CombinedOutput(); err != nil {
		return fmt.Errorf("remove git worktree: %w: %s", err, strings.TrimSpace(string(output)))
	}
	deleteBranch := exec.CommandContext(ctx, "git", "branch", "-D", w.Branch)
	deleteBranch.Dir = w.Repository
	if output, err := deleteBranch.CombinedOutput(); err != nil {
		return fmt.Errorf("delete worktree branch: %w: %s", err, strings.TrimSpace(string(output)))
	}
	w.created = false
	return nil
}
