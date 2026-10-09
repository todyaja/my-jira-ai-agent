package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"reflect"
	"strings"
	"testing"

	"github.com/tody-aja/jira-ai-agent/internal/jira"
)

type issueReaderFake struct {
	issue       jira.Issue
	err         error
	comments    []jira.Comment
	commentsErr error
	order       *[]string
	// properties holds issue properties by key.
	properties map[string]json.RawMessage
}

func (f *issueReaderFake) GetIssueProperty(_ context.Context, _ string, propertyKey string) (json.RawMessage, bool, error) {
	*f.order = append(*f.order, "get state")
	value, found := f.properties[propertyKey]
	return value, found, nil
}

func (f *issueReaderFake) GetIssue(context.Context, string) (jira.Issue, error) {
	*f.order = append(*f.order, "get issue")
	return f.issue, f.err
}

func (f *issueReaderFake) GetComments(context.Context, string) ([]jira.Comment, error) {
	*f.order = append(*f.order, "get comments")
	return f.comments, f.commentsErr
}

type issueWriterFake struct {
	updateErr     error
	transitionErr error
	commentErr    error
	order         *[]string
	description   json.RawMessage
	status        string
	comments      []json.RawMessage
	properties    map[string]json.RawMessage
	// issueProperties holds the issue properties written, by key.
	issueProperties map[string]json.RawMessage
}

func (f *issueWriterFake) AddComment(_ context.Context, _ string, body json.RawMessage) (string, error) {
	*f.order = append(*f.order, "comment")
	f.comments = append(f.comments, body)
	return fmt.Sprintf("c%d", len(f.comments)), f.commentErr
}

func (f *issueWriterFake) SetIssueProperty(_ context.Context, _ string, propertyKey string, value json.RawMessage) error {
	*f.order = append(*f.order, "save state")
	if f.issueProperties == nil {
		f.issueProperties = map[string]json.RawMessage{}
	}
	f.issueProperties[propertyKey] = value
	return nil
}

func (f *issueWriterFake) SetCommentProperty(_ context.Context, commentID, propertyKey string, value json.RawMessage) error {
	*f.order = append(*f.order, "mark "+commentID)
	if f.properties == nil {
		f.properties = map[string]json.RawMessage{}
	}
	f.properties[propertyKey+"/"+commentID] = value
	return nil
}

func (f *issueWriterFake) UpdateDescription(_ context.Context, _ string, description json.RawMessage) error {
	*f.order = append(*f.order, "update description")
	f.description = description
	if f.updateErr != nil {
		return f.updateErr
	}
	return nil
}

func (f *issueWriterFake) TransitionTo(_ context.Context, _ string, status string) error {
	*f.order = append(*f.order, "transition")
	f.status = status
	if f.transitionErr != nil {
		return f.transitionErr
	}
	return nil
}

type prdGeneratorFake struct {
	prd   string
	err   error
	order *[]string
	input PRDInput
}

func (f *prdGeneratorFake) Generate(_ context.Context, input PRDInput) (string, error) {
	*f.order = append(*f.order, "generate")
	f.input = input
	return f.prd, f.err
}

type pagePublisherFake struct {
	url        string
	err        error
	order      *[]string
	title      string
	markdown   string
	content    string
	found      bool
	contentErr error
	// pages, when set, holds the existing pages by title instead of
	// content and found.
	pages map[string]string
}

func (f *pagePublisherFake) PageContent(_ context.Context, title string) (string, bool, error) {
	*f.order = append(*f.order, "get page")
	f.title = title
	if f.pages != nil {
		content, found := f.pages[title]
		return content, found, f.contentErr
	}
	return f.content, f.found, f.contentErr
}

func (f *pagePublisherFake) PublishPage(_ context.Context, title, markdown string) (string, error) {
	*f.order = append(*f.order, "publish")
	f.title = title
	f.markdown = markdown
	return f.url, f.err
}

const testPageURL = "https://example.atlassian.net/wiki/spaces/ENG/pages/1"

func TestExecutePRDRequestedRunsOperationsInOrder(t *testing.T) {
	order := []string{}
	reader := &issueReaderFake{
		issue: jira.Issue{
			Key:            "ABC-1",
			Summary:        "Build a feature",
			Description:    "Human description\nPRD\n=============\nold PRD",
			DescriptionADF: json.RawMessage(`{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"Human description"}]},{"type":"paragraph","content":[{"type":"text","text":"PRD"}]},{"type":"paragraph","content":[{"type":"text","text":"============="}]},{"type":"paragraph","content":[{"type":"text","text":"old PRD"}]}]}`),
		},
		order: &order,
	}
	generator := &prdGeneratorFake{prd: "Generated PRD", order: &order}
	publisher := &pagePublisherFake{url: testPageURL, order: &order}
	writer := &issueWriterFake{order: &order}
	var logs bytes.Buffer

	err := ExecutePRDRequested(context.Background(), "ABC-1", reader, writer, generator, publisher, log.New(&logs, "", 0))

	if err != nil {
		t.Fatalf("ExecutePRDRequested() error = %v", err)
	}
	if got, want := order, []string{"get issue", "get comments", "get page", "generate", "publish", "update description", "transition"}; !equalStrings(got, want) {
		t.Fatalf("operation order = %v, want %v", got, want)
	}
	if publisher.title != "ABC-1: Build a feature PRD" || publisher.markdown != "Generated PRD" {
		t.Fatalf("published page = (%q, %q), want PRD titled after the issue", publisher.title, publisher.markdown)
	}
	if got, want := generator.input, (PRDInput{
		IssueKey:    "ABC-1",
		Summary:     "Build a feature",
		Description: "Human description\nPRD\n=============\nold PRD",
		Feedback:    []Feedback{},
	}); !reflect.DeepEqual(got, want) {
		t.Fatalf("generator input = %#v, want %#v", got, want)
	}
	if !strings.Contains(string(writer.description), `"href":"`+testPageURL+`"`) || strings.Contains(string(writer.description), "Generated PRD") || strings.Contains(string(writer.description), "old PRD") {
		t.Fatalf("updated description = %s, want PRD link in place of the PRD text", writer.description)
	}
	if writer.status != "PRD REVIEW" {
		t.Fatalf("transition status = %q, want %q", writer.status, "PRD REVIEW")
	}
}

func TestExecutePRDRequestedInvalidADFStopsBeforeUpdate(t *testing.T) {
	order := []string{}
	writer := &issueWriterFake{order: &order}
	var logs bytes.Buffer

	err := ExecutePRDRequested(context.Background(), "ABC-5", &issueReaderFake{
		issue: jira.Issue{Key: "ABC-5", DescriptionADF: json.RawMessage(`{"type":"not-a-doc"}`)},
		order: &order,
	}, writer, &prdGeneratorFake{prd: "secret generated PRD", order: &order}, &pagePublisherFake{url: testPageURL, order: &order}, log.New(&logs, "", 0))

	if err == nil || !strings.Contains(err.Error(), "convert description") {
		t.Fatalf("error = %v, want description conversion error", err)
	}
	if got, want := order, []string{"get issue"}; !equalStrings(got, want) {
		t.Fatalf("operation order = %v, want %v", got, want)
	}
}

func TestExecutePRDRequestedGenerationFailureStopsLaterOperations(t *testing.T) {
	order := []string{}
	generatorErr := errors.New("generator failed")
	generator := &prdGeneratorFake{err: generatorErr, prd: "secret generated PRD", order: &order}
	writer := &issueWriterFake{order: &order}
	var logs bytes.Buffer

	err := ExecutePRDRequested(context.Background(), "ABC-2", &issueReaderFake{issue: jira.Issue{Key: "ABC-2"}, order: &order}, writer, generator, &pagePublisherFake{url: testPageURL, order: &order}, log.New(&logs, "", 0))

	if !errors.Is(err, generatorErr) {
		t.Fatalf("error = %v, want wrapped generator error", err)
	}
	if got, want := order, []string{"get issue", "get comments", "get page", "generate"}; !equalStrings(got, want) {
		t.Fatalf("operation order = %v, want %v", got, want)
	}
	assertSafeFailureLog(t, logs.String(), "ABC-2", "generate", "secret generated PRD")
}

func TestExecutePRDRequestedUpdateFailureStopsTransition(t *testing.T) {
	order := []string{}
	updateErr := errors.New("update failed")
	writer := &issueWriterFake{updateErr: updateErr, order: &order}
	var logs bytes.Buffer

	err := ExecutePRDRequested(context.Background(), "ABC-3", &issueReaderFake{issue: jira.Issue{Key: "ABC-3"}, order: &order}, writer, &prdGeneratorFake{prd: "secret generated PRD", order: &order}, &pagePublisherFake{url: testPageURL, order: &order}, log.New(&logs, "", 0))

	if !errors.Is(err, updateErr) {
		t.Fatalf("error = %v, want wrapped update error", err)
	}
	if got, want := order, []string{"get issue", "get comments", "get page", "generate", "publish", "update description"}; !equalStrings(got, want) {
		t.Fatalf("operation order = %v, want %v", got, want)
	}
	assertSafeFailureLog(t, logs.String(), "ABC-3", "update description", "secret generated PRD")
}

func TestExecutePRDRequestedTransitionFailureStopsAfterTransition(t *testing.T) {
	order := []string{}
	transitionErr := errors.New("transition failed")
	writer := &issueWriterFake{transitionErr: transitionErr, order: &order}
	var logs bytes.Buffer

	err := ExecutePRDRequested(context.Background(), "ABC-4", &issueReaderFake{issue: jira.Issue{Key: "ABC-4"}, order: &order}, writer, &prdGeneratorFake{prd: "secret generated PRD", order: &order}, &pagePublisherFake{url: testPageURL, order: &order}, log.New(&logs, "", 0))

	if !errors.Is(err, transitionErr) {
		t.Fatalf("error = %v, want wrapped transition error", err)
	}
	if got, want := order, []string{"get issue", "get comments", "get page", "generate", "publish", "update description", "transition"}; !equalStrings(got, want) {
		t.Fatalf("operation order = %v, want %v", got, want)
	}
	assertSafeFailureLog(t, logs.String(), "ABC-4", "transition", "secret generated PRD")
}

func TestExecutePRDRequestedPublishFailureStopsUpdate(t *testing.T) {
	order := []string{}
	publishErr := errors.New("Confluence returned HTTP 403")
	writer := &issueWriterFake{order: &order}
	var logs bytes.Buffer

	err := ExecutePRDRequested(context.Background(), "ABC-6", &issueReaderFake{issue: jira.Issue{Key: "ABC-6"}, order: &order}, writer, &prdGeneratorFake{prd: "secret generated PRD", order: &order}, &pagePublisherFake{err: publishErr, order: &order}, log.New(&logs, "", 0))

	if !errors.Is(err, publishErr) {
		t.Fatalf("error = %v, want wrapped publish error", err)
	}
	if got, want := order, []string{"get issue", "get comments", "get page", "generate", "publish"}; !equalStrings(got, want) {
		t.Fatalf("operation order = %v, want %v", got, want)
	}
	assertSafeFailureLog(t, logs.String(), "ABC-6", "publish PRD", "secret generated PRD")
}

func TestExecutePRDRequestedRevisesPRDFromOutstandingFeedback(t *testing.T) {
	order := []string{}
	reader := &issueReaderFake{
		issue: jira.Issue{Key: "ABC-7", Summary: "Export", Description: "Export reports", DescriptionADF: json.RawMessage(`{"type":"doc","version":1,"content":[]}`)},
		comments: []jira.Comment{
			{ID: "1", Author: "Ana", AuthorAccountID: "acc-ana", Created: "2026-09-01", Body: "!PRD-Maker: CSV only for now"},
			{ID: "2", Author: "Budi", Created: "2026-09-02", Body: "Looks good to me"},
			{ID: "3", Author: "Ana", AuthorAccountID: "acc-ana", Created: "2026-09-03", Body: "!prd-maker\nThe answer to Q2 is admins only."},
			{ID: "4", Author: "Ana", Created: "2026-08-01", Body: "!prd-maker old feedback", Properties: map[string]json.RawMessage{PRDFeedbackDoneProperty: json.RawMessage(`{"done":true}`)}},
		},
		order: &order,
	}
	generator := &prdGeneratorFake{prd: "Revised PRD", order: &order}
	publisher := &pagePublisherFake{url: testPageURL, order: &order, content: "<h2>Problem</h2>", found: true}
	writer := &issueWriterFake{order: &order}
	var logs bytes.Buffer

	err := ExecutePRDRequested(context.Background(), "ABC-7", reader, writer, generator, publisher, log.New(&logs, "", 0))

	if err != nil {
		t.Fatalf("ExecutePRDRequested() error = %v", err)
	}
	if got, want := order, []string{"get issue", "get comments", "get page", "generate", "publish", "update description", "comment", "mark 1", "comment", "mark 3", "transition"}; !equalStrings(got, want) {
		t.Fatalf("operation order = %v, want %v", got, want)
	}
	if got := generator.input.Feedback; len(got) != 2 || got[0].Comment.ID != "1" || got[0].Text != "CSV only for now" || got[1].Comment.ID != "3" || got[1].Text != "The answer to Q2 is admins only." {
		t.Fatalf("generator feedback = %#v, want outstanding comments 1 and 3", got)
	}
	if generator.input.CurrentPRD != "<h2>Problem</h2>" {
		t.Fatalf("generator current PRD = %q, want published page", generator.input.CurrentPRD)
	}
	if publisher.title != "ABC-7: Export PRD" || publisher.markdown != "Revised PRD" {
		t.Fatalf("published page = (%q, %q), want revised PRD on the same page", publisher.title, publisher.markdown)
	}
	reply := string(writer.comments[0])
	for _, want := range []string{`"type":"mention"`, `"id":"acc-ana"`, "Done", `"href":"` + testPageURL + `"`, `"type":"blockquote"`, "!PRD-Maker: CSV only for now"} {
		if !strings.Contains(reply, want) {
			t.Fatalf("reply = %s, want it to contain %q", reply, want)
		}
	}
	if got := string(writer.properties[PRDFeedbackDoneProperty+"/1"]); !strings.Contains(got, `"done":true`) || !strings.Contains(got, testPageURL) {
		t.Fatalf("property on comment 1 = %s, want done with page URL", got)
	}
	if !strings.Contains(logs.String(), "mode=revision feedback=2") {
		t.Fatalf("log = %q, want revision mode with 2 comments", logs.String())
	}
}

func TestExecutePRDRequestedFeedbackWithoutPublishedPRDWritesNewPRD(t *testing.T) {
	order := []string{}
	reader := &issueReaderFake{
		issue:    jira.Issue{Key: "ABC-8", Summary: "Export"},
		comments: []jira.Comment{{ID: "5", Body: "!prd-maker CSV only"}},
		order:    &order,
	}
	generator := &prdGeneratorFake{prd: "PRD", order: &order}

	err := ExecutePRDRequested(context.Background(), "ABC-8", reader, &issueWriterFake{order: &order}, generator, &pagePublisherFake{url: testPageURL, order: &order}, log.New(&bytes.Buffer{}, "", 0))

	if err != nil {
		t.Fatalf("ExecutePRDRequested() error = %v", err)
	}
	if generator.input.CurrentPRD != "" || len(generator.input.Feedback) != 1 {
		t.Fatalf("generator input = %#v, want feedback without a current PRD", generator.input)
	}
	if !strings.Contains(strings.Join(order, ","), "mark 5") {
		t.Fatalf("operations = %v, want feedback marked done", order)
	}
}

func TestExecutePRDRequestedWithoutOutstandingFeedbackLeavesPRDUnchanged(t *testing.T) {
	order := []string{}
	reader := &issueReaderFake{
		issue:    jira.Issue{Key: "ABC-11", Summary: "Export"},
		comments: []jira.Comment{{ID: "1", Body: "!prd-maker done already", Properties: map[string]json.RawMessage{PRDFeedbackDoneProperty: json.RawMessage(`{"done":true}`)}}},
		order:    &order,
	}
	writer := &issueWriterFake{order: &order}

	err := ExecutePRDRequested(context.Background(), "ABC-11", reader, writer, &prdGeneratorFake{order: &order}, &pagePublisherFake{url: testPageURL, order: &order, content: "<p>PRD</p>", found: true}, log.New(&bytes.Buffer{}, "", 0))

	if err != nil {
		t.Fatalf("ExecutePRDRequested() error = %v", err)
	}
	if got, want := order, []string{"get issue", "get comments", "get page", "comment", "transition"}; !equalStrings(got, want) {
		t.Fatalf("operation order = %v, want %v", got, want)
	}
	if !strings.Contains(string(writer.comments[0]), "Nothing to revise") || writer.status != "PRD REVIEW" {
		t.Fatalf("comment = %s, status = %q, want explanation and PRD REVIEW", writer.comments[0], writer.status)
	}
}

func TestExecutePRDRequestedReplyFailureLeavesFeedbackOutstanding(t *testing.T) {
	order := []string{}
	commentErr := errors.New("comment failed")
	reader := &issueReaderFake{issue: jira.Issue{Key: "ABC-12"}, comments: []jira.Comment{{ID: "1", Body: "!prd-maker CSV only"}}, order: &order}
	writer := &issueWriterFake{order: &order, commentErr: commentErr}

	err := ExecutePRDRequested(context.Background(), "ABC-12", reader, writer, &prdGeneratorFake{prd: "PRD", order: &order}, &pagePublisherFake{url: testPageURL, order: &order, found: true}, log.New(&bytes.Buffer{}, "", 0))

	if !errors.Is(err, commentErr) {
		t.Fatalf("error = %v, want wrapped comment error", err)
	}
	if got, want := order, []string{"get issue", "get comments", "get page", "generate", "publish", "update description", "comment"}; !equalStrings(got, want) {
		t.Fatalf("operation order = %v, want %v", got, want)
	}
}

func TestExecutePRDRequestedCommentsFailureStopsGeneration(t *testing.T) {
	order := []string{}
	commentsErr := errors.New("comments failed")
	reader := &issueReaderFake{issue: jira.Issue{Key: "ABC-9"}, commentsErr: commentsErr, order: &order}

	err := ExecutePRDRequested(context.Background(), "ABC-9", reader, &issueWriterFake{order: &order}, &prdGeneratorFake{order: &order}, &pagePublisherFake{order: &order}, log.New(&bytes.Buffer{}, "", 0))

	if !errors.Is(err, commentsErr) {
		t.Fatalf("error = %v, want wrapped comments error", err)
	}
	if got, want := order, []string{"get issue", "get comments"}; !equalStrings(got, want) {
		t.Fatalf("operation order = %v, want %v", got, want)
	}
}

func TestExecutePRDRequestedCurrentPRDFailureStopsGeneration(t *testing.T) {
	order := []string{}
	contentErr := errors.New("Confluence returned HTTP 500")
	reader := &issueReaderFake{issue: jira.Issue{Key: "ABC-10"}, order: &order}

	err := ExecutePRDRequested(context.Background(), "ABC-10", reader, &issueWriterFake{order: &order}, &prdGeneratorFake{order: &order}, &pagePublisherFake{contentErr: contentErr, order: &order}, log.New(&bytes.Buffer{}, "", 0))

	if !errors.Is(err, contentErr) {
		t.Fatalf("error = %v, want wrapped page error", err)
	}
	if got, want := order, []string{"get issue", "get comments", "get page"}; !equalStrings(got, want) {
		t.Fatalf("operation order = %v, want %v", got, want)
	}
}

func TestPRDFeedbackKeepsOnlyOutstandingCommentsAddressedToTheAgent(t *testing.T) {
	done := map[string]json.RawMessage{PRDFeedbackDoneProperty: json.RawMessage(`{"done":true}`)}
	comments := []jira.Comment{
		{ID: "A", Body: "  !prd-maker the answers are: 1. yes"},
		{ID: "B", Body: "!PRD-MAKER, drop the export"},
		{ID: "C", Body: "!prd-makers please look"},
		{ID: "D", Body: "!prd-maker2 hello"},
		{ID: "E", Body: "cc !prd-maker later"},
		{ID: "F", Body: "!prd-maker"},
		{ID: "G", Body: "@prd"},
		{ID: "H", Body: "!prd-maker already handled", Properties: done},
	}
	want := []Feedback{
		{Comment: comments[0], Text: "the answers are: 1. yes"},
		{Comment: comments[1], Text: "drop the export"},
	}
	if got := PRDFeedback(comments); !reflect.DeepEqual(got, want) {
		t.Fatalf("PRDFeedback() = %#v, want %#v", got, want)
	}
}

func assertSafeFailureLog(t *testing.T, output, key, operation, forbidden string) {
	t.Helper()
	if !strings.Contains(output, key) || !strings.Contains(output, operation) {
		t.Fatalf("log = %q, want issue key %q and operation %q", output, key, operation)
	}
	if strings.Contains(output, forbidden) {
		t.Fatalf("log = %q contains forbidden generated PRD", output)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
