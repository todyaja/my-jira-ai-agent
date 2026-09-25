# Jira PRD Execution Design

## Scope

Connect the authorized `PRD REQUESTED` webhook path to Jira and the configured
PRD generator:

1. Fetch the full Jira issue, including its original Description.
2. Generate a PRD from the issue key, summary, and Description.
3. Preserve the human-authored Description and replace only a managed PRD block.
4. Resolve the `PRD REVIEW` transition and transition the issue after the update.

Other workflow statuses remain no-op actions. Comments, repository inspection,
PRD revisions, retries, and other workflow agents remain future work.

## Jira Client

`internal/jira` owns a small REST client configured from `JIRA_BASE_URL`,
`JIRA_EMAIL`, and `JIRA_API_TOKEN`. It uses HTTP Basic Authentication over the
configured base URL and exposes focused operations for fetching an issue,
updating its Description, listing transitions, and performing a transition.

The issue Description is handled as Jira Cloud Atlassian Document Format (ADF).
The client converts the current ADF description to plain text for the PRD input,
and writes an ADF document containing the original text plus one managed block:

```text
PRD
=============
{generated PRD}
```

If the marker already exists, only the managed PRD content is replaced. The
original text before the marker is preserved. Generated text is not logged.

## Workflow Execution

After webhook filtering confirms ownership and a transition into `PRD REQUESTED`,
the handler fetches the full issue and invokes the PRD agent. It updates the
Description, resolves the transition whose name is exactly `PRD REVIEW`, and
posts that transition. HTTP 200 is returned after the asynchronous-looking
workflow has been attempted; failures are logged with issue key and operation
and do not expose secrets or generated content.

The handler receives Jira client and PRD generator dependencies so tests use
fakes and do not call Jira or OpenAI.

## Testing

Use `httptest.Server` to verify Jira authentication, issue fetch, description
update, transition lookup, transition request, non-success responses, and ADF
conversion. Add workflow handler tests proving the authorized PRD transition
executes the operations in order and ignored events do not call dependencies.
