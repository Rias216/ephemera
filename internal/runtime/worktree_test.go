package runtime

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCreateAndCleanupWorktree(t *testing.T) {
	repository := t.TempDir()
	runGit := func(args ...string) {
		command := exec.Command("git", args...)
		command.Dir = repository
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, output)
		}
	}
	runGit("init", "-q")
	runGit("config", "user.email", "eval@example.com")
	runGit("config", "user.name", "Eval")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit("add", "README.md")
	runGit("commit", "-qm", "base")

	worktree, err := CreateWorktree(context.Background(), repository, "HEAD", "candidate-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(worktree.Directory, "README.md")); err != nil {
		t.Fatal(err)
	}
	if err := worktree.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(worktree.Directory); !os.IsNotExist(err) {
		t.Fatalf("worktree directory still exists: %v", err)
	}
}
