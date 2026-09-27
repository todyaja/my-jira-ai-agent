package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tody-aja/jira-ai-agent/internal/jira"
)

type pollSearcher struct {
	mu     sync.Mutex
	issues []jira.IssueStatus
	err    error
	jqls   []string
	calls  int
	// current overrides an issue's status for direct reads, simulating a
	// search index that lags behind.
	current map[string]string
}

func (s *pollSearcher) GetIssueStatus(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if status, ok := s.current[key]; ok {
		return status, nil
	}
	for _, issue := range s.issues {
		if issue.Key == key {
			return issue.Status, nil
		}
	}
	return "", errors.New("issue not found")
}

func (s *pollSearcher) setCurrent(key, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current == nil {
		s.current = map[string]string{}
	}
	s.current[key] = status
}

func (s *pollSearcher) SearchIssues(_ context.Context, jql string) ([]jira.IssueStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.jqls = append(s.jqls, jql)
	return s.issues, s.err
}

func (s *pollSearcher) set(issues ...jira.IssueStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.issues = issues
}

type pollReader struct{}

func (pollReader) GetIssue(_ context.Context, key string) (jira.Issue, error) {
	return jira.Issue{Key: key, DescriptionADF: json.RawMessage(`{"type":"doc","version":1,"content":[]}`)}, nil
}

func (pollReader) GetComments(context.Context, string) ([]jira.Comment, error) {
	return nil, nil
}

func (pollReader) GetIssueProperty(context.Context, string, string) (json.RawMessage, bool, error) {
	return nil, false, nil
}

type pollWriter struct {
	mu            sync.Mutex
	transitions   []string
	transitionErr map[string]error
}

func (w *pollWriter) UpdateDescription(context.Context, string, json.RawMessage) error { return nil }

func (w *pollWriter) AddComment(context.Context, string, json.RawMessage) (string, error) {
	return "1", nil
}

func (w *pollWriter) SetIssueProperty(context.Context, string, string, json.RawMessage) error {
	return nil
}

func (w *pollWriter) SetCommentProperty(context.Context, string, string, json.RawMessage) error {
	return nil
}

func (w *pollWriter) TransitionTo(_ context.Context, key, status string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.transitionErr[status]; err != nil {
		return err
	}
	w.transitions = append(w.transitions, key+" -> "+status)
	return nil
}

func (w *pollWriter) moves() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.Join(w.transitions, ", ")
}

type pollPublisher struct{}

func (pollPublisher) PublishPage(context.Context, string, string) (string, error) {
	return "https://example.atlassian.net/wiki/spaces/ENG/pages/1", nil
}

func (pollPublisher) PageContent(context.Context, string) (string, bool, error) {
	return "", false, nil
}

type blockingGenerator struct {
	mu      sync.Mutex
	calls   []string
	err     error
	started chan struct{}
	release chan struct{}
}

func (g *blockingGenerator) Generate(_ context.Context, input PRDInput) (string, error) {
	g.mu.Lock()
	g.calls = append(g.calls, input.IssueKey)
	g.mu.Unlock()
	if g.started != nil {
		g.started <- struct{}{}
	}
	if g.release != nil {
		<-g.release
	}
	return "generated PRD", g.err
}

func (g *blockingGenerator) callCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.calls)
}

type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func newTestPoller(searcher *pollSearcher, generator *blockingGenerator, logs *lockedBuffer) *Poller {
	return newTestPollerWithWriter(searcher, generator, &pollWriter{}, logs)
}

func newTestPollerWithWriter(searcher *pollSearcher, generator *blockingGenerator, writer *pollWriter, logs *lockedBuffer) *Poller {
	return &Poller{
		JQL:      "jql",
		Searcher: searcher,
		Dependencies: Dependencies{
			Reader:    pollReader{},
			Writer:    writer,
			Generator: generator,
			Publisher: pollPublisher{},
			Logger:    log.New(logs, "", 0),
		},
	}
}

func TestAssignedIssuesJQL(t *testing.T) {
	tests := []struct {
		name       string
		accountID  string
		projectKey string
		want       string
	}{
		{
			name:      "assignee only",
			accountID: "account-1",
			want:      `assignee = "account-1" ORDER BY created ASC`,
		},
		{
			name:       "restricted to project",
			accountID:  "account-1",
			projectKey: "DEMO",
			want:       `project = "DEMO" AND assignee = "account-1" ORDER BY created ASC`,
		},
		{
			name:      "escapes quotes",
			accountID: `a"b\c`,
			want:      `assignee = "a\"b\\c" ORDER BY created ASC`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AssignedIssuesJQL(tt.accountID, tt.projectKey); got != tt.want {
				t.Fatalf("AssignedIssuesJQL() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPollerDispatchesEveryIssueByStatus(t *testing.T) {
	searcher := &pollSearcher{}
	searcher.set(
		jira.IssueStatus{Key: "DEMO-1", Status: "PRD Requested"},
		jira.IssueStatus{Key: "DEMO-2", Status: "Plan Review"},
		jira.IssueStatus{Key: "DEMO-3", Status: "Done"},
	)
	generator := &blockingGenerator{}
	var logs lockedBuffer
	poller := newTestPoller(searcher, generator, &logs)

	poller.Poll(context.Background())
	poller.wg.Wait()

	if generator.callCount() != 1 || generator.calls[0] != "DEMO-1" {
		t.Fatalf("generate calls = %v, want only the PRD Requested issue", generator.calls)
	}
	for _, want := range []string{
		"Dispatching DEMO-1: status=PRD REQUESTED",
		"Workflow action succeeded: issue=DEMO-1 status=PRD REQUESTED",
		"No AI action implemented for status PLAN REVIEW: issue=DEMO-2",
		"No AI action implemented for status DONE: issue=DEMO-3",
	} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("log = %q, missing %q", logs.String(), want)
		}
	}
	if searcher.jqls[0] != "jql" {
		t.Fatalf("jql = %q, want configured JQL", searcher.jqls[0])
	}
}

func TestPollerDispatchesOnlyWhenStatusChanges(t *testing.T) {
	searcher := &pollSearcher{}
	searcher.set(jira.IssueStatus{Key: "DEMO-1", Status: "Plan Review"})
	var logs lockedBuffer
	poller := newTestPoller(searcher, &blockingGenerator{}, &logs)

	poller.Poll(context.Background())
	poller.wg.Wait()
	poller.Poll(context.Background())
	poller.wg.Wait()
	if got := strings.Count(logs.String(), "status PLAN REVIEW"); got != 1 {
		t.Fatalf("PLAN REVIEW dispatched %d times, want 1 for an unchanged status; log = %q", got, logs.String())
	}

	searcher.set(jira.IssueStatus{Key: "DEMO-1", Status: "Plan Review"})
	poller.Poll(context.Background())
	poller.wg.Wait()
	if !strings.Contains(logs.String(), "No AI action implemented for status PLAN REVIEW: issue=DEMO-1") {
		t.Fatalf("log = %q, want dispatch after status change", logs.String())
	}
}

func TestPollerMovesFailedActionToAIError(t *testing.T) {
	searcher := &pollSearcher{}
	searcher.set(jira.IssueStatus{Key: "DEMO-1", Status: "PRD Requested"})
	generator := &blockingGenerator{err: errors.New("generation failed")}
	writer := &pollWriter{}
	var logs lockedBuffer
	poller := newTestPollerWithWriter(searcher, generator, writer, &logs)

	poller.Poll(context.Background())
	poller.wg.Wait()
	if writer.moves() != "DEMO-1 -> AI ERROR" {
		t.Fatalf("transitions = %q, want move to AI ERROR", writer.moves())
	}
	for _, want := range []string{
		"Workflow action failed: issue=DEMO-1 status=PRD REQUESTED error=generate PRD: generation failed",
		"Moved to AI ERROR after failure: issue=DEMO-1 status=PRD REQUESTED",
	} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("log = %q, missing %q", logs.String(), want)
		}
	}

	// Jira reports the issue in AI ERROR: it is dispatched to the no-op, not retried.
	searcher.set(jira.IssueStatus{Key: "DEMO-1", Status: "AI ERROR"})
	poller.Poll(context.Background())
	poller.wg.Wait()
	if generator.callCount() != 1 {
		t.Fatalf("generate calls = %d, want no retry from AI ERROR", generator.callCount())
	}
	if !strings.Contains(logs.String(), "No AI action implemented for status AI ERROR: issue=DEMO-1") {
		t.Fatalf("log = %q, want AI ERROR dispatched", logs.String())
	}

	// Moving the issue back to PRD Requested retries it.
	generator.err = nil
	searcher.set(jira.IssueStatus{Key: "DEMO-1", Status: "PRD Requested"})
	poller.Poll(context.Background())
	poller.wg.Wait()
	if generator.callCount() != 2 || !strings.Contains(writer.moves(), "DEMO-1 -> PRD REVIEW") {
		t.Fatalf("generate calls = %d, transitions = %q, want successful retry", generator.callCount(), writer.moves())
	}
}

func TestPollerRetriesWhenMoveToAIErrorFails(t *testing.T) {
	searcher := &pollSearcher{}
	searcher.set(jira.IssueStatus{Key: "DEMO-1", Status: "PRD Requested"})
	generator := &blockingGenerator{err: errors.New("generation failed")}
	writer := &pollWriter{transitionErr: map[string]error{"AI ERROR": errors.New(`transition "AI ERROR" not found`)}}
	var logs lockedBuffer
	poller := newTestPollerWithWriter(searcher, generator, writer, &logs)

	poller.Poll(context.Background())
	poller.wg.Wait()
	if !strings.Contains(logs.String(), "Move to AI ERROR failed, will retry: issue=DEMO-1") {
		t.Fatalf("log = %q, want failed move logged", logs.String())
	}

	poller.Poll(context.Background())
	poller.wg.Wait()
	if generator.callCount() != 2 {
		t.Fatalf("generate calls = %d, want retry when the issue could not be parked", generator.callCount())
	}
}

func TestPollerLeavesActionsInterruptedByShutdownInPlace(t *testing.T) {
	searcher := &pollSearcher{}
	searcher.set(jira.IssueStatus{Key: "DEMO-1", Status: "PRD Requested"})
	generator := &blockingGenerator{err: context.Canceled, started: make(chan struct{}, 1), release: make(chan struct{})}
	writer := &pollWriter{}
	var logs lockedBuffer
	poller := newTestPollerWithWriter(searcher, generator, writer, &logs)
	ctx, cancel := context.WithCancel(context.Background())

	poller.Poll(ctx)
	<-generator.started
	cancel()
	close(generator.release)
	poller.wg.Wait()

	if writer.moves() != "" {
		t.Fatalf("transitions = %q, want issue left in place for restart", writer.moves())
	}
}

func TestPollerSkipsIssuesAlreadyInFlight(t *testing.T) {
	searcher := &pollSearcher{}
	searcher.set(jira.IssueStatus{Key: "DEMO-1", Status: "PRD Requested"})
	generator := &blockingGenerator{started: make(chan struct{}, 1), release: make(chan struct{})}
	var logs lockedBuffer
	poller := newTestPoller(searcher, generator, &logs)

	poller.Poll(context.Background())
	select {
	case <-generator.started:
	case <-time.After(time.Second):
		t.Fatal("workflow did not start")
	}
	// The issue moved on while its PRD is still being generated.
	searcher.set(jira.IssueStatus{Key: "DEMO-1", Status: "Plan Review"})
	poller.Poll(context.Background())
	close(generator.release)
	poller.wg.Wait()

	if strings.Contains(logs.String(), "status PLAN REVIEW") {
		t.Fatalf("log = %q, want no dispatch while in flight", logs.String())
	}
	poller.Poll(context.Background())
	poller.wg.Wait()
	if !strings.Contains(logs.String(), "No AI action implemented for status PLAN REVIEW: issue=DEMO-1") {
		t.Fatalf("log = %q, want status change dispatched once the action finished", logs.String())
	}
}

func TestPollerLogsUnknownStatusOnce(t *testing.T) {
	searcher := &pollSearcher{}
	searcher.set(jira.IssueStatus{Key: "DEMO-1", Status: "In Progress"})
	var logs lockedBuffer
	poller := newTestPoller(searcher, &blockingGenerator{}, &logs)

	poller.Poll(context.Background())
	poller.Poll(context.Background())
	poller.wg.Wait()

	if got := strings.Count(logs.String(), `No workflow status matches "In Progress": issue=DEMO-1`); got != 1 {
		t.Fatalf("unknown status logged %d times, want 1; log = %q", got, logs.String())
	}
}

func TestPollerLogsSearchFailure(t *testing.T) {
	searcher := &pollSearcher{err: errors.New("POST search: Jira returned HTTP 401")}
	generator := &blockingGenerator{}
	var logs lockedBuffer
	poller := newTestPoller(searcher, generator, &logs)

	poller.Poll(context.Background())

	if !strings.Contains(logs.String(), "Jira poll failed: error=POST search: Jira returned HTTP 401") {
		t.Fatalf("log = %q, want search failure", logs.String())
	}
	if generator.callCount() != 0 {
		t.Fatal("search failure started a workflow")
	}
}

func TestPollerRunPollsUntilCanceled(t *testing.T) {
	searcher := &pollSearcher{}
	var logs lockedBuffer
	poller := newTestPoller(searcher, &blockingGenerator{}, &logs)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		poller.Run(ctx, 10*time.Millisecond)
		close(done)
	}()
	deadline := time.After(time.Second)
	for {
		searcher.mu.Lock()
		calls := searcher.calls
		searcher.mu.Unlock()
		if calls >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("search calls = %d, want repeated polling", calls)
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return after cancel")
	}
}

// The action moves the issue on, but search still shows the old status and
// a reviewer moves it back before any poll sees the new one.
func TestPollerNoticesRoundTripHiddenBySearchLag(t *testing.T) {
	searcher := &pollSearcher{}
	searcher.set(jira.IssueStatus{Key: "DEMO-1", Status: "PRD Requested"})
	generator := &blockingGenerator{started: make(chan struct{}, 1), release: make(chan struct{})}
	var logs lockedBuffer
	poller := newTestPoller(searcher, generator, &logs)

	poller.Poll(context.Background())
	<-generator.started
	// The action moves the issue to PRD Review; direct reads see it at once,
	// search keeps showing PRD Requested.
	searcher.setCurrent("DEMO-1", "PRD Review")
	close(generator.release)
	poller.wg.Wait()

	poller.Poll(context.Background())
	poller.wg.Wait()
	if generator.callCount() != 1 {
		t.Fatalf("generate calls = %d, want no re-run from a stale search result", generator.callCount())
	}

	// The reviewer moves it back to PRD Requested before search catches up.
	generator.started, generator.release = nil, nil
	searcher.setCurrent("DEMO-1", "PRD Requested")
	poller.Poll(context.Background())
	poller.wg.Wait()
	if generator.callCount() != 2 {
		t.Fatalf("generate calls = %d, want the round trip noticed; log = %q", generator.callCount(), logs.String())
	}
}

func TestPollerRunsChainedActions(t *testing.T) {
	searcher := &pollSearcher{}
	searcher.set(jira.IssueStatus{Key: "DEMO-1", Status: "PRD Requested"})
	generator := &blockingGenerator{}
	var logs lockedBuffer
	poller := newTestPoller(searcher, generator, &logs)

	poller.Poll(context.Background())
	poller.wg.Wait()
	// The next step also has an AI action and must be dispatched.
	searcher.set(jira.IssueStatus{Key: "DEMO-1", Status: "PRD Requested"})
	searcher.setCurrent("DEMO-1", "Planning")
	poller.Poll(context.Background())
	poller.wg.Wait()
	if !strings.Contains(logs.String(), "Dispatching DEMO-1: status=PLANNING") {
		t.Fatalf("log = %q, want the next step dispatched", logs.String())
	}
}
