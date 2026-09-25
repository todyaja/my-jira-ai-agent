# Workflow Actions and OpenAI PRD Agent Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement one explicit action function per Jira workflow status and connect the `PRD REQUESTED` action to a configurable OpenAI-backed PRD generator.

**Architecture:** `internal/workflow` owns canonical statuses, `PRDInput`, `PRDGenerator`, and one action function per status. `internal/agent` owns OpenAI HTTP configuration and generation. The dispatcher receives the generator but does not fetch or update Jira; the existing webhook handler remains the authorization boundary.

**Tech Stack:** Go 1.26.5, standard `net/http`, `context`, `encoding/json`, `httptest`, `testing`, and existing `godotenv`.

## Global Constraints

- `OPENAI_MODEL` defaults to `gpt-5.6-luna` and remains configurable.
- `OPENAI_API_KEY` must never be logged, returned in an error, or committed.
- The real AI provider uses `OPENAI_BASE_URL` defaulting to `https://api.openai.com/v1`.
- `OPENAI_TIMEOUT_SECONDS` defaults to `60`.
- Use one separate workflow action function for each canonical status.
- Only `PRD REQUESTED` invokes the PRD generator; other actions log and return nil.
- Unknown statuses return an error and invoke no action.
- Do not fetch or update Jira, append PRD content to Description, or transition Jira statuses in this slice.
- Do not log generated PRD content or secrets.
- Run `gofmt` and `go test ./...` before completion.

---

### Task 1: Implement Workflow Status Actions

**Files:**
- Create: `internal/workflow/status.go`
- Create: `internal/workflow/prd.go`
- Create: `internal/workflow/actions.go`
- Create: `internal/workflow/actions_test.go`

**Interfaces:**
- Produces: `type Status string` with eleven canonical exported constants.
- Produces: `type PRDInput struct { IssueKey, Summary, Description string }`.
- Produces: `type PRDGenerator interface { Generate(context.Context, PRDInput) (string, error) }`.
- Produces: `func Dispatch(context.Context, Status, PRDInput, PRDGenerator, *log.Logger) error`.

- [ ] **Step 1: Write failing tests**

Create a recording fake generator and table-driven tests for all statuses. Assert exact status strings. Assert each non-PRD status logs a no-op and does not call the generator. Assert `PRD REQUESTED` passes the unchanged input to the generator, returns its error unchanged, and logs only completion without generated text. Assert unknown status returns an error without calling any action.

- [ ] **Step 2: Run focused tests and verify failure**

Run: `go test ./internal/workflow`

Expected: FAIL because the package and symbols do not yet exist.

- [ ] **Step 3: Define statuses and generator contract**

Use these exact values:

```go
const (
	StatusBacklog          Status = "BACKLOG"
	StatusPRDRequested     Status = "PRD REQUESTED"
	StatusPRDReview        Status = "PRD REVIEW"
	StatusPlanning         Status = "PLANNING"
	StatusPlanReview       Status = "PLAN REVIEW"
	StatusReadyToImplement Status = "READY TO IMPLEMENT"
	StatusImplementing     Status = "IMPLEMENTING"
	StatusAIReview         Status = "AI REVIEW"
	StatusPRReady          Status = "PR READY"
	StatusDone             Status = "DONE"
	StatusAIFailed         Status = "AI FAILED"
)
```

Define `PRDInput` and `PRDGenerator` using `context.Context`; do not add provider-specific fields.

- [ ] **Step 4: Implement separate action functions and dispatcher**

Define one function for each status, with the same shape:

```go
type action func(context.Context, PRDInput) error

func backlogAction(logger *log.Logger) action
func prdRequestedAction(generator PRDGenerator, logger *log.Logger) action
func prdReviewAction(logger *log.Logger) action
func planningAction(logger *log.Logger) action
func planReviewAction(logger *log.Logger) action
func readyToImplementAction(logger *log.Logger) action
func implementingAction(logger *log.Logger) action
func aiReviewAction(logger *log.Logger) action
func prReadyAction(logger *log.Logger) action
func doneAction(logger *log.Logger) action
func aiFailedAction(logger *log.Logger) action
```

Each no-op action logs `No AI action implemented for status <STATUS>` and returns nil. `prdRequestedAction` calls `generator.Generate`, returns its error without logging the result, and logs `PRD generation completed for <issue-key>` only on success. `Dispatch` selects exactly one action with a switch and rejects unknown statuses.

- [ ] **Step 5: Run focused tests and verify success**

Run: `go test ./internal/workflow`

Expected: PASS.

- [ ] **Step 6: Commit workflow actions**

Run: `git add internal/workflow; git commit -m "feat: add workflow status actions"`

Expected: Commit contains only workflow status, contract, action, and test files.

### Task 2: Implement Configurable OpenAI PRD Generator

**Files:**
- Create: `internal/agent/openai.go`
- Create: `internal/agent/openai_test.go`

**Interfaces:**
- Consumes: `workflow.PRDInput` and produces `workflow.PRDGenerator`.
- Produces: `type OpenAIConfig struct { APIKey, Model, BaseURL string; Timeout time.Duration }`.
- Produces: `func NewOpenAIPRDGenerator(config OpenAIConfig) (*OpenAIPRDGenerator, error)`.

- [ ] **Step 1: Write HTTP-backed provider tests**

Use `httptest.Server` and assert the provider sends `Authorization: Bearer <test-key>`, the configured model, and input containing issue key, summary, and Description. Return a Chat Completions-shaped JSON response and assert generated text is extracted. Add tests for missing key, HTTP non-2xx, malformed JSON, missing choices/content, and request timeout. Never use or print a real key.

- [ ] **Step 2: Run provider tests and verify failure**

Run: `go test ./internal/agent`

Expected: FAIL because the provider does not yet exist.

- [ ] **Step 3: Implement configuration defaults and request client**

Use defaults:

```go
const (
	DefaultOpenAIModel   = "gpt-5.6-luna"
	DefaultOpenAIBaseURL = "https://api.openai.com/v1"
)
```

Normalize a blank model/base URL to defaults and reject a blank API key. Trim a trailing slash from the base URL and POST to `/chat/completions`. Use the configured timeout with an `http.Client`.

- [ ] **Step 4: Implement the constrained PRD request**

Send a system message requiring a structured PRD with sections for Problem, Goals, Non-Goals, Functional Requirements, Non-Functional Requirements, Acceptance Criteria, Risks/Assumptions, and Open Questions. Instruct the model to preserve uncertainty and never invent missing business requirements. Send the issue key, summary, and original Description as user content. Parse only the first returned choice’s message content. Do not log request bodies, response bodies, API keys, or generated PRD text.

- [ ] **Step 5: Run provider tests and verify success**

Run: `go test ./internal/agent`

Expected: PASS.

- [ ] **Step 6: Commit the provider**

Run: `git add internal/agent; git commit -m "feat: add OpenAI PRD generator"`

Expected: Commit contains only provider code and tests.

### Task 3: Wire Configuration and Verify Integration

**Files:**
- Modify: `cmd/server/main.go`
- Modify: `.env.example`
- Create: `internal/agent/config.go`
- Create: `internal/agent/config_test.go`

**Interfaces:**
- Produces: `func LoadOpenAIConfig(getenv func(string) string) (OpenAIConfig, error)`.

- [ ] **Step 1: Write configuration tests**

Assert empty model/base URL/timeout use the documented defaults, valid timeout parses as seconds, invalid timeout returns an error, and missing `OPENAI_API_KEY` returns an error without including a key value.

- [ ] **Step 2: Implement environment loading**

Read `OPENAI_API_KEY`, `OPENAI_MODEL`, `OPENAI_BASE_URL`, and `OPENAI_TIMEOUT_SECONDS`. Keep the API key in memory only and never log it. Preserve `.env` loading through the existing startup path.

- [ ] **Step 3: Wire the provider into the PRD action construction**

Construct the provider only where the application builds workflow dependencies. Do not make startup call OpenAI or modify Jira. If the provider is not yet wired into the webhook action, keep the dispatcher independently testable and document the dependency boundary in code rather than adding a fake production fallback.

- [ ] **Step 4: Run all tests and formatting**

Run: `gofmt -w internal/workflow internal/agent cmd/server/main.go`; then run `go test ./...`.

Expected: PASS for all packages.

- [ ] **Step 5: Inspect secrets and diff**

Run: `git diff --check; git status --short; git grep -n -I -E 'sk-[A-Za-z0-9_-]{20,}' -- ':!*.md'`.

Expected: no whitespace errors, no API key matches, and no `.env` content staged.

- [ ] **Step 6: Commit integration verification**

Run: `git add cmd/server/main.go .env.example internal/agent/config.go internal/agent/config_test.go; git commit -m "feat: configure PRD agent"`

Expected: Commit contains only safe configuration and wiring changes.
