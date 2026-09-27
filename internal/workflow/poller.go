package workflow

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/tody-aja/jira-ai-agent/internal/jira"
)

// IssueSearcher finds issues and their statuses. Search results can lag
// behind recent changes, so GetIssueStatus reads one issue's current status
// directly.
type IssueSearcher interface {
	SearchIssues(context.Context, string) ([]jira.IssueStatus, error)
	GetIssueStatus(ctx context.Context, key string) (string, error)
}

// AssignedIssuesJQL selects every issue assigned to accountID, optionally
// restricted to a single project.
func AssignedIssuesJQL(accountID, projectKey string) string {
	jql := fmt.Sprintf("assignee = %s", quoteJQL(accountID))
	if strings.TrimSpace(projectKey) != "" {
		jql = fmt.Sprintf("project = %s AND %s", quoteJQL(projectKey), jql)
	}
	return jql + " ORDER BY created ASC"
}

func quoteJQL(value string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value)
	return `"` + escaped + `"`
}

// Poller periodically searches Jira and dispatches each issue whose status
// changed since it was last handled. Every issue is dispatched once on the
// first poll. An issue whose action fails is moved to AI ERROR, and an issue
// is never processed twice concurrently.
//
// Because search results can lag, a changed status is confirmed with a
// direct read before dispatching. After an action that moves the issue on,
// its status is forgotten, so the next poll checks the issue again: a
// reviewer who moves it straight back is noticed, and a stale search result
// cannot re-run the action because the direct read disagrees with it.
type Poller struct {
	JQL          string
	Searcher     IssueSearcher
	Dependencies Dependencies

	mu         sync.Mutex
	inFlight   map[string]bool
	lastStatus map[string]string
	wg         sync.WaitGroup
}

// Run polls immediately and then every interval until ctx is canceled, then
// waits for in-flight actions to finish.
func (p *Poller) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	p.Poll(ctx)
	for {
		select {
		case <-ctx.Done():
			p.wg.Wait()
			return
		case <-ticker.C:
			p.Poll(ctx)
		}
	}
}

// Poll runs one search and starts an action for every issue whose status
// changed and that is not already being processed.
func (p *Poller) Poll(ctx context.Context) {
	logger := p.Dependencies.Logger
	issues, err := p.Searcher.SearchIssues(ctx, p.JQL)
	if err != nil {
		if ctx.Err() == nil {
			logger.Printf("Jira poll failed: error=%v", err)
		}
		return
	}

	for _, issue := range issues {
		if !p.changed(issue.Key, issue.Status) {
			continue
		}
		current, err := p.Searcher.GetIssueStatus(ctx, issue.Key)
		if err != nil {
			if ctx.Err() == nil {
				logger.Printf("Jira status check failed: issue=%s error=%v", issue.Key, err)
			}
			continue
		}
		if !p.claim(issue.Key, current) {
			continue
		}
		status, ok := ParseStatus(current)
		if !ok {
			logger.Printf("No workflow status matches %q: issue=%s", current, issue.Key)
			p.release(issue.Key, true)
			continue
		}

		logger.Printf("Dispatching %s: status=%s", issue.Key, status)
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			err := Dispatch(ctx, status, issue.Key, p.Dependencies)
			if err == nil {
				logger.Printf("Workflow action succeeded: issue=%s status=%s", issue.Key, status)
				// An AI action moves the issue on; a no-op leaves it where it is.
				p.release(issue.Key, !HasAIAction(status))
				return
			}
			logger.Printf("Workflow action failed: issue=%s status=%s error=%v", issue.Key, status, err)
			p.moveToAIError(ctx, issue.Key, status)
			// Moved to AI ERROR: the next poll sees that. Not moved: retry.
			p.release(issue.Key, false)
		}()
	}
}

// moveToAIError parks an issue whose action failed in AI ERROR so it is not
// retried until someone moves it back. It reports whether the issue was
// moved; an issue that was not moved is retried on the next poll. Actions
// interrupted by shutdown are left in place so they resume on restart.
func (p *Poller) moveToAIError(ctx context.Context, key string, status Status) bool {
	logger := p.Dependencies.Logger
	if ctx.Err() != nil || status == StatusAIError {
		return false
	}
	if err := p.Dependencies.Writer.TransitionTo(ctx, key, string(StatusAIError)); err != nil {
		logger.Printf("Move to %s failed, will retry: issue=%s error=%v", StatusAIError, key, err)
		return false
	}
	logger.Printf("Moved to %s after failure: issue=%s status=%s", StatusAIError, key, status)
	return true
}

// changed reports whether an issue may need handling: no action for it is
// running and status differs from the last one handled.
func (p *Poller) changed(key, status string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.inFlight[key] {
		return false
	}
	last, seen := p.lastStatus[key]
	return !seen || last != status
}

// claim reserves an issue for processing when its status differs from the
// last one handled and no action for it is running.
func (p *Poller) claim(key, status string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.inFlight == nil {
		p.inFlight = map[string]bool{}
		p.lastStatus = map[string]string{}
	}
	if p.inFlight[key] {
		return false
	}
	if last, seen := p.lastStatus[key]; seen && last == status {
		return false
	}
	p.inFlight[key] = true
	p.lastStatus[key] = status
	return true
}

// release ends processing. Unless remember is set, the issue's status is
// forgotten so the next poll checks it again.
func (p *Poller) release(key string, remember bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.inFlight, key)
	if !remember {
		delete(p.lastStatus, key)
	}
}
