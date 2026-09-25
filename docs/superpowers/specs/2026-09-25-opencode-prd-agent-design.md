# OpenCode PRD Agent Design

## Scope

Add a command-backed PRD generator that invokes the locally authenticated
OpenCode CLI instead of calling the OpenAI API directly. The existing workflow
and Jira execution path remain unchanged; only the generator dependency changes.

## Command Contract

The generator runs:

```text
opencode run --format default --model <OPENCODE_MODEL> <prompt>
```

The default executable is `opencode`. `OPENCODE_MODEL` is configurable and
defaults to `openai/gpt-5.6-luna`. The process inherits the server user's
OpenCode authentication and provider configuration. Standard output is the
generated PRD; standard error is captured only for safe diagnostics and is not
logged verbatim.

## Safety

Use `exec.CommandContext` without a shell to avoid command injection. The prompt
contains Jira content and must not be logged. Non-zero exit status, empty output,
and context timeout return errors that identify only the operation and exit
status. The existing OpenAI generator remains available for direct API usage,
but OpenCode becomes the application default.

## Testing

Inject the executable path and use a test helper process or fake command to
verify arguments, prompt delivery, stdout extraction, empty output, non-zero
exit, timeout, and stderr suppression. Verify the returned generator still
implements `workflow.PRDGenerator`.
