---
name: code-review
description: How to review an issue's branch against its PRD and TRD - what to check, how to verify, how to grade findings, when to approve, and the review format.
---
# Reviewing a branch

## 1. Understand what should have been built

- Read the PRD's functional requirements and acceptance criteria, and the TRD's proposed changes, implementation plan and testing strategy.
- If this is not the first round, read the earlier reviews. Your first job is to confirm each earlier blocking finding is actually fixed.

## 2. Read the change in context

- Start from the diff in the request; if it was truncated, use `git diff` against the base branch for the rest.
- For every changed file, read the whole file, not just the hunks, and read the callers and tests of anything whose behavior changed.

## 3. Verify, do not assume

Run the repository's checks - the test suite, the linter, and the type check or build. If dependencies are missing and you cannot install them, say which checks you could not run. A failing check is a blocking finding.

## 4. What to look for

- **Requirements** - every PRD functional requirement and acceptance criterion is implemented, and nothing outside scope was added.
- **TRD** - the plan was followed, or deviations are justified.
- **Correctness** - logic errors, edge cases (empty, missing, invalid, boundaries), error handling, state and data consistency, race conditions.
- **Tests** - new behavior is covered by meaningful tests that would fail if it broke; no weakened or skipped tests.
- **Security** - input validation, injection, secrets, unsafe data handling.
- **Conventions and maintainability** - matches the codebase's patterns, naming and structure; no dead code, duplication or needless complexity.
- **Performance** - only where it plausibly matters.

## 5. Grade each finding

- **Blocking** - a bug, a missing or broken requirement, a failing check, missing tests for new behavior, or a security problem.
- **Non-blocking** - style, naming, readability or small improvements that do not affect behavior.

Report only real findings. Do not pad the review, invent problems, or re-raise findings that are fixed. Every finding names the file and line, what is wrong, and what to do instead.

## 6. Verdict

- `approved` - there are no blocking findings (non-blocking ones may remain).
- `changes_requested` - there is at least one blocking finding.

Judge the code, not the round number: on the last round, still request changes if something is blocking. Open findings then go to a human with the pull request.

## Answer format

Your final message is exactly one JSON object, with no text before or after it and no code fence:

```
{"verdict": "changes_requested", "review": "## Summary\n..."}
```

- `verdict` is `approved` or `changes_requested`, as decided in step 6.
- `review` is the review in Markdown, escaped as a JSON string, with these sections, each as a `## ` heading:

1. **Summary** - two or three sentences on the state of the branch.
2. **Checks** - each command you ran and its result.
3. **Earlier findings** - on later rounds only: each earlier blocking finding and whether it is fixed.
4. **Blocking findings** - numbered, or "None."
5. **Non-blocking findings** - numbered, or "None."
6. **Requirement coverage** - each PRD functional requirement or acceptance criterion and whether the branch meets it.

No preamble or closing remarks.
