# Status Dispatcher and PRD Generator Design

## Scope

Add a readable workflow status model and dispatcher for the canonical Jira
statuses. The dispatcher is invoked only after webhook ownership and status
authorization checks have passed.

For this slice, every status except `PRD REQUESTED` logs that no action is
implemented. `PRD REQUESTED` calls a model-agnostic PRD generator interface.
The real AI provider, Jira REST reads/writes, Description PRD block, and Jira
status transitions are separate follow-up work.

## Architecture

`internal/workflow` owns the canonical `Status` constants and dispatch logic.
The dispatcher accepts a `PRDGenerator` dependency and a logger. It switches on
the status and invokes the generator only for `PRD REQUESTED`; all other
statuses produce a simple informational log. Unknown statuses are rejected
instead of silently authorizing work.

The generator interface accepts a `PRDInput` containing the issue key, summary,
and current description, and returns generated PRD text or an error. Tests use a
fake generator to prove the PRD branch is selected without requiring an LLM.

## Safety and Future Integration

The dispatcher does not determine ownership and does not perform Jira updates.
The existing webhook handler remains responsible for assignee authorization and
status-transition detection. A later Jira client will fetch the full issue and
persist the generated PRD in the managed Description section while preserving
the human-authored content.

## Testing

Use table-driven tests for all eleven canonical statuses. Verify that each
known non-PRD status logs the no-op action, `PRD REQUESTED` invokes the fake
generator with the input unchanged, generator errors are returned, and unknown
statuses return an error without invoking the generator.
