# Jira PRD Execution Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Execute the authorized `PRD REQUESTED` action by fetching the full Jira issue, generating a PRD, preserving the original Description while updating a managed PRD block, and transitioning to `PRD REVIEW`.

**Architecture:** `internal/jira` owns authenticated Jira REST operations and ADF/plain-text conversion. `internal/workflow` owns the action orchestration through narrow interfaces. `cmd/server` injects Jira and OpenAI dependencies after webhook ownership and status-transition authorization. All external calls are tested through fakes or `httptest.Server`.

**Tech Stack:** Go 1.26.5, `net/http`, `encoding/json`, Jira Cloud REST v3, Atlassian Document Format, existing OpenAI generator.

## Global Constraints

- Use `JIRA_BASE_URL`, `JIRA_EMAIL`, and `JIRA_API_TOKEN` for Jira authentication.
- Never log API tokens, OpenAI keys, request credentials, or generated PRD content.
- Preserve human-authored Description content before the managed `PRD` marker.
- Replace an existing managed PRD block instead of creating duplicates.
- Transition only after the Description update succeeds.
- Resolve and use the transition named exactly `PRD REVIEW`.
- Valid ignored webhook events still return HTTP 200.
- Log operation failures with issue key and operation, without secrets or PRD text.
- Run `gofmt`, `go test ./...`, and `go vet ./...`.

---

### Task 1: Add Jira REST Client and Description Handling

**Files:**
- Create: `internal/jira/client.go`
- Create: `internal/jira/client_test.go`
- Create: `internal/jira/description.go`
- Create: `internal/jira/description_test.go`

**Interfaces:**
- Produces: `type Client struct` configured with base URL, email, token, HTTP client.
- Produces: `type Issue struct { Key, Summary, Description string; DescriptionADF json.RawMessage }`.
- Produces: `func (c *Client) GetIssue(ctx context.Context, key string) (Issue, error)`.
- Produces: `func (c *Client) UpdateDescription(ctx context.Context, key, description string) error`.
- Produces: `func (c *Client) TransitionTo(ctx context.Context, key, statusName string) error`.
- Produces: `func AppendPRD(description, prd string) string`.

- [ ] **Step 1: Write Description conversion tests**

Cover plain text extraction from ADF paragraphs and text nodes, empty descriptions, appending the exact marker, and replacing an existing marker while preserving the prefix. Assert no duplicate marker is produced.

- [ ] **Step 2: Write Jira client HTTP tests**

Use `httptest.Server`. Assert GET `/rest/api/3/issue/ABC-1` sends Basic Auth and decodes key, summary, and ADF description. Assert PUT `/rest/api/3/issue/ABC-1` sends an ADF body and does not expose credentials in the request body. Assert GET `/rest/api/3/issue/ABC-1/transitions` finds `PRD REVIEW`, then POSTs the matching ID. Cover non-2xx responses and missing transition names.

- [ ] **Step 3: Implement ADF/plain-text and managed block logic**

Represent the outgoing description as an ADF document with paragraph/text nodes. Use the marker `PRD\n=============\n`. On replacement, split at the first marker and retain the prefix; do not parse or modify the human prefix.

- [ ] **Step 4: Implement authenticated Jira operations**

Use `http.NewRequestWithContext`, set Basic Auth with email/token, set `Accept: application/json`, and set `Content-Type: application/json` for PUT/POST. Return errors containing HTTP status and operation only, never response bodies that could contain sensitive data. Encode transition requests as `{ "transition": { "id": "..." } }`.

- [ ] **Step 5: Run Jira package tests**

Run: `go test ./internal/jira`

Expected: PASS.

- [ ] **Step 6: Commit the Jira client**

Run: `git add internal/jira; git commit -m "feat: add Jira issue client"`

### Task 2: Add PRD Execution Orchestration

**Files:**
- Create: `internal/workflow/prd_execution.go`
- Create: `internal/workflow/prd_execution_test.go`

**Interfaces:**
- Consumes: Jira client operations and `PRDGenerator`.
- Produces: `type IssueReader interface { GetIssue(context.Context, string) (jira.Issue, error) }`.
- Produces: `type IssueWriter interface { UpdateDescription(context.Context, string, string) error; TransitionTo(context.Context, string, string) error }`.
- Produces: `func ExecutePRDRequested(ctx context.Context, key string, reader IssueReader, writer IssueWriter, generator PRDGenerator, logger *log.Logger) error`.

- [ ] **Step 1: Write orchestration tests with fakes**

Verify successful execution calls `GetIssue`, `Generate`, `UpdateDescription`, then `TransitionTo` in that order. Assert generator input uses fetched key, summary, and plain-text Description. Assert update contains the managed PRD block and transition target is `PRD REVIEW`. Add failure tests proving generation/update/transition errors stop later operations and logs include issue key plus operation without generated text.

- [ ] **Step 2: Implement the orchestration function**

Fetch the issue, call the generator, append/replace the PRD block using `jira.AppendPRD`, update the Description, then transition. Return wrapped errors while logging concise operation failures. Never log the issue Description or PRD.

- [ ] **Step 3: Run workflow tests**

Run: `go test ./internal/workflow`

Expected: PASS.

- [ ] **Step 4: Commit the orchestration**

Run: `git add internal/workflow; git commit -m "feat: execute Jira PRD workflow"`

### Task 3: Wire Authorized Webhook Execution

**Files:**
- Modify: `cmd/server/main.go`
- Modify: `cmd/server/main_test.go`

**Interfaces:**
- Consumes: `jira.Client`, `workflow.ExecutePRDRequested`, and configured OpenAI generator.

- [ ] **Step 1: Refactor handler dependencies and tests**

Make the webhook handler receive the Jira reader/writer and PRD generator dependencies. Extend handler tests with fakes proving ignored events never execute the PRD action and an authorized `PRD Requested` transition executes it.

- [ ] **Step 2: Wire Jira configuration and action**

Build the Jira client from `JIRA_BASE_URL`, `JIRA_EMAIL`, and `JIRA_API_TOKEN`. After existing webhook event, assignee, and transition filters, log `PRD WORKFLOW TRIGGERED FOR <issue-key>` and call `workflow.ExecutePRDRequested`. Log success or operation failure without payload content, then return HTTP 200 for the received valid webhook.

- [ ] **Step 3: Run application tests**

Run: `go test ./cmd/server ./internal/...`

Expected: PASS.

- [ ] **Step 4: Commit webhook wiring**

Run: `git add cmd/server/main.go cmd/server/main_test.go; git commit -m "feat: run PRD action from Jira webhook"`

### Task 4: Format, Verify, and Audit Logs

**Files:**
- Modify: Go files only if formatting is required.

- [ ] **Step 1: Format and test**

Run: `gofmt -w cmd/server internal/jira internal/workflow internal/agent`; `go test ./...`; `go vet ./...`.

Expected: all commands pass.

- [ ] **Step 2: Audit tracked files for secrets and unsafe logs**

Run: `git diff --check; git grep -n -I -E 'sk-[A-Za-z0-9_-]{20,}|JIRA_API_TOKEN=.*[^=]' -- ':!*.md'`.

Expected: no secret matches and no whitespace errors. Verify logs mention issue key/operation only, never credentials, Description, request bodies, or PRD output.

- [ ] **Step 3: Commit verification changes if needed**

Run: `git status --short` and commit only intentional formatting/test changes with `git commit -am "chore: verify Jira PRD workflow"`.
