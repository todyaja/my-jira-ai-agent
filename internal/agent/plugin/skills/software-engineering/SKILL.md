---
name: software-engineering
description: How to implement an approved TRD, or address an AI code review, the best-practice way - working order, engineering standards, verification, and the summary to report.
---
# Implementing a TRD

## 1. Get oriented

- Read the TRD completely; it is your plan. The PRD explains why, and its acceptance criteria define done.
- Read every file the TRD names before changing it, plus its tests and its callers. Read the README and manifest (`package.json`, `go.mod`, ...) to learn the scripts, test runner and lint setup.
- Run `git status` and `git log` to see what the branch already contains.
- If dependencies are not installed in this worktree (for example no `node_modules`), install them with the lockfile-respecting command (`npm ci`, falling back to `npm install` when there is no lockfile).

## 2. Implement

Follow the TRD's **Implementation Plan** in order. For each step:

- Make the smallest change that fully satisfies it. Do not refactor, rename or reformat code the TRD does not touch.
- Match the codebase: its patterns, naming, file layout, styling approach, error handling and type conventions. When the codebase and a general best practice disagree, follow the codebase unless it causes a bug.
- Write code that is correct at the edges: empty and missing values, invalid input, boundaries, time zones and ordering when relevant.
- Keep types strict: no `any`, unchecked casts or disabled lint rules to make something compile.
- Do not add dependencies unless the TRD calls for them.
- Add or update tests alongside the code, following the **Testing Strategy** and the repository's existing test style. Test behavior, not implementation details. Cover every acceptance criterion.
- Keep comments rare and useful: explain why, never restate what the code does.

If the TRD is wrong or impossible in a detail (a file or function is not as it describes), choose the approach closest to its intent, and report the deviation.

## 3. Verify

Before you finish, run the checks the repository has - typically the test suite, the linter, and the type check or build - and fix what fails. Never weaken, skip or delete a test to make it pass. If a check cannot run (for example a command is refused), say so in the summary rather than claiming it passed.

## Addressing a review

When the request includes an AI review instead of asking for a fresh implementation:

- The branch already holds the earlier work. Read the review in full and look at `git diff` against the base branch to see the current state.
- Fix every blocking finding. Fix each non-blocking finding you agree with; for any you decline, give a one-line reason.
- Re-verify as in step 3.
- Do not make changes the review does not ask for.

## Summary to report

Reply with only this Markdown:

- `## Changes` - the files you changed or added, each with one line on what and why.
- `## Verification` - each check you ran (the exact command) and its result.
- `## Review findings` - only when addressing a review: each finding and how you resolved it, or why you declined it.
- `## Deviations from the TRD` - differences and the reason, or "None."

No preamble or closing remarks.
