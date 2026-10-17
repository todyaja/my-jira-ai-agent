package workflow

import (
	"context"
	"fmt"
	"strings"

	"github.com/tody-aja/jira-ai-agent/internal/jira"
)

// PullRequestOpener opens a pull request from branch into the base branch
// and returns its URL. It returns the open pull request for branch when
// there already is one.
type PullRequestOpener interface {
	OpenPullRequest(ctx context.Context, branch, title, body string) (string, error)
}

// ExecutePRReady pushes the issue's branch and opens a pull request for it,
// then links the pull request from the issue. The issue stays in PR Ready
// for a person to review and merge. An issue that already has a pull
// request is left alone, so a restarted server does not open another.
func ExecutePRReady(ctx context.Context, key string, deps Dependencies) error {
	logger := deps.Logger
	if deps.PullRequests == nil {
		if logger != nil {
			logger.Printf("GitHub is not configured, so no pull request was opened: issue=%s", key)
		}
		return nil
	}
	issue, err := deps.Reader.GetIssue(ctx, key)
	if err != nil {
		logOperationFailure(logger, "Pull request", key, "get issue", err, 0)
		return fmt.Errorf("get issue: %w", err)
	}
	state, err := loadImplementationState(ctx, deps.Reader, key)
	if err != nil {
		logOperationFailure(logger, "Pull request", key, "load state", err, 0)
		return err
	}
	if state.PullRequest != "" {
		if logger != nil {
			logger.Printf("Pull request already open: issue=%s url=%s", key, state.PullRequest)
		}
		return nil
	}

	if err := deps.Workspace.Push(ctx, state.Branch); err != nil {
		logOperationFailure(logger, "Pull request", key, "push", err, 0)
		return fmt.Errorf("push: %w", err)
	}
	title := fmt.Sprintf("%s: %s", issue.Key, issue.Summary)
	pullRequest, err := deps.PullRequests.OpenPullRequest(ctx, state.Branch, title, pullRequestBody(issue, state, deps))
	if err != nil {
		logOperationFailure(logger, "Pull request", key, "open", err, 0)
		return fmt.Errorf("open pull request: %w", err)
	}
	if logger != nil {
		logger.Printf("Pull request opened: issue=%s branch=%s url=%s", key, state.Branch, pullRequest)
	}

	state.PullRequest = pullRequest
	if err := saveImplementationState(ctx, deps.Writer, key, state); err != nil {
		logOperationFailure(logger, "Pull request", key, "save state", err, 0)
		return err
	}
	message := fmt.Sprintf("Pull request opened from branch %s: %s", state.Branch, pullRequest)
	if _, err := deps.Writer.AddComment(ctx, key, jira.TextADF(message)); err != nil {
		logOperationFailure(logger, "Pull request", key, "comment", err, 0)
		return fmt.Errorf("comment: %w", err)
	}
	return nil
}

// pullRequestBody links the issue and states how the AI review ended.
func pullRequestBody(issue jira.Issue, state implementationState, deps Dependencies) string {
	reference := issue.Key
	if base := strings.TrimRight(strings.TrimSpace(deps.JiraBaseURL), "/"); base != "" {
		reference = fmt.Sprintf("[%s](%s/browse/%s)", issue.Key, base, issue.Key)
	}
	lines := []string{fmt.Sprintf("Implements %s: %s", reference, issue.Summary), ""}

	if last := len(state.Reviews) - 1; last >= 0 {
		review := state.Reviews[last]
		if review.Approved {
			lines = append(lines, fmt.Sprintf("The AI reviewer approved this branch in round %d.", review.Round))
		} else {
			lines = append(lines, fmt.Sprintf("**The AI review rounds ran out with findings still open** (round %d). See the last review on the Jira issue before merging.", review.Round))
		}
		lines = append(lines, "")
	}
	lines = append(lines, "The PRD, TRD and every implementation and review round are on the Jira issue.")
	return strings.Join(lines, "\n")
}
