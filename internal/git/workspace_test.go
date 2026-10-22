package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newRepo creates a repository with one commit on branch main.
func newRepo(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "app")
	run := func(args ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run("init", "--quiet", "--initial-branch", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "--all")
	run("commit", "--quiet", "--message", "initial")
	return dir
}

func TestWorkspacePrepareCommitAndDiff(t *testing.T) {
	repo := newRepo(t)
	workspace := &Workspace{RepoPath: repo, BaseBranch: "main", Root: DefaultRoot(repo)}
	ctx := context.Background()

	dir, err := workspace.Prepare(ctx, "ai/ABC-1")
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if dir != filepath.Join(filepath.Dir(repo), "app-worktrees", "ai-ABC-1") {
		t.Fatalf("Prepare() dir = %q, want worktree beside the repository", dir)
	}

	if committed, err := workspace.CommitAll(ctx, dir, "nothing"); err != nil || committed {
		t.Fatalf("CommitAll() without changes = (%v, %v), want nothing committed", committed, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "feature.txt"), []byte("new feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if committed, err := workspace.CommitAll(ctx, dir, "ABC-1: add feature"); err != nil || !committed {
		t.Fatalf("CommitAll() = (%v, %v), want a commit", committed, err)
	}

	diff, err := workspace.Diff(ctx, dir)
	if err != nil {
		t.Fatalf("Diff() error = %v", err)
	}
	if !strings.Contains(diff, "feature.txt | 1 +") || !strings.Contains(diff, "+new feature") {
		t.Fatalf("Diff() = %q, want stat and patch for the new file", diff)
	}
	if _, err := os.Stat(filepath.Join(repo, "feature.txt")); !os.IsNotExist(err) {
		t.Fatal("main checkout was changed; want changes only in the worktree")
	}

	again, err := workspace.Prepare(ctx, "ai/ABC-1")
	if err != nil || again != dir {
		t.Fatalf("Prepare() again = (%q, %v), want existing worktree", again, err)
	}
}

func TestWorkspacePrepareReusesExistingBranch(t *testing.T) {
	repo := newRepo(t)
	workspace := &Workspace{RepoPath: repo, BaseBranch: "main", Root: DefaultRoot(repo)}
	ctx := context.Background()
	dir, _ := workspace.Prepare(ctx, "ai/ABC-2")
	os.WriteFile(filepath.Join(dir, "kept.txt"), []byte("kept\n"), 0o644)
	workspace.CommitAll(ctx, dir, "keep")
	if output, err := exec.Command("git", "-C", repo, "worktree", "remove", "--force", dir).CombinedOutput(); err != nil {
		t.Fatalf("remove worktree: %v\n%s", err, output)
	}

	dir, err := workspace.Prepare(ctx, "ai/ABC-2")
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "kept.txt")); err != nil {
		t.Fatal("Prepare() lost the branch's earlier commits")
	}
}

func TestWorkspaceCheckRejectsMissingBaseBranch(t *testing.T) {
	repo := newRepo(t)
	workspace := &Workspace{RepoPath: repo, BaseBranch: "master"}
	if err := workspace.Check(context.Background()); err == nil || !strings.Contains(err.Error(), `BASE_BRANCH "master"`) {
		t.Fatalf("Check() error = %v, want missing base branch", err)
	}
	if err := (&Workspace{RepoPath: t.TempDir(), BaseBranch: "main"}).Check(context.Background()); err == nil || !strings.Contains(err.Error(), "not a git repository") {
		t.Fatalf("Check() error = %v, want not a repository", err)
	}
}

func TestWorkspaceRemoveKeepsBranch(t *testing.T) {
	repo := newRepo(t)
	workspace := &Workspace{RepoPath: repo, BaseBranch: "main", Root: DefaultRoot(repo)}
	ctx := context.Background()
	dir, _ := workspace.Prepare(ctx, "feature/ABC-3")
	os.WriteFile(filepath.Join(dir, "done.txt"), []byte("done\n"), 0o644)
	workspace.CommitAll(ctx, dir, "done")
	// Leftover build output must not block removal.
	os.WriteFile(filepath.Join(dir, "output.log"), []byte("log\n"), 0o644)

	if err := workspace.Remove(ctx, "feature/ABC-3"); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("worktree %s still exists", dir)
	}
	if output, err := exec.Command("git", "-C", repo, "checkout", "--quiet", "feature/ABC-3").CombinedOutput(); err != nil {
		t.Fatalf("checkout of the kept branch in the main checkout: %v\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(repo, "done.txt")); err != nil {
		t.Fatal("branch lost its commit")
	}
	if err := workspace.Remove(ctx, "feature/ABC-3"); err != nil {
		t.Fatalf("Remove() of a missing worktree = %v, want no-op", err)
	}
}

func TestWorkspacePushPublishesBranch(t *testing.T) {
	repo := newRepo(t)
	remote := filepath.Join(t.TempDir(), "remote.git")
	for _, args := range [][]string{{"init", "--quiet", "--bare", remote}, {"-C", repo, "remote", "add", "origin", remote}} {
		if output, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	workspace := &Workspace{RepoPath: repo, BaseBranch: "main", Root: DefaultRoot(repo)}
	ctx := context.Background()
	workspace.Prepare(ctx, "feature/ABC-4")

	if err := workspace.Push(ctx, "feature/ABC-4"); err != nil {
		t.Fatalf("Push() error = %v", err)
	}
	if output, err := exec.Command("git", "-C", remote, "rev-parse", "--verify", "refs/heads/feature/ABC-4").CombinedOutput(); err != nil {
		t.Fatalf("remote has no feature/ABC-4: %v\n%s", err, output)
	}
	if err := workspace.Push(ctx, "feature/missing"); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("Push() of a missing branch = %v, want does not exist", err)
	}
}

func TestWorkspaceReferenceFollowsTheBaseBranch(t *testing.T) {
	repo := newRepo(t)
	run := func(dir string, args ...string) string {
		t.Helper()
		output, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	// The main checkout is on another branch with its own change, as when a
	// developer is working on something else.
	run(repo, "checkout", "--quiet", "-b", "feature/other")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("other work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(repo, "commit", "--quiet", "--all", "--message", "other")

	workspace := &Workspace{RepoPath: repo, BaseBranch: "main", Root: DefaultRoot(repo)}
	ctx := context.Background()
	dir, err := workspace.Reference(ctx)
	if err != nil {
		t.Fatalf("Reference() error = %v", err)
	}
	if content, _ := os.ReadFile(filepath.Join(dir, "README.md")); strings.ReplaceAll(string(content), "\r", "") != "hello\n" {
		t.Fatalf("reference README = %q, want main's content", content)
	}

	// main moves on; a stray edit in the reference is discarded.
	run(repo, "checkout", "--quiet", "main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("main v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(repo, "commit", "--quiet", "--all", "--message", "v2")
	run(repo, "checkout", "--quiet", "feature/other")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("stray edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	again, err := workspace.Reference(ctx)
	if err != nil || again != dir {
		t.Fatalf("Reference() again = %q, %v, want the same directory", again, err)
	}
	if content, _ := os.ReadFile(filepath.Join(dir, "README.md")); strings.ReplaceAll(string(content), "\r", "") != "main v2\n" {
		t.Fatalf("reference README = %q, want main's new content", content)
	}
	if branch := run(repo, "rev-parse", "--abbrev-ref", "HEAD"); branch != "feature/other" {
		t.Fatalf("main checkout is on %q, want it left alone", branch)
	}
}
