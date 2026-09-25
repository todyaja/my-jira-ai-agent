package jira

import (
	"strings"
	"testing"
)

func TestParseWebhookEvent(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		wantKey string
		wantErr bool
	}{
		{
			name:    "decodes webhook event",
			payload: `{"webhookEvent":"jira:issue_updated","issue":{"key":"DEMO-1"}}`,
			wantKey: "DEMO-1",
		},
		{
			name:    "rejects invalid JSON",
			payload: "{",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event, err := ParseWebhookEvent(strings.NewReader(tt.payload))
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseWebhookEvent() error = %v, want error: %v", err, tt.wantErr)
			}
			if event.Issue.Key != tt.wantKey {
				t.Fatalf("Issue.Key = %q, want %q", event.Issue.Key, tt.wantKey)
			}
		})
	}
}

func TestWebhookEventStatusChangedTo(t *testing.T) {
	to := func(value string) *string { return &value }

	tests := []struct {
		name   string
		items  []ChangelogItem
		status string
		want   bool
	}{
		{
			name:   "matches status transition destination",
			items:  []ChangelogItem{{Field: "status", FromString: to("Planning"), ToString: to("PRD Requested")}},
			status: "PRD Requested",
			want:   true,
		},
		{
			name:   "does not require previous status",
			items:  []ChangelogItem{{Field: "status", ToString: to("PRD Requested")}},
			status: "PRD Requested",
			want:   true,
		},
		{
			name:   "ignores unrelated destination",
			items:  []ChangelogItem{{Field: "status", ToString: to("Planning")}},
			status: "PRD Requested",
			want:   false,
		},
		{
			name:   "ignores ordinary field changes",
			items:  []ChangelogItem{{Field: "summary", ToString: to("PRD Requested")}},
			status: "PRD Requested",
			want:   false,
		},
		{
			name:   "ignores missing destination",
			items:  []ChangelogItem{{Field: "status"}},
			status: "PRD Requested",
			want:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := WebhookEvent{}
			event.Changelog.Items = tt.items
			if got := event.StatusChangedTo(tt.status); got != tt.want {
				t.Fatalf("StatusChangedTo(%q) = %v, want %v", tt.status, got, tt.want)
			}
		})
	}
}
