package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/tody-aja/jira-ai-agent/internal/agent"
	"github.com/tody-aja/jira-ai-agent/internal/confluence"
	"github.com/tody-aja/jira-ai-agent/internal/git"
	"github.com/tody-aja/jira-ai-agent/internal/gitlab"
	"github.com/tody-aja/jira-ai-agent/internal/repos"
	"github.com/tody-aja/jira-ai-agent/internal/workflow"
)

// defaultBankBaseBranch is the bank repositories' usual base branch.
const defaultBankBaseBranch = "master"

// loadBankDependencies configures the bank workflow: the repositories under
// REPOS_ROOT, the document writers and planner, Confluence filing and
// GitLab merge requests.
func loadBankDependencies(getenv func(string) string, publisher *confluence.Client) (workflow.BankDependencies, *repos.Registry, error) {
	root := strings.TrimSpace(getenv("REPOS_ROOT"))
	if root == "" {
		return workflow.BankDependencies{}, nil, fmt.Errorf("REPOS_ROOT is not configured; the bank workflow needs the folder holding the team's repositories")
	}
	baseBranch := strings.TrimSpace(getenv("BASE_BRANCH"))
	if baseBranch == "" {
		baseBranch = defaultBankBaseBranch
	}
	baseBranches, err := repos.ParseBaseBranches(getenv("REPO_BASE_BRANCHES"))
	if err != nil {
		return workflow.BankDependencies{}, nil, err
	}
	registry, err := repos.Discover(root, baseBranch, baseBranches)
	if err != nil {
		return workflow.BankDependencies{}, nil, err
	}

	config, err := agent.LoadClaudeConfig(getenv)
	if err != nil {
		return workflow.BankDependencies{}, nil, err
	}
	planner, err := agent.NewClaudeRepoPlanner(config, registry.Root)
	if err != nil {
		return workflow.BankDependencies{}, nil, err
	}
	docsDir := strings.TrimSpace(getenv("DOCS_DIR"))
	if docsDir == "" {
		docsDir = registry.Root
	}
	if docsDir, err = filepath.Abs(docsDir); err != nil {
		return workflow.BankDependencies{}, nil, fmt.Errorf("resolve DOCS_DIR: %w", err)
	}
	addDirs := []string{registry.Root}
	if skills := skillsDir(getenv); skills != "" {
		addDirs = append(addDirs, skills)
	}
	writer, err := agent.NewClaudeDocumentWriter(config, docsDir, addDirs)
	if err != nil {
		return workflow.BankDependencies{}, nil, err
	}

	deps := workflow.BankDependencies{
		Repos:     registry,
		Documents: writer,
		Planner:   planner,
		Workspace: workspaces(),
		Publisher: confluencePublisher{publisher},
	}
	if token := strings.TrimSpace(getenv("GITLAB_TOKEN")); token != "" {
		baseURL := strings.TrimSpace(getenv("GITLAB_URL"))
		if baseURL == "" {
			return workflow.BankDependencies{}, nil, fmt.Errorf("GITLAB_URL must be set when GITLAB_TOKEN is")
		}
		deps.MergeRequests = gitlab.RepoOpener{Client: &gitlab.Client{BaseURL: baseURL, Token: token, HTTPClient: &http.Client{Timeout: apiTimeout}}}
	}
	return deps, registry, nil
}

// skillsDir is where the user's Claude skills, such as make-fsd, live:
// SKILLS_DIR, or ~/.claude/skills.
func skillsDir(getenv func(string) string) string {
	if dir := strings.TrimSpace(getenv("SKILLS_DIR")); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "skills")
}

// workspaces returns one worktree manager per repository, reused across
// issues so its lock serializes git commands on the repository. Worktrees go
// beside each repository.
func workspaces() func(repos.Repo) workflow.Workspace {
	var mu sync.Mutex
	byPath := map[string]*git.Workspace{}
	return func(repo repos.Repo) workflow.Workspace {
		mu.Lock()
		defer mu.Unlock()
		workspace, found := byPath[repo.Path]
		if !found {
			workspace = &git.Workspace{RepoPath: repo.Path, BaseBranch: repo.BaseBranch, Root: git.DefaultRoot(repo.Path)}
			byPath[repo.Path] = workspace
		}
		return workspace
	}
}

// confluencePublisher adapts the Confluence client to the workflow.
type confluencePublisher struct {
	*confluence.Client
}

func (p confluencePublisher) PublishPageUnder(ctx context.Context, parents []string, title, markdown string) (workflow.PublishedPage, error) {
	page, err := p.Client.PublishPageUnder(ctx, parents, title, markdown)
	return workflow.PublishedPage{ID: page.ID, URL: page.URL}, err
}
