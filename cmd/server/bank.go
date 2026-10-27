package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/tody-aja/jira-ai-agent/internal/agent"
	"github.com/tody-aja/jira-ai-agent/internal/confluence"
	"github.com/tody-aja/jira-ai-agent/internal/devdb"
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
		// The ticket code and what changes follow, e.g.
		// tody/feature/core-312-bulk-overpayment-withdraw.
		BranchPrefix: strings.TrimSpace(getenv("BRANCH_PREFIX")),
	}
	if config.DevDB.EnvFile != "" {
		runner, err := devdb.NewRunner(config.DevDB.EnvFile, config.DevDB.Address)
		if err != nil {
			return workflow.BankDependencies{}, nil, fmt.Errorf("DEV_DB_ENV_FILE: %w", err)
		}
		deps.SQL = sqlRunner{runner}
		log.Printf("Development database %s (%s): the TRD planner and engineer read it through %s; approved SQL plans are applied to it at Ready To Implement", config.DevDB.Address, strings.Join(devdb.Names(runner.Connections), ", "), config.DevDB.Tool)
	} else {
		log.Println("Warning: DEV_DB_ENV_FILE is not configured, so SQL migration plans are written without the development database and issues with one stop at Ready To Implement")
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

// sqlRunner adapts the development database runner to the workflow.
type sqlRunner struct {
	*devdb.Runner
}

func (r sqlRunner) CheckSQL(database, script string, readOnly bool) error {
	_, err := r.Check(database, script, readOnly)
	return err
}

func (r sqlRunner) RunSQL(ctx context.Context, database, script string, readOnly bool) (string, error) {
	var output strings.Builder
	err := r.Run(ctx, database, script, readOnly, &output)
	return output.String(), err
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

func (p confluencePublisher) OpenComments(ctx context.Context, pageID string) ([]workflow.PageComment, error) {
	comments, err := p.Client.OpenComments(ctx, pageID)
	if err != nil {
		return nil, err
	}
	converted := make([]workflow.PageComment, 0, len(comments))
	for _, comment := range comments {
		converted = append(converted, workflow.PageComment(comment))
	}
	return converted, nil
}

func (p confluencePublisher) CloseComment(ctx context.Context, comment workflow.PageComment, reply string) error {
	return p.Client.CloseComment(ctx, confluence.Comment(comment), reply)
}
