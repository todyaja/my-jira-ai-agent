// Package git manages the worktrees the implementation agents work in.
package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// Workspace gives each branch its own worktree of the repository at
// RepoPath, under Root, so agents never touch the main checkout.
type Workspace struct {
	RepoPath   string
	BaseBranch string
	Root       string

	mu sync.Mutex
}

// DefaultRoot is where worktrees go when none is configured: a directory
// beside the repository, e.g. D:/Code/app-worktrees for D:/Code/app.
func DefaultRoot(repoPath string) string {
	clean := filepath.Clean(repoPath)
	return filepath.Join(filepath.Dir(clean), filepath.Base(clean)+"-worktrees")
}

// Check reports why the repository cannot be worked in, or nil.
func (w *Workspace) Check(ctx context.Context) error {
	if w.RepoPath == "" {
		return fmt.Errorf("REPO_PATH is not configured")
	}
	if _, err := w.git(ctx, w.RepoPath, "rev-parse", "--git-dir"); err != nil {
		return fmt.Errorf("REPO_PATH %q is not a git repository", w.RepoPath)
	}
	if !w.branchExists(ctx, w.BaseBranch) {
		return fmt.Errorf("BASE_BRANCH %q does not exist in %s", w.BaseBranch, w.RepoPath)
	}
	return nil
}

// Prepare returns the worktree checked out on branch, creating the branch
// from BaseBranch and the worktree when they do not exist yet. An existing
// branch keeps its commits.
func (w *Workspace) Prepare(ctx context.Context, branch string) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if err := w.Check(ctx); err != nil {
		return "", err
	}
	dir := w.worktreeDir(branch)
	if _, err := os.Stat(dir); err == nil {
		current, err := w.git(ctx, dir, "rev-parse", "--abbrev-ref", "HEAD")
		if err != nil || current != branch {
			return "", fmt.Errorf("worktree %s exists but is not on branch %s", dir, branch)
		}
		return dir, nil
	}

	if _, err := w.git(ctx, w.RepoPath, "worktree", "prune"); err != nil {
		return "", err
	}
	if err := os.MkdirAll(w.Root, 0o755); err != nil {
		return "", fmt.Errorf("create worktree root: %w", err)
	}
	args := []string{"worktree", "add", dir, branch}
	if !w.branchExists(ctx, branch) {
		args = []string{"worktree", "add", "-b", branch, dir, w.BaseBranch}
	}
	if _, err := w.git(ctx, w.RepoPath, args...); err != nil {
		return "", err
	}
	return dir, nil
}

// CommitAll commits every change in the worktree and reports whether there
// was anything to commit.
func (w *Workspace) CommitAll(ctx context.Context, dir, message string) (bool, error) {
	if _, err := w.git(ctx, dir, "add", "--all"); err != nil {
		return false, err
	}
	if _, err := w.git(ctx, dir, "diff", "--cached", "--quiet"); err == nil {
		return false, nil
	}
	if _, err := w.git(ctx, dir, "commit", "--quiet", "--message", message); err != nil {
		return false, err
	}
	return true, nil
}

// Diff returns the summary and full diff of the branch against BaseBranch.
func (w *Workspace) Diff(ctx context.Context, dir string) (string, error) {
	base := w.BaseBranch + "...HEAD"
	stat, err := w.git(ctx, dir, "diff", "--stat", base)
	if err != nil {
		return "", err
	}
	diff, err := w.git(ctx, dir, "diff", base)
	if err != nil {
		return "", err
	}
	return stat + "\n\n" + diff, nil
}

// Remove deletes branch's worktree, keeping the branch and its commits, so
// the branch can be checked out in the main checkout. Every change was
// committed before review, so anything left in the worktree is build or test
// output and is discarded. Removing a worktree that does not exist is a no-op.
func (w *Workspace) Remove(ctx context.Context, branch string) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	dir := w.worktreeDir(branch)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}
	_, err := w.git(ctx, w.RepoPath, "worktree", "remove", "--force", dir)
	return err
}

// Push publishes branch to the origin remote with git's own credentials and
// sets it as the branch's upstream.
func (w *Workspace) Push(ctx context.Context, branch string) error {
	if !w.branchExists(ctx, branch) {
		return fmt.Errorf("branch %s does not exist in %s", branch, w.RepoPath)
	}
	_, err := w.git(ctx, w.RepoPath, "push", "--quiet", "--set-upstream", "origin", branch)
	return err
}

// worktreeDir is where branch's worktree lives: its name with path
// separators turned into dashes, e.g. feature-ABC-1 for feature/ABC-1.
func (w *Workspace) worktreeDir(branch string) string {
	return filepath.Join(w.Root, strings.NewReplacer("/", "-", `\`, "-").Replace(branch))
}

func (w *Workspace) branchExists(ctx context.Context, branch string) bool {
	_, err := w.git(ctx, w.RepoPath, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// git runs a git command in dir and returns its trimmed output.
func (w *Workspace) git(ctx context.Context, dir string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	// Fail instead of waiting forever for a credentials prompt nobody sees.
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", fmt.Errorf("git %s: %s", args[0], firstLine(stderr.String(), exitErr.Error()))
		}
		return "", fmt.Errorf("git %s: %w", args[0], err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

func firstLine(text, fallback string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return fallback
	}
	line, _, _ := strings.Cut(text, "\n")
	return line
}
