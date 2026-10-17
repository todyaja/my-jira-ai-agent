package workflow

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tody-aja/jira-ai-agent/internal/jira"
)

type pullRequestFake struct {
	order  *[]string
	url    string
	err    error
	branch string
	title  string
	body   string
}

func (f *pullRequestFake) OpenPullRequest(_ context.Context, branch, title, body string) (string, error) {
	*f.order = append(*f.order, "open pull request")
	f.branch, f.title, f.body = branch, title, body
	return f.url, f.err
}

func newPullRequestFixture(state string) (*implementationFixture, *pullRequestFake, Dependencies) {
	f := newImplementationFixture(state)
	opener := &pullRequestFake{order: &f.order, url: "https://github.com/o/r/pull/7"}
	deps := f.deps(3)
	deps.PullRequests = opener
	deps.JiraBaseURL = "https://example.atlassian.net/"
	return f, opener, deps
}

func TestPRReadyPushesBranchOpensPullRequestAndLinksIt(t *testing.T) {
	f, opener, deps := newPullRequestFixture(`{"branch":"feature/ABC-1","reviews":[{"round":2,"approved":true,"commentId":"r2"}]}`)

	if err := Dispatch(context.Background(), StatusPRReady, "ABC-1", deps); err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if got, want := f.order, []string{"get issue", "get state", "push", "open pull request", "save state", "comment"}; !equalStrings(got, want) {
		t.Fatalf("operation order = %v, want %v", got, want)
	}
	if f.workspace.pushed != "feature/ABC-1" || opener.branch != "feature/ABC-1" || opener.title != "ABC-1: Build a feature" {
		t.Fatalf("pushed %q, opened %q titled %q; want feature/ABC-1 titled with the issue", f.workspace.pushed, opener.branch, opener.title)
	}
	if !strings.Contains(opener.body, "[ABC-1](https://example.atlassian.net/browse/ABC-1)") || !strings.Contains(opener.body, "approved this branch in round 2") {
		t.Fatalf("body = %q, want issue link and review outcome", opener.body)
	}
	if state := f.savedState(t); state.PullRequest != opener.url || len(state.Reviews) != 1 {
		t.Fatalf("state = %+v, want pull request recorded and reviews kept", state)
	}
	if comment, _ := jira.DescriptionText(f.writer.comments[0]); !strings.Contains(comment, opener.url) {
		t.Fatalf("comment = %q, want the pull request link", comment)
	}
	if f.writer.status != "" {
		t.Fatalf("status = %q, want the issue left in PR Ready", f.writer.status)
	}
}

func TestPRReadyFlagsOpenFindings(t *testing.T) {
	_, opener, deps := newPullRequestFixture(`{"branch":"feature/ABC-1","reviews":[{"round":3,"approved":false,"commentId":"r3"}]}`)

	if err := Dispatch(context.Background(), StatusPRReady, "ABC-1", deps); err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if !strings.Contains(opener.body, "findings still open") {
		t.Fatalf("body = %q, want open findings called out", opener.body)
	}
}

func TestPRReadySkipsIssueWithPullRequest(t *testing.T) {
	f, _, deps := newPullRequestFixture(`{"branch":"feature/ABC-1","reviews":[],"pullRequest":"https://github.com/o/r/pull/1"}`)

	if err := Dispatch(context.Background(), StatusPRReady, "ABC-1", deps); err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if got, want := f.order, []string{"get issue", "get state"}; !equalStrings(got, want) {
		t.Fatalf("operation order = %v, want nothing pushed or opened", got)
	}
}

func TestPRReadyWithoutGitHubDoesNothing(t *testing.T) {
	f := newImplementationFixture(`{"branch":"feature/ABC-1","reviews":[]}`)

	if err := Dispatch(context.Background(), StatusPRReady, "ABC-1", f.deps(3)); err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if len(f.order) != 0 {
		t.Fatalf("operation order = %v, want nothing done", f.order)
	}
}

func TestPRReadyPushFailureOpensNothing(t *testing.T) {
	f, _, deps := newPullRequestFixture(`{"branch":"feature/ABC-1","reviews":[]}`)
	f.workspace.pushErr = errors.New("authentication failed")

	err := Dispatch(context.Background(), StatusPRReady, "ABC-1", deps)
	if err == nil || !strings.Contains(err.Error(), "push: authentication failed") {
		t.Fatalf("Dispatch() error = %v, want the push failure", err)
	}
	if got, want := f.order, []string{"get issue", "get state", "push"}; !equalStrings(got, want) {
		t.Fatalf("operation order = %v, want %v", got, want)
	}
}
