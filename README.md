# my-jira-ai-agent

A small Go service that turns a Jira board into an AI workflow. Move a ticket
into a column and a Claude Code agent does that column's job: it writes the
PRD, plans the change, writes the code, reviews it and opens the pull request.
You review at three checkpoints: the PRD, the plan and the pull request.

```
BACKLOG
  → PRD REQUESTED      AI  prd-maker writes the PRD and publishes it to Confluence
  → PRD REVIEW         you approve, or comment and send it back
  → PLANNING           AI  technical-planner reads the repo and writes the TRD
  → PLAN REVIEW        you approve, or comment and send it back
  → READY TO IMPLEMENT AI  starts a branch feature/<ISSUE-KEY>
  → IMPLEMENTING       AI  software-engineer changes the code in its own worktree
  ⇄ AI REVIEW          AI  code-reviewer approves or requests changes (up to 3 rounds)
  → PR READY           AI  pushes the branch and opens the pull request; you review and merge
  → DONE

Any failed step  → AI ERROR (nothing retries until you move the ticket back)
```

## How it works

- **Polling, not webhooks.** Every `JIRA_POLL_INTERVAL_SECONDS` (default 60)
  the server searches Jira for issues assigned to `JIRA_ACCOUNT_ID`. An issue
  whose status changed since it was last handled is re-read directly (search
  results can lag) and dispatched to the action for its new status
  (`internal/workflow/poller.go`, `internal/workflow/actions.go`).
- **One status, one action.** The columns are constants in
  `internal/workflow/status.go`. AI works only in PRD Requested, Planning,
  Ready to Implement, Implementing and AI Review, then moves the ticket on
  itself. PRD Review, Plan Review and PR Ready wait for a person.
- **Claude Code agents.** Each AI step runs the `claude` CLI in print mode
  with one agent from the plugin embedded in the binary
  (`internal/agent/plugin/`). Each agent follows a skill and gets only the
  tools its step needs:

  | Agent | Step | Can | Cannot |
  | --- | --- | --- | --- |
  | `prd-maker` | PRD Requested | load its skill | read files, code or the web |
  | `technical-planner` | Planning | read the repository | run commands or edit |
  | `software-engineer` | Implementing | edit files, run allowed commands | commit, push, switch branches |
  | `code-reviewer` | AI Review | read files, run allowed commands | edit anything |

  The CLI uses the plan you are logged in with: `ANTHROPIC_API_KEY` and
  `ANTHROPIC_AUTH_TOKEN` are removed from its environment.
- **Documents live in Confluence.** The PRD and TRD are published to
  `CONFLUENCE_SPACE_KEY` as `<KEY>: <summary> PRD` / `TRD` and linked from the
  issue description. A revision replaces the page.
- **Code lives in worktrees.** Implementation happens on `feature/<KEY>` in a
  git worktree under `WORKTREES_PATH`, so your main checkout is never touched.
  The workflow commits each round; the worktree is removed when the issue
  reaches PR Ready. Progress (branch, review rounds, pull request) is stored
  in the issue property `jira-ai-agent.implementation`, so a restart picks up
  where it left off.

### Giving feedback on a document

Comment on the issue starting with the agent's name, then move the issue back:

| Comment prefix | Move the issue to | Revises |
| --- | --- | --- |
| `!prd-maker` | PRD Requested | the PRD |
| `!tech-planner` | Planning | the TRD |

The agent changes only what the comments ask for, replies "Done" to each one
and marks it handled so it is not applied twice. With no new comments the
document is left unchanged and the issue goes straight back to review.

## Requirements

- Go 1.26+
- [Claude Code](https://claude.com/claude-code) installed and logged in
  (`claude` on `PATH`, or set `CLAUDE_COMMAND`)
- A Jira Cloud project whose workflow has the statuses above (names are
  matched ignoring case), and a Confluence space
- A local clone of the repository the agents should work on, with an `origin`
  remote git can push to
- Optional: a GitHub fine-grained token with *Pull requests: read and write*

## Setup

```sh
cp .env.example .env   # then fill it in
go run ./cmd/server
```

Or build a binary:

```sh
go build -o bin/server ./cmd/server
./bin/server
```

The server logs every poll and dispatch, and serves a health check at
`http://localhost:8080/health`. On startup it warns if `REPO_PATH`,
`BASE_BRANCH` or `GITHUB_TOKEN` are not usable, since those steps would fail.

## Configuration

All settings are environment variables, read from `.env` when present. See
`.env.example` for the full list with comments.

| Variable | Required | Default | Purpose |
| --- | --- | --- | --- |
| `JIRA_BASE_URL`, `JIRA_EMAIL`, `JIRA_API_TOKEN` | yes | | Jira Cloud site and credentials |
| `JIRA_ACCOUNT_ID` | yes | | Only issues assigned to this account are handled |
| `JIRA_PROJECT_KEY` | no | all projects | Restrict polling to one project |
| `JIRA_POLL_INTERVAL_SECONDS` | no | `60` | How often to poll |
| `CONFLUENCE_SPACE_KEY` | yes | | Space the PRD and TRD pages go in |
| `CONFLUENCE_PARENT_PAGE_ID` | no | space root | Parent page for new pages |
| `CONFLUENCE_BASE_URL`, `CONFLUENCE_EMAIL`, `CONFLUENCE_API_TOKEN` | no | the Jira values | Override when Confluence differs |
| `REPO_PATH` | yes | | Repository the agents read and change |
| `BASE_BRANCH` | no | `main` | Branch feature branches start from and PRs target |
| `WORKTREES_PATH` | no | `<REPO_PATH>-worktrees` | Where per-issue worktrees go |
| `GITHUB_OWNER`, `GITHUB_REPO`, `GITHUB_TOKEN` | no | | Open pull requests in PR Ready; an empty token turns this off |
| `CLAUDE_COMMAND` | no | `claude` | Path to the Claude Code CLI |
| `CLAUDE_MODEL` | no | CLI default | Model alias or name, e.g. `opus` |
| `CLAUDE_<STEP>_MODEL` | no | `CLAUDE_MODEL` | Model for one step: `PRD`, `TRD`, `ENGINEER`, `REVIEWER`, `RFC`, `FSD`, `RUNBOOK` (e.g. `CLAUDE_FSD_MODEL=sonnet`) |
| `CLAUDE_TIMEOUT_SECONDS` | no | `300` | PRD agent time limit |
| `CLAUDE_PLANNING_TIMEOUT_SECONDS` | no | `900` | Technical planner time limit |
| `CLAUDE_IMPLEMENTATION_TIMEOUT_SECONDS` | no | `1800` | Software engineer time limit |
| `CLAUDE_REVIEW_TIMEOUT_SECONDS` | no | `900` | Code reviewer time limit |
| `CLAUDE_ALLOWED_COMMANDS` | no | read-only git | Extra command prefixes the engineer and reviewer may run besides the detected checks |
| `MAX_REVIEW_CYCLES` | no | `3` | AI review rounds before the issue moves on anyway |
| `PORT` | no | `8080` | Health endpoint port; one per server on a machine |
| `WORKFLOW` | no | `scrum` | `scrum` or `bank` |
| `REPOS_ROOT` | bank | | Folder holding the team's repositories |
| `REPO_BASE_BRANCHES` | no | | `name=branch` overrides of the base branch, comma separated |
| `REPOS_FIELD` | no | `Repositories` | Checkbox field whose options are the repository folder names |
| `DOCS_DIR` | no | `REPOS_ROOT` | Where the RFC, FSD and runbook writers run |
| `SKILLS_DIR` | no | `~/.claude/skills` | The user's Claude skills (`make-fsd`, `make-runbook`) |
| `GITLAB_URL`, `GITLAB_TOKEN` | no | | Open merge requests in PR Review; an empty token turns this off |

## Bank workflow

With `WORKFLOW=bank` the agent follows the bank's board instead of the PRD
flow. One server runs per team; every ticket ticks the repositories it
changes in a **Repositories** checkbox field whose options are their folder
names under `REPOS_ROOT`. Add the field to the project's issue types once.
Without the field, or when it is empty, a checklist (`[]` in the Jira editor)
in the description is read instead.

| Status | What happens |
| --- | --- |
| Request RFC | The `rfc-writer` agent reads the current code and writes the RFC with the `make-fsd` skill. It is filed in Confluence under *RFCs > RFCs <year>* with the Word file attached, labelled with the issue key, and linked from the issue. Moves to RFC Review. |
| Request TRD | The `repo-planner` agent writes an overview TRD (cross-repository contracts, end-to-end flow, merge order) and one TRD per ticked repository, titled `<key> [<repo>] <what changes>`, under *TRDs > TRDs <year> > <overview>*. Moves to TRD Review. |
| Ready To Implement | Starts a cycle per repository on branch `feature/<key>`. Moves to Implementing. |
| Implementing | The engineer works in every repository still under review, in parallel, each in its own worktree, writing unit tests for everything it adds or changes. Moves to AI Review. |
| AI Review | Each repository is reviewed against its TRD. Repositories with changes requested go back to Implementing; once all are approved (or out of rounds) the branches are pushed and the issue moves to Engineer Review. |
| PR Review | One GitLab merge request per changed repository. The issue stays for the reviewers. |
| FSD Request | The `fsd-writer` agent writes the FSD from the RFC, the TRDs and the implemented code, filed under *FSDs > FSDs <year>*. Moves to FSD Review. |
| Runbook Request | The `runbook-writer` agent writes the deployment runbook, filed under *Runbooks > Runbooks <year>*. Moves to Runbook Review. |

The review statuses, Engineer Review, Development Done and Go Live are
people's steps. A failed step moves the issue to AI ERROR. Each repository's
progress, branch and merge request is kept in a status table at the end of
the description. Reviewers ask for document changes with comments starting
`!rfc-writer`, `!tech-planner`, `!fsd-writer` or `!runbook-writer` and move
the issue back to the request status. The application is never run: checks
are unit tests (`mvn -q -B test` for Maven).

## Project layout

```
cmd/server/            entry point: config, wiring, poller, /health
internal/workflow/     statuses, poller, and the action for each status
internal/agent/        runs the Claude Code CLI; plugin/ holds the agents and skills
internal/jira/         Jira REST client, ADF and Markdown conversion
internal/confluence/   publishes PRD and TRD pages
internal/git/          per-issue worktrees: prepare, commit, diff, push, remove
internal/github/       opens the pull request
docs/superpowers/      design specs and implementation plans
```

## Development

```sh
go test ./...
```

To change how a step works, edit its agent in
`internal/agent/plugin/agents/` or its skill in
`internal/agent/plugin/skills/`. The plugin is embedded at build time and
installed under a content-hashed directory, so a rebuilt binary always runs
the agents it was built with.
