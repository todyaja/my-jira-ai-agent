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

type recordingGenerator struct {
	calls []PRDInput
	text  string
	err   error
}

func (g *recordingGenerator) Generate(_ context.Context, input PRDInput) (string, error) {
	g.calls = append(g.calls, input)
	return g.text, g.err
}

func TestCanonicalStatuses(t *testing.T) {
	tests := []struct {
		name   string
		status Status
		want   string
	}{
		{"backlog", StatusBacklog, "BACKLOG"},
		{"prd requested", StatusPRDRequested, "PRD REQUESTED"},
		{"prd review", StatusPRDReview, "PRD REVIEW"},
		{"planning", StatusPlanning, "PLANNING"},
		{"plan review", StatusPlanReview, "PLAN REVIEW"},
		{"ready to implement", StatusReadyToImplement, "READY TO IMPLEMENT"},
		{"implementing", StatusImplementing, "IMPLEMENTING"},
		{"ai review", StatusAIReview, "AI REVIEW"},
		{"pr ready", StatusPRReady, "PR READY"},
		{"done", StatusDone, "DONE"},
		{"ai error", StatusAIError, "AI ERROR"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.status) != tt.want {
				t.Fatalf("status = %q, want %q", tt.status, tt.want)
			}
		})
	}
}

func TestParseStatusMatchesJiraNamesIgnoringCase(t *testing.T) {
	// Names as they appear in the Jira workflow.
	jiraNames := map[string]Status{
		"Backlog":            StatusBacklog,
		"PRD Requested":      StatusPRDRequested,
		"PRD Review":         StatusPRDReview,
		"Planning":           StatusPlanning,
		"Plan Review":        StatusPlanReview,
		"Ready To Implement": StatusReadyToImplement,
		"Implementing":       StatusImplementing,
		"AI Review":          StatusAIReview,
		"PR Ready":           StatusPRReady,
		"Done":               StatusDone,
		"AI ERROR":           StatusAIError,
	}
	for name, want := range jiraNames {
		if got, ok := ParseStatus(name); !ok || got != want {
			t.Errorf("ParseStatus(%q) = (%q, %v), want %q", name, got, ok, want)
		}
	}
	for _, name := range []string{"", "In Progress", "PRD Reviewer"} {
		if got, ok := ParseStatus(name); ok {
			t.Errorf("ParseStatus(%q) = %q, want no match", name, got)
		}
	}
	if len(jiraNames) != len(Statuses) {
		t.Fatalf("test covers %d statuses, Statuses has %d", len(jiraNames), len(Statuses))
	}
}

func TestDispatchNoOpStatuses(t *testing.T) {
	for _, status := range Statuses {
		switch status {
		case StatusPRDRequested, StatusPlanning, StatusReadyToImplement, StatusImplementing, StatusAIReview:
			continue
		}
		t.Run(string(status), func(t *testing.T) {
			var output bytes.Buffer
			generator := &recordingGenerator{}
			deps := Dependencies{Generator: generator, Logger: log.New(&output, "", 0)}

			if err := Dispatch(context.Background(), status, "DEMO-1", deps); err != nil {
				t.Fatalf("Dispatch() error = %v", err)
			}
			if len(generator.calls) != 0 {
				t.Fatalf("generator calls = %d, want 0", len(generator.calls))
			}
			wantLog := "No AI action implemented for status " + string(status) + ": issue=DEMO-1\n"
			if output.String() != wantLog {
				t.Fatalf("log = %q, want %q", output.String(), wantLog)
			}
		})
	}
}

func TestDispatchPRDRequestedRunsPRDWorkflow(t *testing.T) {
	order := []string{}
	reader := &issueReaderFake{
		issue: jira.Issue{Key: "DEMO-1", Summary: "Build a thing", Description: "The original description", DescriptionADF: json.RawMessage(`{"type":"doc","version":1,"content":[]}`)},
		order: &order,
	}
	writer := &issueWriterFake{order: &order}
	generator := &prdGeneratorFake{prd: "generated PRD", order: &order}
	publisher := &pagePublisherFake{url: testPageURL, order: &order}
	var output bytes.Buffer
	deps := Dependencies{Reader: reader, Writer: writer, Generator: generator, Publisher: publisher, Logger: log.New(&output, "", 0)}

	if err := Dispatch(context.Background(), StatusPRDRequested, "DEMO-1", deps); err != nil {
		t.Fatalf("Dispatch() error = %v", err)
	}
	if strings.Join(order, ",") != "get issue,get comments,get page,generate,publish,update description,transition" {
		t.Fatalf("operations = %v, want full PRD workflow", order)
	}
	if generator.input.IssueKey != "DEMO-1" || generator.input.Summary != "Build a thing" {
		t.Fatalf("generator input = %+v, want issue fields", generator.input)
	}
	if writer.status != string(StatusPRDReview) {
		t.Fatalf("transition = %q, want %q", writer.status, StatusPRDReview)
	}
}

func TestDispatchPRDRequestedReturnsWorkflowError(t *testing.T) {
	order := []string{}
	wantErr := errors.New("generator failed")
	deps := Dependencies{
		Reader:    &issueReaderFake{issue: jira.Issue{Key: "DEMO-1"}, order: &order},
		Writer:    &issueWriterFake{order: &order},
		Generator: &prdGeneratorFake{err: wantErr, order: &order},
		Publisher: &pagePublisherFake{url: testPageURL, order: &order},
		Logger:    log.New(&bytes.Buffer{}, "", 0),
	}

	err := Dispatch(context.Background(), StatusPRDRequested, "DEMO-1", deps)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Dispatch() error = %v, want %v", err, wantErr)
	}
	if strings.Contains(strings.Join(order, ","), "publish") {
		t.Fatalf("operations = %v, want stop after failed generation", order)
	}
}

func TestDispatchUnknownStatus(t *testing.T) {
	var output bytes.Buffer
	generator := &recordingGenerator{}
	deps := Dependencies{Generator: generator, Logger: log.New(&output, "", 0)}

	err := Dispatch(context.Background(), Status("UNKNOWN"), "DEMO-1", deps)
	if err == nil {
		t.Fatal("Dispatch() error = nil, want error")
	}
	if len(generator.calls) != 0 {
		t.Fatalf("generator calls = %d, want 0", len(generator.calls))
	}
	if output.Len() != 0 {
		t.Fatalf("log = %q, want empty log", output.String())
	}
}
