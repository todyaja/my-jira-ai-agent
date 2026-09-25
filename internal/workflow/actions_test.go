package workflow

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
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
		{"ai failed", StatusAIFailed, "AI FAILED"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if string(tt.status) != tt.want {
				t.Fatalf("status = %q, want %q", tt.status, tt.want)
			}
		})
	}
}

func TestDispatchNoOpStatuses(t *testing.T) {
	statuses := []Status{
		StatusBacklog,
		StatusPRDReview,
		StatusPlanning,
		StatusPlanReview,
		StatusReadyToImplement,
		StatusImplementing,
		StatusAIReview,
		StatusPRReady,
		StatusDone,
		StatusAIFailed,
	}

	for _, status := range statuses {
		t.Run(string(status), func(t *testing.T) {
			var output bytes.Buffer
			generator := &recordingGenerator{}
			logger := log.New(&output, "", 0)

			if err := Dispatch(context.Background(), status, PRDInput{IssueKey: "DEMO-1"}, generator, logger); err != nil {
				t.Fatalf("Dispatch() error = %v", err)
			}
			if len(generator.calls) != 0 {
				t.Fatalf("generator calls = %d, want 0", len(generator.calls))
			}
			wantLog := "No AI action implemented for status " + string(status) + "\n"
			if output.String() != wantLog {
				t.Fatalf("log = %q, want %q", output.String(), wantLog)
			}
		})
	}
}

func TestDispatchPRDRequested(t *testing.T) {
	input := PRDInput{
		IssueKey:    "DEMO-1",
		Summary:     "Build a thing",
		Description: "The original description",
	}

	t.Run("passes input and logs completion without generated text", func(t *testing.T) {
		var output bytes.Buffer
		generator := &recordingGenerator{text: "generated PRD"}
		logger := log.New(&output, "", 0)

		if err := Dispatch(context.Background(), StatusPRDRequested, input, generator, logger); err != nil {
			t.Fatalf("Dispatch() error = %v", err)
		}
		if len(generator.calls) != 1 || generator.calls[0] != input {
			t.Fatalf("generator input = %#v, want %#v", generator.calls, []PRDInput{input})
		}
		wantLog := "PRD generation completed for DEMO-1\n"
		if output.String() != wantLog {
			t.Fatalf("log = %q, want %q", output.String(), wantLog)
		}
		if strings.Contains(output.String(), generator.text) {
			t.Fatalf("log contains generated text: %q", output.String())
		}
	})

	t.Run("returns generator error unchanged", func(t *testing.T) {
		wantErr := errors.New("generator failed")
		var output bytes.Buffer
		generator := &recordingGenerator{err: wantErr}
		logger := log.New(&output, "", 0)

		err := Dispatch(context.Background(), StatusPRDRequested, input, generator, logger)
		if err != wantErr {
			t.Fatalf("Dispatch() error = %v, want %v", err, wantErr)
		}
		if output.Len() != 0 {
			t.Fatalf("log = %q, want empty log", output.String())
		}
	})
}

func TestDispatchUnknownStatus(t *testing.T) {
	var output bytes.Buffer
	generator := &recordingGenerator{}
	logger := log.New(&output, "", 0)

	err := Dispatch(context.Background(), Status("UNKNOWN"), PRDInput{}, generator, logger)
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
