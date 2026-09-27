package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/tody-aja/jira-ai-agent/internal/agent"
	"github.com/tody-aja/jira-ai-agent/internal/confluence"
	"github.com/tody-aja/jira-ai-agent/internal/git"
	"github.com/tody-aja/jira-ai-agent/internal/jira"
	"github.com/tody-aja/jira-ai-agent/internal/workflow"
)

const defaultPollInterval = 60 * time.Second

// apiTimeout bounds each Jira and Confluence request, so one stuck request
// cannot stall the poller.
const apiTimeout = 60 * time.Second

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("Warning: .env file not found")
	}

	myAccountID := os.Getenv("JIRA_ACCOUNT_ID")

	if myAccountID == "" {
		log.Fatal("JIRA_ACCOUNT_ID is not configured")
	}

	pollInterval, err := loadPollInterval(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	agents, err := buildAgents(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	if err := agents.planner.CheckRepository(); err != nil {
		log.Printf("Warning: the Planning step will fail until this is fixed: %v", err)
	}
	workspace, err := loadWorkspace(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	if err := workspace.Check(context.Background()); err != nil {
		log.Printf("Warning: the Implementing and AI Review steps will fail until this is fixed: %v", err)
	}
	maxReviewCycles, err := loadMaxReviewCycles(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	confluenceClient, err := loadConfluenceClient(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	jiraClient := jira.NewClient(
		os.Getenv("JIRA_BASE_URL"),
		os.Getenv("JIRA_EMAIL"),
		os.Getenv("JIRA_API_TOKEN"),
		&http.Client{Timeout: apiTimeout},
	)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})
	server := &http.Server{Addr: ":8080", Handler: mux}
	go func() {
		log.Println("Jira AI Agent listening on :8080")
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
	}()

	poller := &workflow.Poller{
		JQL:      workflow.AssignedIssuesJQL(myAccountID, os.Getenv("JIRA_PROJECT_KEY")),
		Searcher: jiraClient,
		Dependencies: workflow.Dependencies{
			Reader:          jiraClient,
			Writer:          jiraClient,
			Generator:       agents.prd,
			Planner:         agents.planner,
			Engineer:        agents.engineer,
			Reviewer:        agents.reviewer,
			Workspace:       workspace,
			MaxReviewCycles: maxReviewCycles,
			Publisher:       confluenceClient,
			Logger:          log.Default(),
		},
	}
	log.Printf("Polling Jira every %s: %s", pollInterval, poller.JQL)
	poller.Run(ctx, pollInterval)

	shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownContext)
}

// workflowAgents are the Claude agents behind each AI workflow step.
type workflowAgents struct {
	prd      *agent.ClaudePRDGenerator
	planner  *agent.ClaudeTRDGenerator
	engineer *agent.ClaudeEngineer
	reviewer *agent.ClaudeReviewer
}

func buildAgents(getenv func(string) string) (workflowAgents, error) {
	config, err := agent.LoadClaudeConfig(getenv)
	if err != nil {
		return workflowAgents{}, err
	}
	var agents workflowAgents
	if agents.prd, err = agent.NewClaudePRDGenerator(config); err != nil {
		return workflowAgents{}, err
	}
	if agents.planner, err = agent.NewClaudeTRDGenerator(config, strings.TrimSpace(getenv("REPO_PATH"))); err != nil {
		return workflowAgents{}, err
	}
	if agents.engineer, err = agent.NewClaudeEngineer(config); err != nil {
		return workflowAgents{}, err
	}
	if agents.reviewer, err = agent.NewClaudeReviewer(config); err != nil {
		return workflowAgents{}, err
	}
	return agents, nil
}

// loadWorkspace configures the worktrees issues are implemented in: the
// repository at REPO_PATH, branched from BASE_BRANCH (default main), with
// worktrees under WORKTREES_PATH (default beside the repository).
func loadWorkspace(getenv func(string) string) (*git.Workspace, error) {
	workspace := &git.Workspace{BaseBranch: strings.TrimSpace(getenv("BASE_BRANCH"))}
	if workspace.BaseBranch == "" {
		workspace.BaseBranch = "main"
	}
	if repoPath := strings.TrimSpace(getenv("REPO_PATH")); repoPath != "" {
		absolute, err := filepath.Abs(repoPath)
		if err != nil {
			return nil, fmt.Errorf("resolve REPO_PATH: %w", err)
		}
		workspace.RepoPath = absolute
		workspace.Root = git.DefaultRoot(absolute)
	}
	if root := strings.TrimSpace(getenv("WORKTREES_PATH")); root != "" {
		absolute, err := filepath.Abs(root)
		if err != nil {
			return nil, fmt.Errorf("resolve WORKTREES_PATH: %w", err)
		}
		workspace.Root = absolute
	}
	return workspace, nil
}

func loadMaxReviewCycles(getenv func(string) string) (int, error) {
	value := strings.TrimSpace(getenv("MAX_REVIEW_CYCLES"))
	if value == "" {
		return workflow.DefaultMaxReviewCycles, nil
	}
	cycles, err := strconv.Atoi(value)
	if err != nil || cycles <= 0 {
		return 0, fmt.Errorf("MAX_REVIEW_CYCLES must be a positive integer")
	}
	return cycles, nil
}

// loadConfluenceClient configures publishing to Confluence. The site and
// credentials default to Jira's, since Atlassian Cloud shares them.
func loadConfluenceClient(getenv func(string) string) (*confluence.Client, error) {
	valueOrJira := func(key, jiraKey string) string {
		if value := strings.TrimSpace(getenv(key)); value != "" {
			return value
		}
		return strings.TrimSpace(getenv(jiraKey))
	}
	client := &confluence.Client{
		BaseURL:      valueOrJira("CONFLUENCE_BASE_URL", "JIRA_BASE_URL"),
		Email:        valueOrJira("CONFLUENCE_EMAIL", "JIRA_EMAIL"),
		Token:        valueOrJira("CONFLUENCE_API_TOKEN", "JIRA_API_TOKEN"),
		SpaceKey:     strings.TrimSpace(getenv("CONFLUENCE_SPACE_KEY")),
		ParentPageID: strings.TrimSpace(getenv("CONFLUENCE_PARENT_PAGE_ID")),
		HTTPClient:   &http.Client{Timeout: apiTimeout},
	}
	if client.SpaceKey == "" {
		return nil, fmt.Errorf("CONFLUENCE_SPACE_KEY is not configured")
	}
	if client.BaseURL == "" {
		return nil, fmt.Errorf("CONFLUENCE_BASE_URL or JIRA_BASE_URL must be configured")
	}
	return client, nil
}

func loadPollInterval(getenv func(string) string) (time.Duration, error) {
	value := strings.TrimSpace(getenv("JIRA_POLL_INTERVAL_SECONDS"))
	if value == "" {
		return defaultPollInterval, nil
	}
	seconds, err := strconv.Atoi(value)
	if err != nil || seconds <= 0 {
		return 0, fmt.Errorf("JIRA_POLL_INTERVAL_SECONDS must be a positive integer")
	}
	return time.Duration(seconds) * time.Second, nil
}
