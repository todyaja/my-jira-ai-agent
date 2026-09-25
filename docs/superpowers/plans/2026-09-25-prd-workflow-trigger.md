# PRD Workflow Trigger Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Detect and log the authorized transition of an assigned Jira issue into `PRD Requested` while returning HTTP 200 for valid ignored events.

**Architecture:** Keep changelog interpretation in `internal/jira` through `WebhookEvent.StatusChangedTo`. Keep event filtering, assignee authorization, and the trigger log in a testable HTTP handler function in `cmd/server/main.go`. Do not add Jira REST, AI, or Jira mutation behavior.

**Tech Stack:** Go 1.26.5, `net/http`, `httptest`, standard `testing` package, `log`.

## Global Constraints

- Keep webhook parsing in `internal/jira`.
- Treat assignee as ownership and the status transition as authorization.
- Ignore non-`jira:issue_updated` events, unassigned issues, other assignees, unrelated status changes, and ordinary field changes.
- Return HTTP 200 for ignored valid webhook events.
- Log exactly `PRD WORKFLOW TRIGGERED FOR <issue-key>` for a matching event.
- Do not add an AI/LLM integration or modify Jira.
- Do not expose or print secrets from `.env`.
- Run `gofmt` and `go test ./...` before completion.

---

### Task 1: Add Status Transition Detection

**Files:**
- Modify: `internal/jira/webhook.go`
- Create: `internal/jira/webhook_test.go`

**Interfaces:**
- Produces: `func (e WebhookEvent) StatusChangedTo(status string) bool`

- [ ] **Step 1: Write the failing table-driven tests**

Add tests in `internal/jira/webhook_test.go` that construct `WebhookEvent` values directly and verify:

```go
func TestWebhookEventStatusChangedTo(t *testing.T) {
	to := func(value string) *string { return &value }

	tests := []struct {
		name    string
		items   []ChangelogItem
		status  string
		want    bool
	}{
		{
			name:   "matches status transition destination",
			items:  []ChangelogItem{{Field: "status", ToString: to("PRD Requested")}},
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
```

The duplicate-looking destination case explicitly documents that `FromString` is irrelevant; include a non-empty `FromString` in the first case if desired, but retain one case with it absent.

- [ ] **Step 2: Run the focused test and verify it fails**

Run: `go test ./internal/jira`

Expected: FAIL because `WebhookEvent.StatusChangedTo` is not yet defined.

- [ ] **Step 3: Implement the minimal method**

Add this method to `internal/jira/webhook.go`:

```go
func (e WebhookEvent) StatusChangedTo(status string) bool {
	for _, item := range e.Changelog.Items {
		if item.Field == "status" && item.ToString != nil && *item.ToString == status {
			return true
		}
	}

	return false
}
```

- [ ] **Step 4: Run the focused test and verify it passes**

Run: `go test ./internal/jira`

Expected: PASS.

- [ ] **Step 5: Commit the isolated model change**

Run: `git add internal/jira/webhook.go internal/jira/webhook_test.go; git commit -m "feat: detect Jira status transitions"`

Expected: A commit containing only the status predicate and its tests.

### Task 2: Filter Webhook Events and Trigger PRD Logging

**Files:**
- Modify: `cmd/server/main.go`
- Create: `cmd/server/main_test.go`

**Interfaces:**
- Produces: `func handleJiraWebhook(w http.ResponseWriter, r *http.Request, accountID string, logger *log.Logger)` for direct handler tests.

- [ ] **Step 1: Write handler tests for the required filters**

Create a helper that marshals a `jira.WebhookEvent` and sends it to `handleJiraWebhook` with an `httptest.ResponseRecorder` and a buffer-backed logger. Cover these cases:

```go
tests := []struct {
	name          string
	webhookEvent  string
	assignee      *jira.User
	items         []jira.ChangelogItem
	wantStatus    int
	wantLog       string
}{
	{
		name:         "triggers assigned issue entering PRD Requested",
		webhookEvent: "jira:issue_updated",
		assignee:     &jira.User{AccountID: "account-1"},
		items:        []jira.ChangelogItem{{Field: "status", ToString: stringPtr("PRD Requested")}},
		wantStatus:   http.StatusOK,
		wantLog:      "PRD WORKFLOW TRIGGERED FOR DEMO-1\n",
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
```

Set `event.Issue.Key = "DEMO-1"` in each test event. Assert every response is 200 and only the matching case contains the exact trigger line. Add a separate invalid-JSON test asserting HTTP 400.

- [ ] **Step 2: Run the focused handler tests and verify they fail**

Run: `go test ./cmd/server`

Expected: FAIL because the handler function and non-event filtering are not yet implemented.

- [ ] **Step 3: Extract the handler and add the event-type filter**

Move the existing webhook body into:

```go
func handleJiraWebhook(w http.ResponseWriter, r *http.Request, accountID string, logger *log.Logger) {
```

Decode JSON first. If `event.WebhookEvent != "jira:issue_updated"`, write `http.StatusOK` and return. Preserve the existing invalid-payload response as HTTP 400.

- [ ] **Step 4: Apply authorization and trigger logging**

In the extracted handler, retain the nil-assignee and account ID checks, then call `event.StatusChangedTo("PRD Requested")`. For a match, log only the required trigger message through the injected logger:

```go
logger.Printf("PRD WORKFLOW TRIGGERED FOR %s", event.Issue.Key)
```

Return HTTP 200 after every valid path. Remove the existing verbose event and changelog logging so secrets or unrelated payload details are not emitted.

- [ ] **Step 5: Register the extracted handler from `main`**

Replace the inline `/webhooks/jira` closure with a call to `handleJiraWebhook`, passing `myAccountID` and `log.Default()` while preserving the existing `.env` loading and required-account-ID startup check.

- [ ] **Step 6: Run the focused handler tests and verify they pass**

Run: `go test ./cmd/server`

Expected: PASS, including the matching trigger and all ignored-event cases.

- [ ] **Step 7: Commit the handler change**

Run: `git add cmd/server/main.go cmd/server/main_test.go; git commit -m "feat: trigger PRD workflow from Jira status"`

Expected: A commit containing only the webhook filters, trigger log, and handler tests.

### Task 3: Format and Run the Full Verification Suite

**Files:**
- Modify: `internal/jira/webhook.go`, `internal/jira/webhook_test.go`, `cmd/server/main.go`, `cmd/server/main_test.go` only if formatting changes are required.

- [ ] **Step 1: Format the Go files**

Run: `gofmt -w internal/jira/webhook.go internal/jira/webhook_test.go cmd/server/main.go cmd/server/main_test.go`

Expected: Command succeeds without output.

- [ ] **Step 2: Run all tests**

Run: `go test ./...`

Expected: PASS for every package.

- [ ] **Step 3: Inspect the final worktree**

Run: `git status --short; git diff --check`

Expected: No whitespace errors. The only uncommitted changes, if any, are formatting changes made in this task; do not alter the pre-existing `cmd/server/main.go` work before the implementation commits.
