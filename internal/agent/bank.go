package agent

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/tody-aja/jira-ai-agent/internal/repos"
	"github.com/tody-aja/jira-ai-agent/internal/workflow"
)

// Long documents are answered as sections opened by a marker line such as
// "<<<OVERVIEW>>>" rather than as JSON, which models escape unreliably at
// this length.
var sectionMarker = regexp.MustCompile(`^<<<\s*(.+?)\s*>>>\s*$`)

type answerSection struct {
	// Name is the marker's first word in upper case; Argument the rest.
	Name     string
	Argument string
	Body     string
}

// parseSections splits an answer into its marked sections. Text before the
// first marker is ignored, and so is a marker inside a fenced code block,
// where a document may show the format as an example.
func parseSections(answer string) []answerSection {
	sections := []answerSection{}
	var current *answerSection
	var body []string
	inFence := false
	flush := func() {
		if current != nil {
			current.Body = strings.TrimSpace(strings.Join(body, "\n"))
			sections = append(sections, *current)
		}
	}
	for _, line := range strings.Split(strings.ReplaceAll(answer, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
		}
		match := sectionMarker.FindStringSubmatch(trimmed)
		if match == nil || inFence {
			body = append(body, line)
			continue
		}
		flush()
		name, argument, _ := strings.Cut(match[1], " ")
		current = &answerSection{Name: strings.ToUpper(strings.TrimSpace(name)), Argument: strings.TrimSpace(argument)}
		body = nil
	}
	flush()
	return sections
}

const repoPlannerTools = "Skill,Read,Grep,Glob,Bash"

// gitReadCommands read any branch of a repository without changing a
// checkout, and gitExactCommands are allowed only exactly as written. The
// fetch updates the origin/* branches, so branches pushed by others can be
// read, but never local branches or files; it takes no arguments because
// options such as --upload-pack run programs.
var (
	gitReadCommands  = []string{"log", "show", "diff", "grep", "ls-tree"}
	gitExactCommands = []string{"branch -a", "fetch origin", "ls-remote --heads origin"}
)

// gitReadRules allow the git commands only as "git -C <path> ..." with each
// repository's exact path: a wildcard before the subcommand would also allow
// options such as -c, which can run anything.
func gitReadRules(lists ...[]repos.Repo) []string {
	rules := []string{}
	seen := map[string]bool{}
	for _, list := range lists {
		for _, repo := range list {
			path := slashPath(repo.Path)
			if path == "" || seen[path] {
				continue
			}
			seen[path] = true
			for _, command := range gitExactCommands {
				rules = append(rules, "Bash(git -C "+path+" "+command+")")
			}
			for _, command := range gitReadCommands {
				rules = append(rules, "Bash(git -C "+path+" "+command+":*)")
			}
		}
	}
	return rules
}

// slashPath writes a path with forward slashes, which the agent's shell
// keeps as they are; backslashes would be read as escapes.
func slashPath(path string) string {
	return strings.ReplaceAll(strings.TrimSpace(path), `\`, "/")
}

const claudeRepoPlanPrompt = `Write the TRDs for this Jira issue: one overview and one TRD for each repository it changes. Your working directory holds every repository of the team; read the code before proposing anything.

Repositories this issue changes, as checkouts of their base branch (the code implementation starts from):
%s

Other repositories you may read for context but must not plan changes in:
%s

The files show one branch only. When work on another branch matters, such as a related feature still unmerged, read it with git, giving -C and the repository path exactly as listed above:
- git -C <path> fetch origin brings in the remote branches others pushed (exactly this, with no other arguments); run it once per repository before looking for a branch
- git -C <path> branch -a lists the branches; remote ones are origin/<name>, and git -C <path> ls-remote --heads origin lists what the server has
- git -C <path> show <branch>:<file> reads a file on a branch
- git -C <path> ls-tree -r --name-only <branch> lists its files
- git -C <path> log, diff and grep show and search its history and changes, e.g. git -C <path> diff master...<branch>
These are the only git commands you can run. Never check out or change a branch.%s

Issue key: %s
Summary: %s
Description:
%s

Approved RFC (Confluence storage format):
%s%s

Reply in exactly this format, with every marker on its own line:

<<<OVERVIEW>>>
(the overview TRD in Markdown)
<<<MERGE ORDER>>>
(the changed repositories in the order they must merge and deploy, comma separated)
<<<REPO name | what changes in this repository>>>
(that repository's TRD in Markdown)
<<<SQL MIGRATION PLAN>>>
(the full SQL migration plan in Markdown, as your instructions describe, or exactly "None" when the change needs no database change)
<<<BRANCH NAME>>>
(3 to 6 lowercase English words joined by hyphens naming what this issue changes, for the git branch, e.g. bulk-overpayment-withdraw-batch; no ticket code)

Write one REPO section for every repository this issue changes, and no other, and always the SQL MIGRATION PLAN section.`

const claudeRepoPlanRevision = `

Current overview TRD (Confluence storage format):
%s

Current repository TRDs (Confluence storage format):
%s
Current SQL migration plan (Confluence storage format; reviewers may have edited the scripts on the page - keep their edits):
%s

Reviewer feedback, oldest first:
%s
Revise the current TRDs and SQL migration plan for the feedback as the trd-writing skill describes, keep each repository's title, and reply with every section in full.`

// ClaudeRepoPlanner writes the bank workflow's TRDs with the repo-planner
// agent, run in the folder holding every repository.
type ClaudeRepoPlanner struct {
	cli     claudeCLI
	root    string
	timeout time.Duration
	devDB   DevDB
}

func NewClaudeRepoPlanner(config ClaudeConfig, root string) (*ClaudeRepoPlanner, error) {
	cli, err := newClaudeCLI(config, StepTRD)
	if err != nil {
		return nil, err
	}
	return &ClaudeRepoPlanner{cli: cli, root: root, timeout: timeoutOrDefault(config.PlanningTimeout, DefaultClaudePlanningTimeout), devDB: config.DevDB}, nil
}

func (p *ClaudeRepoPlanner) Plan(ctx context.Context, input workflow.PlanInput) (workflow.PlanResult, error) {
	answer, err := p.cli.run(ctx, claudeRun{
		agent:        RepoPlannerAgent,
		tools:        repoPlannerTools,
		dir:          p.root,
		prompt:       repoPlanPrompt(input, p.devDB),
		timeout:      p.timeout,
		allowedTools: append(gitReadRules(input.Repos, input.AllRepos), p.devDB.rules()...),
		env:          p.devDB.env(),
	})
	if err != nil {
		return workflow.PlanResult{}, err
	}
	return parsePlan(answer)
}

func repoPlanPrompt(input workflow.PlanInput, devDB DevDB) string {
	revision := ""
	if len(input.Feedback) > 0 || input.CurrentOverview != "" {
		var current strings.Builder
		for _, repo := range input.Current {
			fmt.Fprintf(&current, "\n[%s - %s]\n%s\n", repo.Repo, repo.Title, repo.TRD)
		}
		currentSQL := input.CurrentSQL
		if strings.TrimSpace(currentSQL) == "" {
			currentSQL = "(none published)"
		}
		revision = fmt.Sprintf(claudeRepoPlanRevision, input.CurrentOverview, current.String(), currentSQL, workflow.FormatFeedback(input.Feedback))
	}
	return fmt.Sprintf(claudeRepoPlanPrompt, repoList(input.Repos), contextRepoList(input.AllRepos, input.Repos), devDB.readSection(), input.IssueKey, input.Summary, input.Description, input.RFC, revision)
}

func parsePlan(answer string) (workflow.PlanResult, error) {
	result := workflow.PlanResult{}
	for _, section := range parseSections(answer) {
		switch section.Name {
		case "OVERVIEW":
			result.Overview = section.Body
		case "MERGE":
			for _, name := range strings.Split(section.Body, ",") {
				if name = strings.TrimSpace(strings.Trim(strings.TrimSpace(name), "`")); name != "" {
					result.MergeOrder = append(result.MergeOrder, name)
				}
			}
		case "SQL":
			result.SQL = section.Body
		case "BRANCH":
			result.BranchName = section.Body
		case "REPO":
			name, title, _ := strings.Cut(section.Argument, "|")
			name, title = strings.TrimSpace(name), strings.TrimSpace(title)
			// A TRD split over several sections is joined; the first
			// section names it.
			merged := false
			for index := range result.Repos {
				if strings.EqualFold(result.Repos[index].Repo, name) {
					result.Repos[index].TRD += "\n\n" + section.Body
					merged = true
				}
			}
			if !merged {
				result.Repos = append(result.Repos, workflow.RepoTRD{Repo: name, Title: title, TRD: section.Body})
			}
		}
	}
	if result.Overview == "" || len(result.Repos) == 0 {
		return workflow.PlanResult{}, fmt.Errorf("the planner's answer has no overview or repository TRDs")
	}
	return result, nil
}

// documentWriterTools let a writer read the code, run its skill's scripts
// and write the document files.
const documentWriterTools = "Skill,Read,Grep,Glob,Write,Edit,Bash"

// documentWriterCommands are what a writer may run: the skills' Node
// builders, Java for PlantUML diagrams, and reading git history.
var documentWriterCommands = []string{"node", "java", "git log", "git show", "git diff"}

const claudeDocumentPrompt = `Write the %s for this Jira issue with your skill, in Bahasa Indonesia, from the bank's Word template. Read the current code of the repositories involved first, so the document describes how they work today accurately.

Repositories this issue changes, as checkouts of their base branch (the code implementation starts from):
%s

Other repositories you may read for context:
%s

Never change anything inside a repository; write only the document's own files (spec, diagrams and the Word file) where the skill puts them.

Issue key: %s
Summary: %s
Description:
%s%s%s

When the Word file is built and checked, reply in exactly this format, with every marker on its own line:

<<<TITLE>>>
(the document's title, starting with its number, e.g. its file name without .docx)
<<<DOCX>>>
(the absolute path of the Word file)
<<<SUMMARY>>>
(a Markdown summary of the document for its Confluence page: purpose, the main points of each section, and the open points)`

const claudeDocumentRevision = `

This is a revision of the published %s titled %q, built as %s. Update that same document and Word file - keep its number - for the reviewer feedback below; change only what the feedback asks for.

Current page content (Confluence storage format):
%s

Reviewer feedback, oldest first:
%s`

var documentNames = map[workflow.DocumentKind]string{workflow.RFCKind: "RFC", workflow.FSDKind: "FSD", workflow.RunbookKind: "runbook"}

var documentAgents = map[workflow.DocumentKind]string{workflow.RFCKind: RFCWriterAgent, workflow.FSDKind: FSDWriterAgent, workflow.RunbookKind: RunbookWriterAgent}

// ClaudeDocumentWriter writes RFCs, FSDs and runbooks with the writer agents
// and the user's make-fsd and make-runbook skills, run in the documents
// folder.
type ClaudeDocumentWriter struct {
	cli     claudeCLI
	dir     string
	addDirs []string
	timeout time.Duration
	// models are the models of the RFC, FSD and runbook writers.
	models map[workflow.DocumentKind]string
}

// NewClaudeDocumentWriter runs the writers in dir, where the skills keep
// their templates and outputs, with read access to addDirs such as the
// repositories root and the skills folder.
func NewClaudeDocumentWriter(config ClaudeConfig, dir string, addDirs []string) (*ClaudeDocumentWriter, error) {
	cli, err := newClaudeCLI(config, StepRFC)
	if err != nil {
		return nil, err
	}
	models := map[workflow.DocumentKind]string{workflow.RFCKind: config.ModelFor(StepRFC), workflow.FSDKind: config.ModelFor(StepFSD), workflow.RunbookKind: config.ModelFor(StepRunbook)}
	return &ClaudeDocumentWriter{cli: cli, dir: dir, addDirs: addDirs, timeout: timeoutOrDefault(config.ImplementationTimeout, DefaultClaudeImplementationTimeout), models: models}, nil
}

func (w *ClaudeDocumentWriter) WriteDocument(ctx context.Context, input workflow.DocumentInput) (workflow.DocumentResult, error) {
	agentName, found := documentAgents[input.Kind]
	if !found {
		return workflow.DocumentResult{}, fmt.Errorf("unknown document kind %q", input.Kind)
	}
	before := repoChanges(ctx, input.AllRepos)
	answer, err := w.cli.run(ctx, claudeRun{
		agent:          agentName,
		tools:          documentWriterTools,
		dir:            w.dir,
		prompt:         documentPrompt(input),
		timeout:        w.timeout,
		permissionMode: "acceptEdits",
		allowedTools:   bashRules(documentWriterCommands),
		addDirs:        w.addDirs,
		model:          w.models[input.Kind],
	})
	if err != nil {
		return workflow.DocumentResult{}, err
	}
	if changed := changedRepos(before, repoChanges(ctx, input.AllRepos)); len(changed) > 0 {
		return workflow.DocumentResult{}, fmt.Errorf("the %s writer changed files in %s; check those repositories by hand", documentNames[input.Kind], strings.Join(changed, ", "))
	}
	return parseDocument(answer)
}

func documentPrompt(input workflow.DocumentInput) string {
	var sources strings.Builder
	for _, source := range input.Sources {
		fmt.Fprintf(&sources, "\n\nApproved source - %s:\n%s", source.Title, source.Content)
	}
	revision := ""
	if input.CurrentTitle != "" {
		revision = fmt.Sprintf(claudeDocumentRevision, documentNames[input.Kind], input.CurrentTitle, input.CurrentDocx, input.CurrentContent, workflow.FormatFeedback(input.Feedback))
	}
	return fmt.Sprintf(claudeDocumentPrompt, documentNames[input.Kind], repoList(input.Repos), contextRepoList(input.AllRepos, input.Repos), input.IssueKey, input.Summary, input.Description, sources.String(), revision)
}

func parseDocument(answer string) (workflow.DocumentResult, error) {
	result := workflow.DocumentResult{}
	for _, section := range parseSections(answer) {
		switch section.Name {
		case "TITLE":
			result.Title = firstLineOf(section.Body)
		case "DOCX":
			result.Docx = strings.Trim(firstLineOf(section.Body), "`\"' ")
		case "SUMMARY":
			result.Summary = section.Body
		}
	}
	if result.Docx == "" || result.Summary == "" {
		return workflow.DocumentResult{}, fmt.Errorf("the writer's answer has no Word file path or summary")
	}
	return result, nil
}

func firstLineOf(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	return strings.TrimSpace(line)
}

func repoList(list []repos.Repo) string {
	if len(list) == 0 {
		return "(none)"
	}
	lines := make([]string, 0, len(list))
	for _, repo := range list {
		lines = append(lines, fmt.Sprintf("- %s: %s (base branch %s)", repo.Name, slashPath(repo.Path), repo.BaseBranch))
	}
	return strings.Join(lines, "\n")
}

// contextRepoList lists the repositories in all that are not in changed.
func contextRepoList(all, changed []repos.Repo) string {
	others := []repos.Repo{}
	for _, repo := range all {
		isChanged := false
		for _, target := range changed {
			isChanged = isChanged || target.Name == repo.Name
		}
		if !isChanged {
			others = append(others, repo)
		}
	}
	return repoList(others)
}

// repoChanges records each repository's uncommitted changes, so a run that
// edits a repository can be caught.
func repoChanges(ctx context.Context, list []repos.Repo) map[string]string {
	changes := map[string]string{}
	for _, repo := range list {
		command := exec.CommandContext(ctx, "git", "-C", repo.Path, "status", "--porcelain")
		var output bytes.Buffer
		command.Stdout = &output
		if err := command.Run(); err != nil {
			changes[repo.Name] = "unreadable"
			continue
		}
		changes[repo.Name] = output.String()
	}
	return changes
}

func changedRepos(before, after map[string]string) []string {
	changed := []string{}
	for name, status := range after {
		if before[name] != status {
			changed = append(changed, name)
		}
	}
	return changed
}

var (
	_ workflow.RepoPlanner    = (*ClaudeRepoPlanner)(nil)
	_ workflow.DocumentWriter = (*ClaudeDocumentWriter)(nil)
)
