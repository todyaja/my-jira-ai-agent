package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tody-aja/jira-ai-agent/internal/workflow"
)

// The technical-planner agent and its skill (plugin/) define how the code is
// explored and the TRD is written; the issue and PRD are sent on stdin.
const claudeTRDPrompt = `Write the TRD for this Jira issue. The repository to change is your current working directory: explore it before proposing anything.

Issue key: %s
Summary: %s
Description:
%s

Approved PRD (Confluence storage format):
%s`

const claudeTRDRevisionPrompt = `Revise the TRD for this Jira issue based on the reviewer feedback below. The repository is your current working directory. Change only what the feedback asks for and keep the rest of the current TRD as it is. Cross-check every comment against the current TRD first and skip any it already addresses. Read the code a comment concerns before changing the plan for it.

Issue key: %s
Summary: %s
Description:
%s

Approved PRD (Confluence storage format):
%s

Current TRD (Confluence storage format):
%s

Reviewer feedback, oldest first:
%s`

// plannerTools are read-only, and the CLI only lets them read inside the
// working directory, so the planner can study the repository but never
// change it.
const plannerTools = "Skill,Read,Grep,Glob"

// ClaudeTRDGenerator writes TRDs with the technical-planner agent, run
// inside the repository the issue is about.
type ClaudeTRDGenerator struct {
	cli      claudeCLI
	repoPath string
	timeout  time.Duration
}

// NewClaudeTRDGenerator plans changes to the repository at repoPath, which
// is resolved to an absolute path and checked on each run.
func NewClaudeTRDGenerator(config ClaudeConfig, repoPath string) (*ClaudeTRDGenerator, error) {
	cli, err := newClaudeCLI(config)
	if err != nil {
		return nil, err
	}
	if repoPath != "" {
		if repoPath, err = filepath.Abs(repoPath); err != nil {
			return nil, fmt.Errorf("resolve REPO_PATH: %w", err)
		}
	}
	return &ClaudeTRDGenerator{cli: cli, repoPath: repoPath, timeout: timeoutOrDefault(config.PlanningTimeout, DefaultClaudePlanningTimeout)}, nil
}

// CheckRepository reports why the repository cannot be planned against, or
// nil when it can.
func (g *ClaudeTRDGenerator) CheckRepository() error {
	if g.repoPath == "" {
		return fmt.Errorf("REPO_PATH is not configured")
	}
	info, err := os.Stat(g.repoPath)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("REPO_PATH %q is not a directory", g.repoPath)
	}
	return nil
}

func (g *ClaudeTRDGenerator) GenerateTRD(ctx context.Context, input workflow.TRDInput) (string, error) {
	if err := g.CheckRepository(); err != nil {
		return "", err
	}
	return g.cli.run(ctx, claudeRun{
		agent:   TechnicalPlannerAgent,
		tools:   plannerTools,
		dir:     g.repoPath,
		prompt:  trdPrompt(input),
		timeout: g.timeout,
	})
}

// trdPrompt asks for a new TRD, or for a revision when the issue has
// reviewer feedback.
func trdPrompt(input workflow.TRDInput) string {
	if len(input.Feedback) == 0 {
		return fmt.Sprintf(claudeTRDPrompt, input.IssueKey, input.Summary, input.Description, input.PRD)
	}
	currentTRD := input.CurrentTRD
	if strings.TrimSpace(currentTRD) == "" {
		currentTRD = "(none published - write the TRD from the PRD, the repository and the feedback)"
	}
	return fmt.Sprintf(claudeTRDRevisionPrompt, input.IssueKey, input.Summary, input.Description, input.PRD, currentTRD, workflow.FormatFeedback(input.Feedback))
}

var _ workflow.TRDGenerator = (*ClaudeTRDGenerator)(nil)
