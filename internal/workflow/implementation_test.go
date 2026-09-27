package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"testing"

	"github.com/tody-aja/jira-ai-agent/internal/jira"
)

type workspaceFake struct {
	order     *[]string
	committed bool
	diff      string
	branch    string
	message   string
}

func (f *workspaceFake) Prepare(_ context.Context, branch string) (string, error) {
	*f.order = append(*f.order, "prepare")
	f.branch = branch
	return "/worktrees/" + branch, nil
}

func (f *workspaceFake) CommitAll(_ context.Context, _, message string) (bool, error) {
	*f.order = append(*f.order, "commit")
	f.message = message
	return f.committed, nil
}

func (f *workspaceFake) Diff(context.Context, string) (string, error) {
	*f.order = append(*f.order, "diff")
	return f.diff, nil
}

type engineerFake struct {
	order   *[]string
	summary string
	err     error
	input   ImplementInput
}

func (f *engineerFake) Implement(_ context.Context, input ImplementInput) (string, error) {
	*f.order = append(*f.order, "implement")
	f.input = input
	return f.summary, f.err
}

type reviewerFake struct {
	order  *[]string
	result ReviewResult
	input  ReviewInput
}

func (f *reviewerFake) Review(_ context.Context, input ReviewInput) (ReviewResult, error) {
	*f.order = append(*f.order, "review")
	f.input = input
	return f.result, nil
}

type implementationFixture struct {
	order     []string
	reader    *issueReaderFake
	writer    *issueWriterFake
	workspace *workspaceFake
	engineer  *engineerFake
	reviewer  *reviewerFake
	publisher *pagePublisherFake
}

// newImplementationFixture returns an issue with a published PRD and TRD,
// and the given implementation state and comments.
func newImplementationFixture(state string, comments ...jira.Comment) *implementationFixture {
	f := &implementationFixture{}
	f.reader = &issueReaderFake{issue: planningIssue(), comments: comments, order: &f.order}
	if state != "" {
		f.reader.properties = map[string]json.RawMessage{ImplementationProperty: json.RawMessage(state)}
	}
	f.writer = &issueWriterFake{order: &f.order}
	f.workspace = &workspaceFake{order: &f.order, committed: true, diff: "app.ts | 2 +-"}
	f.engineer = &engineerFake{order: &f.order, summary: "## Changes\n- app.ts"}
	f.reviewer = &reviewerFake{order: &f.order}
	f.publisher = &pagePublisherFake{order: &f.order, pages: map[string]string{testPRDTitle: "<p>PRD</p>", testTRDTitle: "<p>TRD</p>"}}
	return f
}

func (f *implementationFixture) deps(maxReviews int) Dependencies {
	return Dependencies{
		Reader:          f.reader,
		Writer:          f.writer,
		Publisher:       f.publisher,
		Engineer:        f.engineer,
		Reviewer:        f.reviewer,
		Workspace:       f.workspace,
		MaxReviewCycles: maxReviews,
		Logger:          log.New(&bytes.Buffer{}, "", 0),
	}
}

func (f *implementationFixture) savedState(t *testing.T) implementationState {
	t.Helper()
	var state implementationState
	if err := json.Unmarshal(f.writer.issueProperties[ImplementationProperty], &state); err != nil {
		t.Fatalf("decode saved state: %v", err)
	}
	return state
}

func TestReadyToImplementStartsCycleAndMovesToImplementing(t *testing.T) {
	f := newImplementationFixture("")

	if err := Dispatch(context.Background(), StatusReadyToImplement, "ABC-1", f.deps(3)); err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if got, want := f.order, []string{"save state", "comment", "transition"}; !equalStrings(got, want) {
		t.Fatalf("operation order = %v, want %v", got, want)
	}
	if state := f.savedState(t); state.Branch != "ai/ABC-1" || len(state.Reviews) != 0 {
		t.Fatalf("state = %+v, want a fresh cycle on ai/ABC-1", state)
	}
	if !strings.Contains(string(f.writer.comments[0]), "up to 3 review rounds") || f.writer.status != "IMPLEMENTING" {
		t.Fatalf("comment = %s, status = %q, want start comment and IMPLEMENTING", f.writer.comments[0], f.writer.status)
	}
}

func TestImplementingFirstRoundImplementsTRDAndMovesToAIReview(t *testing.T) {
	f := newImplementationFixture(`{"branch":"ai/ABC-1","reviews":[]}`)

	if err := Dispatch(context.Background(), StatusImplementing, "ABC-1", f.deps(3)); err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if got, want := f.order, []string{"get issue", "get page", "get page", "get state", "prepare", "implement", "commit", "comment", "transition"}; !equalStrings(got, want) {
		t.Fatalf("operation order = %v, want %v", got, want)
	}
	input := f.engineer.input
	if input.PRD != "<p>PRD</p>" || input.TRD != "<p>TRD</p>" || input.Dir != "/worktrees/ai/ABC-1" || input.Branch != "ai/ABC-1" || input.Review != "" {
		t.Fatalf("engineer input = %+v, want documents, worktree and no review", input)
	}
	if f.workspace.message != "ABC-1: implement Build a feature" {
		t.Fatalf("commit message = %q", f.workspace.message)
	}
	comment, _ := jira.DescriptionText(f.writer.comments[0])
	if !strings.Contains(comment, "Implementation round 1") || !strings.Contains(comment, "Committed to branch ai/ABC-1") || !strings.Contains(comment, "app.ts") {
		t.Fatalf("comment = %q, want round header and summary", comment)
	}
	if f.writer.status != "AI REVIEW" {
		t.Fatalf("status = %q, want AI REVIEW", f.writer.status)
	}
}

func TestImplementingAddressesLatestRequestedChanges(t *testing.T) {
	f := newImplementationFixture(`{"branch":"ai/ABC-1","reviews":[{"round":1,"approved":false,"commentId":"r1"}]}`,
		jira.Comment{ID: "r1", Body: "Blocking: missing test for empty title"},
	)

	if err := Dispatch(context.Background(), StatusImplementing, "ABC-1", f.deps(3)); err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if f.engineer.input.Review != "Blocking: missing test for empty title" {
		t.Fatalf("engineer review = %q, want latest review text", f.engineer.input.Review)
	}
	if f.workspace.message != "ABC-1: address AI review round 1" {
		t.Fatalf("commit message = %q", f.workspace.message)
	}
	if comment, _ := jira.DescriptionText(f.writer.comments[0]); !strings.Contains(comment, "Implementation round 2") {
		t.Fatalf("comment = %q, want round 2", comment)
	}
}

func TestImplementingWithoutTRDStopsBeforeCoding(t *testing.T) {
	f := newImplementationFixture("")
	delete(f.publisher.pages, testTRDTitle)

	err := Dispatch(context.Background(), StatusImplementing, "ABC-1", f.deps(3))

	if err == nil || !strings.Contains(err.Error(), `no page titled "ABC-1: Build a feature TRD"`) {
		t.Fatalf("error = %v, want missing TRD", err)
	}
	if strings.Contains(strings.Join(f.order, ","), "implement") {
		t.Fatalf("operations = %v, want no implementation", f.order)
	}
}

func TestImplementingEngineerFailureStopsBeforeCommit(t *testing.T) {
	f := newImplementationFixture("")
	f.engineer.err = errors.New("engineer failed")

	err := Dispatch(context.Background(), StatusImplementing, "ABC-1", f.deps(3))

	if !errors.Is(err, f.engineer.err) {
		t.Fatalf("error = %v, want engineer error", err)
	}
	if order := strings.Join(f.order, ","); strings.Contains(order, "commit") || strings.Contains(order, "transition") {
		t.Fatalf("operations = %v, want stop after implement", f.order)
	}
}

func TestAIReviewRequestingChangesReturnsToImplementing(t *testing.T) {
	f := newImplementationFixture(`{"branch":"ai/ABC-1","reviews":[]}`)
	f.reviewer.result = ReviewResult{Review: "## Blocking findings\n1. Missing test"}

	if err := Dispatch(context.Background(), StatusAIReview, "ABC-1", f.deps(3)); err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if got, want := f.order, []string{"get issue", "get page", "get page", "get state", "prepare", "diff", "review", "comment", "save state", "transition"}; !equalStrings(got, want) {
		t.Fatalf("operation order = %v, want %v", got, want)
	}
	input := f.reviewer.input
	if input.Round != 1 || input.MaxRounds != 3 || input.Diff != "app.ts | 2 +-" || len(input.PreviousReviews) != 0 || input.TRD != "<p>TRD</p>" {
		t.Fatalf("reviewer input = %+v, want round 1 of 3 with diff and TRD", input)
	}
	state := f.savedState(t)
	if len(state.Reviews) != 1 || state.Reviews[0] != (reviewRecord{Round: 1, Approved: false, CommentID: "c1"}) {
		t.Fatalf("state = %+v, want review 1 recorded with its comment", state)
	}
	comment, _ := jira.DescriptionText(f.writer.comments[0])
	if !strings.Contains(comment, "AI review round 1 of 3") || !strings.Contains(comment, "engineer will address") || !strings.Contains(comment, "Missing test") {
		t.Fatalf("comment = %q, want round header, outcome and review", comment)
	}
	if f.writer.status != "IMPLEMENTING" {
		t.Fatalf("status = %q, want IMPLEMENTING", f.writer.status)
	}
}

func TestAIReviewApprovalMovesToPRReady(t *testing.T) {
	f := newImplementationFixture(`{"branch":"ai/ABC-1","reviews":[{"round":1,"approved":false,"commentId":"r1"}]}`,
		jira.Comment{ID: "r1", Body: "Round 1 review"},
	)
	f.reviewer.result = ReviewResult{Approved: true, Review: "All good"}

	if err := Dispatch(context.Background(), StatusAIReview, "ABC-1", f.deps(3)); err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if input := f.reviewer.input; input.Round != 2 || len(input.PreviousReviews) != 1 || input.PreviousReviews[0] != "Round 1 review" {
		t.Fatalf("reviewer input = %+v, want round 2 with the earlier review", input)
	}
	if f.writer.status != "PR READY" {
		t.Fatalf("status = %q, want PR READY", f.writer.status)
	}
	if state := f.savedState(t); len(state.Reviews) != 2 || !state.Reviews[1].Approved {
		t.Fatalf("state = %+v, want approved round 2", state)
	}
}

func TestAIReviewLastRoundMovesToPRReadyWithOpenFindings(t *testing.T) {
	f := newImplementationFixture(`{"branch":"ai/ABC-1","reviews":[{"round":1,"approved":false,"commentId":"r1"},{"round":2,"approved":false,"commentId":"r2"}]}`,
		jira.Comment{ID: "r1", Body: "one"}, jira.Comment{ID: "r2", Body: "two"},
	)
	f.reviewer.result = ReviewResult{Review: "Still a blocking bug"}

	if err := Dispatch(context.Background(), StatusAIReview, "ABC-1", f.deps(3)); err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if f.writer.status != "PR READY" {
		t.Fatalf("status = %q, want PR READY after the last round", f.writer.status)
	}
	if comment, _ := jira.DescriptionText(f.writer.comments[0]); !strings.Contains(comment, "AI review round 3 of 3") || !strings.Contains(comment, "last review round") {
		t.Fatalf("comment = %q, want last-round outcome", comment)
	}
}

func TestAIReviewDefaultsToThreeRounds(t *testing.T) {
	f := newImplementationFixture("")
	f.reviewer.result = ReviewResult{Review: "r"}

	if err := Dispatch(context.Background(), StatusAIReview, "ABC-1", f.deps(0)); err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if f.reviewer.input.MaxRounds != DefaultMaxReviewCycles {
		t.Fatalf("MaxRounds = %d, want default %d", f.reviewer.input.MaxRounds, DefaultMaxReviewCycles)
	}
}
