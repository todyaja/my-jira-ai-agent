# OpenCode PRD Agent Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Use the authenticated local OpenCode CLI to generate PRDs without consuming direct OpenAI API credits.

**Architecture:** Add `internal/agent.CommandPRDGenerator` implementing the existing `workflow.PRDGenerator` interface. The server constructs it from environment configuration and passes it through the unchanged Jira/workflow execution path.

**Tech Stack:** Go 1.26.5, `os/exec`, `context`, existing workflow/Jira packages.

## Global Constraints

- Default executable: `opencode`.
- Default model: `openai/gpt-5.6-luna`.
- Use `exec.CommandContext` without a shell.
- Do not log Jira Description, prompt, generated PRD, stderr, credentials, or provider configuration.
- Return safe errors for non-zero exit, empty stdout, and timeout.
- Preserve existing Jira authorization and Description update behavior.
- Run `gofmt`, `go test ./...`, and `go vet ./...`.

---

### Task 1: Implement Command PRD Generator

**Files:**
- Create: `internal/agent/opencode.go`
- Create: `internal/agent/opencode_test.go`

**Interfaces:**
- Produces: `type OpenCodeConfig struct { Executable, Model string; Timeout time.Duration }`.
- Produces: `func NewOpenCodePRDGenerator(config OpenCodeConfig) (*OpenCodePRDGenerator, error)`.
- Produces: `OpenCodePRDGenerator.Generate(context.Context, workflow.PRDInput) (string, error)`.

- [ ] **Step 1: Write failing command tests**

Use a test helper process controlled by an environment variable. Verify the generator invokes `run`, `--format default`, `--model`, and the configured model; passes a prompt containing issue key, summary, Description, and Open Questions requirements; returns stdout; rejects empty stdout; returns a safe non-zero exit error without stderr; and returns a timeout error.

- [ ] **Step 2: Run focused tests and verify failure**

Run: `go test ./internal/agent -run TestOpenCode`

Expected: FAIL because the command generator does not exist.

- [ ] **Step 3: Implement the generator**

Build the prompt from `workflow.PRDInput`, call:

```go
exec.CommandContext(ctx, config.Executable, "run", "--format", "default", "--model", config.Model, prompt)
```

Capture stdout and stderr separately. Return trimmed stdout as the PRD. Do not include stderr or prompt content in returned errors.

- [ ] **Step 4: Run focused and full tests**

Run: `go test ./internal/agent`; then `go test ./...`.

Expected: PASS.

- [ ] **Step 5: Commit the generator**

Run: `git add internal/agent/opencode.go internal/agent/opencode_test.go; git commit -m "feat: add OpenCode PRD generator"`

### Task 2: Configure OpenCode as the Default Provider

**Files:**
- Modify: `cmd/server/main.go`
- Modify: `.env.example`
- Create: `internal/agent/opencode_config.go`
- Create: `internal/agent/opencode_config_test.go`

**Interfaces:**
- Produces: `func LoadOpenCodeConfig(getenv func(string) string) (OpenCodeConfig, error)`.

- [ ] **Step 1: Write configuration tests**

Assert blank executable uses `opencode`, blank model uses `openai/gpt-5.6-luna`, blank timeout uses 60 seconds, invalid timeout fails, and configured values are preserved.

- [ ] **Step 2: Implement configuration loading**

Read `AGENT_COMMAND`, `OPENCODE_MODEL`, and `OPENCODE_TIMEOUT_SECONDS`. Use `AGENT_COMMAND` only when it is exactly one executable path; do not parse or execute shell fragments. Keep the default executable `opencode`.

- [ ] **Step 3: Build the OpenCode generator in main**

Change `buildWorkflowDependencies` to construct `NewOpenCodePRDGenerator(LoadOpenCodeConfig(os.Getenv))`. Do not require `OPENAI_API_KEY` for startup when OpenCode is the selected provider. Keep the direct OpenAI generator available but unused by default.

- [ ] **Step 4: Update the environment template**

Add safe fields:

```env
OPENCODE_MODEL=openai/gpt-5.6-luna
OPENCODE_TIMEOUT_SECONDS=120
```

Do not add credentials or remove existing Jira/OpenAI fields.

- [ ] **Step 5: Run all tests and commit**

Run: `gofmt -w cmd/server internal/agent`; `go test ./...`; `go vet ./...`.

Then run: `git add cmd/server/main.go .env.example internal/agent/opencode_config.go internal/agent/opencode_config_test.go; git commit -m "feat: use OpenCode for PRD generation"`.

### Task 3: Verify Runtime Command Safety

**Files:**
- Modify: `cmd/server/main_test.go` only if integration coverage is needed.

- [ ] **Step 1: Verify provider injection**

Assert the authorized webhook path still reaches `ExecutePRDRequested` with a generator and ignored events do not launch a command.

- [ ] **Step 2: Run final checks**

Run: `gofmt -w cmd/server internal/agent internal/jira internal/workflow`; `go test ./...`; `go vet ./...`; `git diff --check`.

- [ ] **Step 3: Audit secrets and shell usage**

Run: `git grep -n -I -E 'sk-[A-Za-z0-9_-]{20,}' -- ':!*.md'` and inspect the command construction for shell invocation. Expected: no real key matches and no `cmd /c`, `sh -c`, or equivalent.
