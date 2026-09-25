# PRD Workflow Trigger Design

## Scope

Add the first authorized Jira workflow trigger: an issue assigned to the configured
`JIRA_ACCOUNT_ID` is eligible for the PRD workflow only when a Jira issue-updated
webhook reports a status change into `PRD Requested`.

This change does not call an AI/LLM, fetch or update Jira issues, or expose values
from `.env`.

## Architecture

Webhook payload parsing and status-transition interpretation remain in
`internal/jira`. `WebhookEvent.StatusChangedTo(status string) bool` scans the
changelog for a status-field change whose destination (`ToString`) matches the
requested status. The previous status and `FromString` are not required.

The HTTP handler in `cmd/server/main.go` performs the authorization and filtering:

1. Decode the webhook payload.
2. Ignore events other than `jira:issue_updated`.
3. Ignore unassigned issues and issues whose assignee account ID differs from
   `JIRA_ACCOUNT_ID`.
4. Ignore events without a transition into `PRD Requested`.
5. Log `PRD WORKFLOW TRIGGERED FOR <issue-key>` for matching events.
6. Return HTTP 200 for all valid ignored events and matching events.

Invalid JSON remains an HTTP 400 response.

## Testing

Use table-driven unit tests for `StatusChangedTo`, covering matching status
changes, unrelated destinations, non-status field changes, and missing
destinations. Add handler tests covering event type, assignee, transition, and
ordinary field-change filters, including the expected status code and trigger
behavior.
