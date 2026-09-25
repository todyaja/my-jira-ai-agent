package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tody-aja/jira-ai-agent/internal/jira"
	"github.com/tody-aja/jira-ai-agent/internal/workflow"
)

type fakeIssueReader struct {
	called bool
}

func (f *fakeIssueReader) GetIssue(_ context.Context, _ string) (jira.Issue, error) {
	f.called = true
	return jira.Issue{Key: "DEMO-1"}, nil
}

type fakeIssueWriter struct {
	called bool
}

func (f *fakeIssueWriter) UpdateDescription(context.Context, string, json.RawMessage) error {
	f.called = true
	return nil
}

func (f *fakeIssueWriter) TransitionTo(context.Context, string, string) error {
	f.called = true
	return nil
}

type fakePRDGenerator struct {
	called bool
	err    error
}

func (f *fakePRDGenerator) Generate(context.Context, workflow.PRDInput) (string, error) {
	f.called = true
	return "generated PRD", f.err
}

func TestHandleJiraWebhookFiltersAndTriggers(t *testing.T) {
	tests := []struct {
		name         string
		webhookEvent string
		assignee     *jira.User
		items        []jira.ChangelogItem
		wantStatus   int
		wantLog      string
	}{
		{
			name:         "triggers assigned issue entering PRD Requested",
			webhookEvent: "jira:issue_updated",
			assignee:     &jira.User{AccountID: "account-1"},
			items:        []jira.ChangelogItem{{Field: "status", ToString: stringPtr("PRD Requested")}},
			wantStatus:   http.StatusOK,
			wantLog:      "PRD WORKFLOW TRIGGERED FOR DEMO-1\nPRD operation succeeded: issue=DEMO-1 operation=execute PRD requested\n",
		},
		{
			name:         "ignores other webhook event",
			webhookEvent: "jira:issue_created",
			assignee:     &jira.User{AccountID: "account-1"},
			items:        []jira.ChangelogItem{{Field: "status", ToString: stringPtr("PRD Requested")}},
			wantStatus:   http.StatusOK,
		},
		{
			name:         "ignores unassigned issue",
			webhookEvent: "jira:issue_updated",
			items:        []jira.ChangelogItem{{Field: "status", ToString: stringPtr("PRD Requested")}},
			wantStatus:   http.StatusOK,
		},
		{
			name:         "ignores issue assigned to another account",
			webhookEvent: "jira:issue_updated",
			assignee:     &jira.User{AccountID: "account-2"},
			items:        []jira.ChangelogItem{{Field: "status", ToString: stringPtr("PRD Requested")}},
			wantStatus:   http.StatusOK,
		},
		{
			name:         "ignores unrelated status transition",
			webhookEvent: "jira:issue_updated",
			assignee:     &jira.User{AccountID: "account-1"},
			items:        []jira.ChangelogItem{{Field: "status", ToString: stringPtr("Planning")}},
			wantStatus:   http.StatusOK,
		},
		{
			name:         "ignores ordinary field change",
			webhookEvent: "jira:issue_updated",
			assignee:     &jira.User{AccountID: "account-1"},
			items:        []jira.ChangelogItem{{Field: "summary", ToString: stringPtr("new summary")}},
			wantStatus:   http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := jira.WebhookEvent{WebhookEvent: tt.webhookEvent}
			event.Issue.Key = "DEMO-1"
			event.Issue.Fields.Assignee = tt.assignee
			event.Changelog.Items = tt.items
			payload, err := json.Marshal(event)
			if err != nil {
				t.Fatalf("marshal webhook event: %v", err)
			}

			request := httptest.NewRequest(http.MethodPost, "/webhooks/jira", bytes.NewReader(payload))
			response := httptest.NewRecorder()
			var logs bytes.Buffer
			logger := log.New(&logs, "", 0)
			reader := &fakeIssueReader{}
			writer := &fakeIssueWriter{}
			generator := &fakePRDGenerator{}

			handleJiraWebhook(response, request, "account-1", reader, writer, generator, logger)

			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, tt.wantStatus)
			}
			if got := logs.String(); got != tt.wantLog {
				t.Fatalf("log = %q, want %q", got, tt.wantLog)
			}
			if tt.wantLog == "" && (reader.called || writer.called || generator.called) {
				t.Fatal("ignored webhook called a workflow dependency")
			}
			if tt.wantLog != "" && (!reader.called || !writer.called || !generator.called) {
				t.Fatal("authorized webhook did not execute the workflow dependencies")
			}
		})
	}
}

func TestHandleJiraWebhookRejectsInvalidJSON(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/webhooks/jira", bytes.NewBufferString("{"))
	response := httptest.NewRecorder()
	var logs bytes.Buffer
	reader := &fakeIssueReader{}
	writer := &fakeIssueWriter{}
	generator := &fakePRDGenerator{}

	handleJiraWebhook(response, request, "account-1", reader, writer, generator, log.New(&logs, "", 0))

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusBadRequest)
	}
}

func TestHandleJiraWebhookLogsSafePRDError(t *testing.T) {
	event := jira.WebhookEvent{WebhookEvent: "jira:issue_updated"}
	event.Issue.Key = "DEMO-1"
	event.Issue.Fields.Assignee = &jira.User{AccountID: "account-1"}
	status := "PRD Requested"
	event.Changelog.Items = []jira.ChangelogItem{{Field: "status", ToString: &status}}
	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal webhook event: %v", err)
	}

	request := httptest.NewRequest(http.MethodPost, "/webhooks/jira", bytes.NewReader(payload))
	response := httptest.NewRecorder()
	var logs bytes.Buffer
	generator := &fakePRDGenerator{err: errors.New("OpenAI returned HTTP status 404")}

	handleJiraWebhook(
		response,
		request,
		"account-1",
		&fakeIssueReader{},
		&fakeIssueWriter{},
		generator,
		log.New(&logs, "", 0),
	)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if !strings.Contains(logs.String(), "OpenAI returned HTTP status 404") {
		t.Fatalf("log = %q, want safe OpenAI status error", logs.String())
	}
}

func stringPtr(value string) *string {
	return &value
}
