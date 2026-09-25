# OpenAI PRD Agent Design

## Scope

Add a configurable OpenAI-backed implementation of the existing
`workflow.PRDGenerator` contract. The provider generates a structured PRD from
the Jira issue summary and original Description, explicitly placing missing
requirements under `Open Questions` instead of inventing requirements.

The workflow dispatcher keeps one separate action function per canonical status.
Only the `PRD REQUESTED` action invokes the PRD agent in this slice. Other
status actions remain explicit no-op logging functions until their behavior is
implemented.

This slice does not update Jira, append the PRD to the Description, transition
statuses, fetch comments, or implement other workflow agents.

## Configuration

Read configuration from environment variables loaded by the existing `.env`
handling:

```env
OPENAI_API_KEY=
OPENAI_MODEL=gpt-5.6-luna
OPENAI_BASE_URL=https://api.openai.com/v1
OPENAI_TIMEOUT_SECONDS=60
```

`OPENAI_MODEL` is configurable and defaults to `gpt-5.6-luna`; the API may
reject that model if it is not available to the configured account. The API key
must never be logged, returned in an error, or committed. `.env.example` may
contain names and safe defaults but no secret value.

## Architecture

`internal/agent/openai.go` implements `workflow.PRDGenerator` using an HTTP
client and the configured OpenAI-compatible base URL. The provider sends the
issue key, summary, and original Description in a tightly scoped PRD prompt and
returns generated text without logging it. HTTP status failures, malformed
responses, missing API keys, and request timeouts return errors.

`internal/workflow` owns the status constants and dispatcher. Each canonical
status maps to a separate action function. The `PRD REQUESTED` action receives a
`PRDGenerator`; all other actions only log that they are not implemented. The
dispatcher does not decide ownership or perform Jira writes.

## Testing

Use an `httptest.Server` for OpenAI provider tests. Verify the configured model,
authorization header, request input, successful response parsing, HTTP errors,
malformed responses, and timeout/error propagation without making real network
calls. Use a fake `PRDGenerator` in workflow tests to verify only the
`PRD REQUESTED` action invokes the agent and each other status invokes its own
no-op action.
