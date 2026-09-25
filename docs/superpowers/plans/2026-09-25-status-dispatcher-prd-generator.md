# Status Dispatcher and PRD Generator Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add canonical Jira workflow statuses and a testable dispatcher that invokes a model-agnostic PRD generator only for `PRD REQUESTED`.

**Architecture:** `internal/workflow` owns status constants, PRD input, generator interface, and dispatch behavior. The dispatcher does not authorize users or call Jira; the existing webhook handler remains responsible for ownership and transition filtering. A later task will provide a real AI generator and Jira issue persistence.

**Tech Stack:** Go 1.26.5, `context`, `errors`, `log`, standard `testing` package.

## Global Constraints

- `internal/workflow` owns the canonical `Status` constants and dispatch logic.
- Every canonical status except `PRD REQUESTED` logs that no action is implemented.
- `PRD REQUESTED` calls a model-agnostic PRD generator interface.
- Unknown statuses return an error and never invoke the generator.
- The dispatcher does not determine ownership and does not perform Jira updates.
- Do not add a real AI provider, Jira REST calls, Description writes, or status transitions in this slice.
- Do not log generated PRD content or secrets.
- Run `gofmt` and `go test ./...` before completion.

---

### Task 1: Add Canonical Statuses and PRD Generator Contract

**Files:**
- Create: `internal/workflow/status.go`
- Create: `internal/workflow/prd.go`
- Create: `internal/workflow/status_test.go`

**Interfaces:**
- Produces: `type Status string` and eleven exported status constants.
- Produces: `type PRDInput struct { IssueKey string; Summary string; Description string }`.
- Produces: `type PRDGenerator interface { Generate(context.Context, PRDInput) (string, error) }`.

- [ ] **Step 1: Write status and contract tests**

Create tests that assert the canonical string values:

```go
func TestStatuses(t *testing.T) {
	tests := []struct {
		name string
		got  Status
		want string
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
			if string(tt.got) != tt.want {
				t.Fatalf("got %q, want %q", tt.got, tt.want)
			}
		})
	}
}
```

Also compile a fake implementation of `PRDGenerator` in the test file. The fake should record the received `PRDInput`, return a configurable string, and return a configurable error. This verifies the interface can be used without an LLM.

- [ ] **Step 2: Run the focused tests and verify they fail**

Run: `go test ./internal/workflow`

Expected: FAIL because the `internal/workflow` package and status constants do not yet exist.

- [ ] **Step 3: Add the status constants**

Define:

```go
type Status string

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

- [ ] **Step 4: Add the PRD input and generator interface**

Define the exact contract:

```go
type PRDInput struct {
	IssueKey    string
	Summary     string
	Description string
}

type PRDGenerator interface {
	Generate(context.Context, PRDInput) (string, error)
}
```

Do not add provider-specific fields or dependencies.

- [ ] **Step 5: Run the focused tests and verify they pass**

Run: `go test ./internal/workflow`

Expected: PASS.

- [ ] **Step 6: Commit the status contract**

Run: `git add internal/workflow/status.go internal/workflow/prd.go internal/workflow/status_test.go; git commit -m "feat: define Jira workflow statuses"`

Expected: A commit containing only the status constants, PRD contract, and tests.

### Task 2: Implement the Status Dispatcher

**Files:**
- Create: `internal/workflow/dispatcher.go`
- Create: `internal/workflow/dispatcher_test.go`

**Interfaces:**
- Consumes: `Status`, `PRDInput`, and `PRDGenerator` from Task 1.
- Produces: `func Dispatch(ctx context.Context, status Status, input PRDInput, generator PRDGenerator, logger *log.Logger) error`.

- [ ] **Step 1: Write table-driven dispatcher tests**

Use a recording fake generator and a buffer-backed logger. Cover all eleven canonical statuses. For every status except `StatusPRDRequested`, assert the generator was not called, the return error is nil, and the log contains the status and a no-op message. For `StatusPRDRequested`, assert the generator receives the exact `PRDInput`, returns nil error, and the log indicates PRD generation completed without including the returned PRD text.

Add these error cases:

```go
func TestDispatchReturnsGeneratorError(t *testing.T) {
	wantErr := errors.New("generator failed")
	gotErr := Dispatch(context.Background(), StatusPRDRequested, PRDInput{}, fakeGenerator{err: wantErr}, log.New(io.Discard, "", 0))
	if !errors.Is(gotErr, wantErr) {
		t.Fatalf("Dispatch() error = %v, want %v", gotErr, wantErr)
	}
}

func TestDispatchRejectsUnknownStatus(t *testing.T) {
	fake := &recordingGenerator{}
	err := Dispatch(context.Background(), Status("UNKNOWN"), PRDInput{}, fake, log.New(io.Discard, "", 0))
	if err == nil {
		t.Fatal("Dispatch() error = nil, want error")
	}
	if fake.called {
		t.Fatal("generator called for unknown status")
	}
}
```

- [ ] **Step 2: Run the dispatcher tests and verify they fail**

Run: `go test ./internal/workflow -run TestDispatch`

Expected: FAIL because `Dispatch` is not yet defined.

- [ ] **Step 3: Implement the switch dispatcher**

Implement the exact behavior:

```go
func Dispatch(ctx context.Context, status Status, input PRDInput, generator PRDGenerator, logger *log.Logger) error {
	switch status {
	case StatusPRDRequested:
		if generator == nil {
			return errors.New("PRD generator is nil")
		}
		if _, err := generator.Generate(ctx, input); err != nil {
			return err
		}
		logger.Printf("PRD generation completed for %s", input.IssueKey)
		return nil
	case StatusBacklog, StatusPRDReview, StatusPlanning, StatusPlanReview,
		StatusReadyToImplement, StatusImplementing, StatusAIReview, StatusPRReady,
		StatusDone, StatusAIFailed:
		logger.Printf("No AI action implemented for status %s", status)
		return nil
	default:
		return fmt.Errorf("unknown workflow status %q", status)
	}
}
```

Call `generator.Generate` directly, return its error, and log only after success. The dispatcher requires a non-nil logger from its caller; tests always provide one, so no nil-logger fallback is needed in this slice.

- [ ] **Step 4: Run the dispatcher tests and verify they pass**

Run: `go test ./internal/workflow -run TestDispatch`

Expected: PASS.

- [ ] **Step 5: Run all repository tests**

Run: `go test ./...`

Expected: PASS.

- [ ] **Step 6: Commit the dispatcher**

Run: `git add internal/workflow/dispatcher.go internal/workflow/dispatcher_test.go; git commit -m "feat: dispatch workflow status actions"`

Expected: A commit containing only the dispatcher and its tests.

### Task 3: Format and Verify

**Files:**
- Modify: `internal/workflow/*.go` only if formatting changes are required.

- [ ] **Step 1: Format the new Go files**

Run: `gofmt -w internal/workflow/status.go internal/workflow/prd.go internal/workflow/status_test.go internal/workflow/dispatcher.go internal/workflow/dispatcher_test.go`

Expected: Command succeeds without output.

- [ ] **Step 2: Run the complete test suite**

Run: `go test ./...`

Expected: PASS for every package.

- [ ] **Step 3: Check the diff**

Run: `git diff --check`

Expected: No whitespace errors.
