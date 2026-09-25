package jira

import "testing"

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
