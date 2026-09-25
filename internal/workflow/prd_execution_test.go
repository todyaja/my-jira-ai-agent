package workflow

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"

	"github.com/tody-aja/jira-ai-agent/internal/jira"
)

type issueReaderFake struct {
	issue jira.Issue
	err   error
	order *[]string
}

func (f *issueReaderFake) GetIssue(context.Context, string) (jira.Issue, error) {
	*f.order = append(*f.order, "get issue")
	return f.issue, f.err
}

type issueWriterFake struct {
	updateErr     error
	transitionErr error
	order         *[]string
	description   string
	status        string
}

func (f *issueWriterFake) UpdateDescription(_ context.Context, _ string, description string) error {
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

func TestExecutePRDRequestedRunsOperationsInOrder(t *testing.T) {
	order := []string{}
	reader := &issueReaderFake{
		issue: jira.Issue{
			Key:         "ABC-1",
			Summary:     "Build a feature",
			Description: "Human description\nPRD\n=============\nold PRD",
		},
		order: &order,
	}
	generator := &prdGeneratorFake{prd: "Generated PRD", order: &order}
	writer := &issueWriterFake{order: &order}
	var logs bytes.Buffer

	err := ExecutePRDRequested(context.Background(), "ABC-1", reader, writer, generator, log.New(&logs, "", 0))

	if err != nil {
		t.Fatalf("ExecutePRDRequested() error = %v", err)
	}
	if got, want := order, []string{"get issue", "generate", "update description", "transition"}; !equalStrings(got, want) {
		t.Fatalf("operation order = %v, want %v", got, want)
	}
	if got, want := generator.input, (PRDInput{
		IssueKey:    "ABC-1",
		Summary:     "Build a feature",
		Description: "Human description\nPRD\n=============\nold PRD",
	}); got != want {
		t.Fatalf("generator input = %#v, want %#v", got, want)
	}
	if got, want := writer.description, "Human description\nPRD\n=============\nGenerated PRD"; got != want {
		t.Fatalf("updated description = %q, want %q", got, want)
	}
	if writer.status != "PRD REVIEW" {
		t.Fatalf("transition status = %q, want %q", writer.status, "PRD REVIEW")
	}
}

func TestExecutePRDRequestedGenerationFailureStopsLaterOperations(t *testing.T) {
	order := []string{}
	generatorErr := errors.New("generator failed")
	generator := &prdGeneratorFake{err: generatorErr, prd: "secret generated PRD", order: &order}
	writer := &issueWriterFake{order: &order}
	var logs bytes.Buffer

	err := ExecutePRDRequested(context.Background(), "ABC-2", &issueReaderFake{issue: jira.Issue{Key: "ABC-2"}, order: &order}, writer, generator, log.New(&logs, "", 0))

	if !errors.Is(err, generatorErr) {
		t.Fatalf("error = %v, want wrapped generator error", err)
	}
	if got, want := order, []string{"get issue", "generate"}; !equalStrings(got, want) {
		t.Fatalf("operation order = %v, want %v", got, want)
	}
	assertSafeFailureLog(t, logs.String(), "ABC-2", "generate", "secret generated PRD")
}

func TestExecutePRDRequestedUpdateFailureStopsTransition(t *testing.T) {
	order := []string{}
	updateErr := errors.New("update failed")
	writer := &issueWriterFake{updateErr: updateErr, order: &order}
	var logs bytes.Buffer

	err := ExecutePRDRequested(context.Background(), "ABC-3", &issueReaderFake{issue: jira.Issue{Key: "ABC-3"}, order: &order}, writer, &prdGeneratorFake{prd: "secret generated PRD", order: &order}, log.New(&logs, "", 0))

	if !errors.Is(err, updateErr) {
		t.Fatalf("error = %v, want wrapped update error", err)
	}
	if got, want := order, []string{"get issue", "generate", "update description"}; !equalStrings(got, want) {
		t.Fatalf("operation order = %v, want %v", got, want)
	}
	assertSafeFailureLog(t, logs.String(), "ABC-3", "update description", "secret generated PRD")
}

func TestExecutePRDRequestedTransitionFailureStopsAfterTransition(t *testing.T) {
	order := []string{}
	transitionErr := errors.New("transition failed")
	writer := &issueWriterFake{transitionErr: transitionErr, order: &order}
	var logs bytes.Buffer

	err := ExecutePRDRequested(context.Background(), "ABC-4", &issueReaderFake{issue: jira.Issue{Key: "ABC-4"}, order: &order}, writer, &prdGeneratorFake{prd: "secret generated PRD", order: &order}, log.New(&logs, "", 0))

	if !errors.Is(err, transitionErr) {
		t.Fatalf("error = %v, want wrapped transition error", err)
	}
	if got, want := order, []string{"get issue", "generate", "update description", "transition"}; !equalStrings(got, want) {
		t.Fatalf("operation order = %v, want %v", got, want)
	}
	assertSafeFailureLog(t, logs.String(), "ABC-4", "transition", "secret generated PRD")
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
