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

type trdGeneratorFake struct {
	trd   string
	err   error
	order *[]string
	input TRDInput
}

func (f *trdGeneratorFake) GenerateTRD(_ context.Context, input TRDInput) (string, error) {
	*f.order = append(*f.order, "generate TRD")
	f.input = input
	return f.trd, f.err
}

const testTRDURL = "https://example.atlassian.net/wiki/spaces/ENG/pages/2"

const (
	testPRDTitle = "ABC-1: Build a feature PRD"
	testTRDTitle = "ABC-1: Build a feature TRD"
)

func prdOnly() map[string]string {
	return map[string]string{testPRDTitle: "<h2>Problem</h2>"}
}

func planningIssue() jira.Issue {
	return jira.Issue{
		Key:            "ABC-1",
		Summary:        "Build a feature",
		Description:    "Human description\nPRD\n=============\n" + testPageURL,
		DescriptionADF: json.RawMessage(`{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"Human description"}]},{"type":"paragraph","content":[{"type":"text","text":"PRD"}]},{"type":"paragraph","content":[{"type":"text","text":"============="}]},{"type":"paragraph","content":[{"type":"text","text":"` + testPageURL + `"}]}]}`),
	}
}

func TestExecutePlanningWritesTRDFromPRDAndMovesToPlanReview(t *testing.T) {
	order := []string{}
	reader := &issueReaderFake{issue: planningIssue(), order: &order}
	publisher := &pagePublisherFake{url: testTRDURL, order: &order, pages: prdOnly()}
	planner := &trdGeneratorFake{trd: "## Summary\nPlan", order: &order}
	writer := &issueWriterFake{order: &order}
	var logs bytes.Buffer

	err := ExecutePlanning(context.Background(), "ABC-1", reader, writer, planner, publisher, log.New(&logs, "", 0))

	if err != nil {
		t.Fatalf("ExecutePlanning() error = %v", err)
	}
	if got, want := order, []string{"get issue", "get page", "get comments", "get page", "generate TRD", "publish", "update description", "transition"}; !equalStrings(got, want) {
		t.Fatalf("operation order = %v, want %v", got, want)
	}
	if planner.input.PRD != "<h2>Problem</h2>" || planner.input.IssueKey != "ABC-1" || planner.input.Summary != "Build a feature" {
		t.Fatalf("planner input = %#v, want issue and published PRD", planner.input)
	}
	if publisher.title != "ABC-1: Build a feature TRD" || publisher.markdown != "## Summary\nPlan" {
		t.Fatalf("published page = (%q, %q), want TRD titled after the issue", publisher.title, publisher.markdown)
	}
	description, _ := jira.DescriptionText(writer.description)
	if want := "Human description\nPRD\n=============\n" + testPageURL + "\nTRD\n=============\n" + testTRDURL; description != want {
		t.Fatalf("description = %q, want TRD link below the PRD link", description)
	}
	if writer.status != "PLAN REVIEW" {
		t.Fatalf("transition status = %q, want PLAN REVIEW", writer.status)
	}
}

func TestExecutePlanningWithoutPRDStopsBeforePlanning(t *testing.T) {
	order := []string{}
	var logs bytes.Buffer

	err := ExecutePlanning(context.Background(), "ABC-1", &issueReaderFake{issue: planningIssue(), order: &order}, &issueWriterFake{order: &order}, &trdGeneratorFake{order: &order}, &pagePublisherFake{order: &order}, log.New(&logs, "", 0))

	if err == nil || !strings.Contains(err.Error(), `no PRD page titled "ABC-1: Build a feature PRD"`) {
		t.Fatalf("error = %v, want missing PRD error", err)
	}
	if got, want := order, []string{"get issue", "get page"}; !equalStrings(got, want) {
		t.Fatalf("operation order = %v, want %v", got, want)
	}
}

func TestExecutePlanningGenerationFailureStopsLaterOperations(t *testing.T) {
	order := []string{}
	plannerErr := errors.New("planner failed")
	var logs bytes.Buffer

	err := ExecutePlanning(context.Background(), "ABC-1", &issueReaderFake{issue: planningIssue(), order: &order}, &issueWriterFake{order: &order}, &trdGeneratorFake{err: plannerErr, trd: "secret generated TRD", order: &order}, &pagePublisherFake{pages: prdOnly(), order: &order}, log.New(&logs, "", 0))

	if !errors.Is(err, plannerErr) {
		t.Fatalf("error = %v, want wrapped planner error", err)
	}
	if got, want := order, []string{"get issue", "get page", "get comments", "get page", "generate TRD"}; !equalStrings(got, want) {
		t.Fatalf("operation order = %v, want %v", got, want)
	}
	assertSafeFailureLog(t, logs.String(), "ABC-1", "generate TRD", "secret generated TRD")
}

func TestExecutePlanningPublishFailureStopsUpdate(t *testing.T) {
	order := []string{}
	publishErr := errors.New("Confluence returned HTTP 403")
	var logs bytes.Buffer

	err := ExecutePlanning(context.Background(), "ABC-1", &issueReaderFake{issue: planningIssue(), order: &order}, &issueWriterFake{order: &order}, &trdGeneratorFake{trd: "secret generated TRD", order: &order}, &pagePublisherFake{pages: prdOnly(), err: publishErr, order: &order}, log.New(&logs, "", 0))

	if !errors.Is(err, publishErr) {
		t.Fatalf("error = %v, want wrapped publish error", err)
	}
	if got, want := order, []string{"get issue", "get page", "get comments", "get page", "generate TRD", "publish"}; !equalStrings(got, want) {
		t.Fatalf("operation order = %v, want %v", got, want)
	}
	assertSafeFailureLog(t, logs.String(), "ABC-1", "publish TRD", "secret generated TRD")
}

func TestDispatchPlanningRunsPlanningWorkflow(t *testing.T) {
	order := []string{}
	writer := &issueWriterFake{order: &order}
	deps := Dependencies{
		Reader:    &issueReaderFake{issue: planningIssue(), order: &order},
		Writer:    writer,
		Planner:   &trdGeneratorFake{trd: "TRD", order: &order},
		Publisher: &pagePublisherFake{url: testTRDURL, pages: prdOnly(), order: &order},
		Logger:    log.New(&bytes.Buffer{}, "", 0),
	}

	if err := Dispatch(context.Background(), StatusPlanning, "ABC-1", deps); err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if writer.status != string(StatusPlanReview) {
		t.Fatalf("transition = %q, want %q", writer.status, StatusPlanReview)
	}
}

func TestExecutePlanningRevisesTRDFromOutstandingFeedback(t *testing.T) {
	order := []string{}
	done := map[string]json.RawMessage{TRDFeedbackDoneProperty: json.RawMessage(`{"done":true}`)}
	reader := &issueReaderFake{
		issue: planningIssue(),
		comments: []jira.Comment{
			{ID: "1", Author: "Ana", AuthorAccountID: "acc-ana", Created: "2026-09-01", Body: "!tech-planner use localStorage, not a server"},
			{ID: "2", Author: "Ana", Created: "2026-09-02", Body: "!prd-maker this is PRD feedback"},
			{ID: "3", Author: "Ana", Created: "2026-08-01", Body: "!Tech-Planner already handled", Properties: done},
		},
		order: &order,
	}
	pages := prdOnly()
	pages[testTRDTitle] = "<h2>Summary</h2>"
	publisher := &pagePublisherFake{url: testTRDURL, order: &order, pages: pages}
	planner := &trdGeneratorFake{trd: "Revised TRD", order: &order}
	writer := &issueWriterFake{order: &order}
	var logs bytes.Buffer

	err := ExecutePlanning(context.Background(), "ABC-1", reader, writer, planner, publisher, log.New(&logs, "", 0))

	if err != nil {
		t.Fatalf("ExecutePlanning() error = %v", err)
	}
	if got, want := order, []string{"get issue", "get page", "get comments", "get page", "generate TRD", "publish", "update description", "comment", "mark 1", "transition"}; !equalStrings(got, want) {
		t.Fatalf("operation order = %v, want %v", got, want)
	}
	if got := planner.input.Feedback; len(got) != 1 || got[0].Comment.ID != "1" || got[0].Text != "use localStorage, not a server" {
		t.Fatalf("planner feedback = %#v, want only outstanding comment 1", got)
	}
	if planner.input.CurrentTRD != "<h2>Summary</h2>" || planner.input.PRD != "<h2>Problem</h2>" {
		t.Fatalf("planner input = %#v, want current TRD and PRD", planner.input)
	}
	reply := string(writer.comments[0])
	if !strings.Contains(reply, "applied in the TRD") || !strings.Contains(reply, `"href":"`+testTRDURL+`"`) || !strings.Contains(reply, `"id":"acc-ana"`) {
		t.Fatalf("reply = %s, want TRD done reply mentioning the author", reply)
	}
	if _, ok := writer.properties[TRDFeedbackDoneProperty+"/1"]; !ok {
		t.Fatalf("properties = %v, want comment 1 marked with the TRD property", writer.properties)
	}
	if !strings.Contains(logs.String(), "mode=revision feedback=1") {
		t.Fatalf("log = %q, want revision mode", logs.String())
	}
}

func TestExecutePlanningWithoutOutstandingFeedbackLeavesTRDUnchanged(t *testing.T) {
	order := []string{}
	pages := prdOnly()
	pages[testTRDTitle] = "<h2>Summary</h2>"
	writer := &issueWriterFake{order: &order}

	err := ExecutePlanning(context.Background(), "ABC-1", &issueReaderFake{issue: planningIssue(), order: &order}, writer, &trdGeneratorFake{order: &order}, &pagePublisherFake{order: &order, pages: pages}, log.New(&bytes.Buffer{}, "", 0))

	if err != nil {
		t.Fatalf("ExecutePlanning() error = %v", err)
	}
	if got, want := order, []string{"get issue", "get page", "get comments", "get page", "comment", "transition"}; !equalStrings(got, want) {
		t.Fatalf("operation order = %v, want %v", got, want)
	}
	comment := string(writer.comments[0])
	if !strings.Contains(comment, "Nothing to revise") || !strings.Contains(comment, "!tech-planner") || !strings.Contains(comment, "Planning") || writer.status != "PLAN REVIEW" {
		t.Fatalf("comment = %s, status = %q, want TRD explanation and PLAN REVIEW", comment, writer.status)
	}
}

func TestTRDFeedbackIgnoresPRDFeedback(t *testing.T) {
	comments := []jira.Comment{
		{ID: "1", Body: "!prd-maker for the PRD"},
		{ID: "2", Body: "!tech-planner: for the TRD"},
		{ID: "3", Body: "!tech-planners someone else"},
		{ID: "4", Body: "!tech-planner done", Properties: map[string]json.RawMessage{PRDFeedbackDoneProperty: json.RawMessage(`{"done":true}`)}},
	}
	got := TRDFeedback(comments)
	if len(got) != 2 || got[0].Comment.ID != "2" || got[0].Text != "for the TRD" || got[1].Comment.ID != "4" {
		t.Fatalf("TRDFeedback() = %#v, want comments 2 and 4 (a PRD done marker does not count)", got)
	}
	if prd := PRDFeedback(comments); len(prd) != 1 || prd[0].Comment.ID != "1" {
		t.Fatalf("PRDFeedback() = %#v, want only comment 1", prd)
	}
}
