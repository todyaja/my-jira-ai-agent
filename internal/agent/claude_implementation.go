package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/tody-aja/jira-ai-agent/internal/workflow"
)

const claudeImplementPrompt = `Implement the approved TRD for this Jira issue. Your current working directory is a git worktree checked out on branch %s: make every change there. Do not commit; the workflow commits your changes when you finish.

Issue key: %s
Summary: %s
Description:
%s

Approved PRD (Confluence storage format):
%s

Approved TRD (Confluence storage format):
%s`

const claudeAddressReviewPrompt = `Address the AI code review below for this Jira issue. Your current working directory is a git worktree checked out on branch %s, which already holds the earlier implementation. Make every change there. Do not commit; the workflow commits your changes when you finish.

Issue key: %s
Summary: %s
Description:
%s

Approved PRD (Confluence storage format):
%s

Approved TRD (Confluence storage format):
%s

AI review to address:
%s`

const claudeReviewPrompt = `Review branch %s for this Jira issue against its PRD and TRD. This is review round %d of %d. Your current working directory is the branch's worktree.

Issue key: %s
Summary: %s
Description:
%s

Approved PRD (Confluence storage format):
%s

Approved TRD (Confluence storage format):
%s

Earlier reviews in this cycle, oldest first:
%s

Changes on the branch against the base branch:
%s%s

Finish with exactly one JSON object and nothing else: {"verdict": "approved" or "changes_requested", "review": "<the review in Markdown>"}.`

// reviewSchema is the structured answer the reviewer must give.
const reviewSchema = `{"type":"object","properties":{"verdict":{"type":"string","enum":["approved","changes_requested"]},"review":{"type":"string","description":"The full review in Markdown"}},"required":["verdict","review"],"additionalProperties":false}`

// maxPromptDiffRunes keeps a large diff from crowding out the rest of the
// review prompt; the reviewer can read the rest with git diff.
const maxPromptDiffRunes = 150000

// engineerTools let the engineer read, edit and run the allowed commands.
const engineerTools = "Skill,Read,Grep,Glob,Edit,Write,Bash"

// reviewerTools let the reviewer read and run the allowed commands, but not
// edit.
const reviewerTools = "Skill,Read,Grep,Glob,Bash"

// ClaudeEngineer implements issues with the software-engineer agent.
type ClaudeEngineer struct {
	cli             claudeCLI
	timeout         time.Duration
	allowedCommands []string
	checks          checkDetector
	devDB           DevDB
}

func NewClaudeEngineer(config ClaudeConfig) (*ClaudeEngineer, error) {
	cli, err := newClaudeCLI(config, StepEngineer)
	if err != nil {
		return nil, err
	}
	return &ClaudeEngineer{cli: cli, timeout: timeoutOrDefault(config.ImplementationTimeout, DefaultClaudeImplementationTimeout), allowedCommands: commandsOrDefault(config.AllowedCommands), checks: newCheckDetector(), devDB: config.DevDB}, nil
}

// Implement runs the engineer in the worktree. File edits there are accepted
// automatically and only the worktree's checks and the allowed commands may
// run, plus the read-only development database queries when the workflow
// asks for them.
func (e *ClaudeEngineer) Implement(ctx context.Context, input workflow.ImplementInput) (string, error) {
	checks := e.checks.detect(ctx, input.Dir)
	run := claudeRun{
		agent:          SoftwareEngineerAgent,
		tools:          engineerTools,
		dir:            input.Dir,
		prompt:         implementPrompt(input, checks, e.allowedCommands),
		timeout:        e.timeout,
		permissionMode: "acceptEdits",
		allowedTools:   bashRules(append(checks.all(), e.allowedCommands...)),
	}
	if input.ReadDatabase {
		run.prompt += e.devDB.readSection()
		run.allowedTools = append(run.allowedTools, e.devDB.rules()...)
		run.env = e.devDB.env()
	}
	return e.cli.run(ctx, run)
}

func implementPrompt(input workflow.ImplementInput, checks Checks, commands []string) string {
	if strings.TrimSpace(input.Review) == "" {
		return fmt.Sprintf(claudeImplementPrompt, input.Branch, input.IssueKey, input.Summary, input.Description, input.PRD, input.TRD) + contextSection(input.Context) + checksSection(checks, commands)
	}
	return fmt.Sprintf(claudeAddressReviewPrompt, input.Branch, input.IssueKey, input.Summary, input.Description, input.PRD, input.TRD, input.Review) + contextSection(input.Context) + checksSection(checks, commands)
}

// contextSection adds the workflow's own instructions, such as which
// repository of a multi-repository change this is.
func contextSection(context string) string {
	if strings.TrimSpace(context) == "" {
		return ""
	}
	return "\n\nWorkflow notes:\n" + strings.TrimSpace(context)
}

// checksSection tells the agent where the checks run and the exact commands
// for them, so it does not guess a command that is refused or runs on the
// wrong toolchain.
func checksSection(checks Checks, commands []string) string {
	var builder strings.Builder
	if len(checks.Commands) == 0 {
		builder.WriteString("\n\nNo checks were found for this repository (no test, lint, type check or build scripts). Say so in your summary.")
	} else {
		builder.WriteString("\n\nChecks for this repository run " + checks.Environment + ". Run them from the worktree root exactly as written:")
		if len(checks.Setup) > 0 {
			builder.WriteString("\n- First, because dependencies are not installed: " + strings.Join(checks.Setup, ", "))
		}
		for _, command := range checks.Commands {
			builder.WriteString("\n- " + command)
		}
	}
	builder.WriteString("\n\nOther shell commands you may run (each is a prefix; any other command is refused):")
	for _, command := range commands {
		if command = strings.TrimSpace(command); command != "" {
			builder.WriteString("\n- " + command)
		}
	}
	return builder.String()
}

// ClaudeReviewer reviews branches with the code-reviewer agent.
type ClaudeReviewer struct {
	cli             claudeCLI
	timeout         time.Duration
	allowedCommands []string
	checks          checkDetector
}

func NewClaudeReviewer(config ClaudeConfig) (*ClaudeReviewer, error) {
	cli, err := newClaudeCLI(config, StepReviewer)
	if err != nil {
		return nil, err
	}
	return &ClaudeReviewer{cli: cli, timeout: timeoutOrDefault(config.ReviewTimeout, DefaultClaudeReviewTimeout), allowedCommands: commandsOrDefault(config.AllowedCommands), checks: newCheckDetector()}, nil
}

type reviewAnswer struct {
	Verdict string `json:"verdict"`
	Review  string `json:"review"`
}

// Review runs the reviewer in the worktree without edit tools and returns
// its verdict and review.
func (r *ClaudeReviewer) Review(ctx context.Context, input workflow.ReviewInput) (workflow.ReviewResult, error) {
	checks := r.checks.detect(ctx, input.Dir)
	result, err := r.cli.runResult(ctx, claudeRun{
		agent:        CodeReviewerAgent,
		tools:        reviewerTools,
		dir:          input.Dir,
		prompt:       reviewPrompt(input, checks, r.allowedCommands),
		timeout:      r.timeout,
		allowedTools: bashRules(append(checks.all(), r.allowedCommands...)),
		jsonSchema:   reviewSchema,
	})
	if err != nil {
		return workflow.ReviewResult{}, err
	}
	answer, err := decodeReviewAnswer(result)
	if err != nil {
		return workflow.ReviewResult{}, err
	}
	return workflow.ReviewResult{Approved: answer.Verdict == "approved", Review: strings.TrimSpace(answer.Review)}, nil
}

// verdictLine finds a "Verdict: approved" or "Verdict: changes requested"
// line in a Markdown review, ignoring emphasis and case.
var verdictLine = regexp.MustCompile(`(?im)^[\W_]*verdict[\W_]*(approved|changes[ _-]requested)\b`)

// decodeReviewAnswer reads the reviewer's verdict. The CLI ignores
// --json-schema when running an agent, so besides structured output it
// accepts the JSON object in the text answer, fenced or not, and as a last
// resort a Markdown review with a verdict line.
func decodeReviewAnswer(result claudeResult) (reviewAnswer, error) {
	answer, err := parseReviewJSON(result.StructuredOutput)
	if err != nil {
		answer, err = parseReviewJSON(jsonObject(result.Result))
	}
	if err != nil {
		match := verdictLine.FindStringSubmatch(result.Result)
		if match == nil {
			return reviewAnswer{}, fmt.Errorf("decode review verdict: no JSON verdict or verdict line in the answer")
		}
		answer = reviewAnswer{Verdict: "approved", Review: strings.TrimSpace(result.Result)}
		if !strings.EqualFold(match[1], "approved") {
			answer.Verdict = "changes_requested"
		}
	}
	if strings.TrimSpace(answer.Review) == "" {
		return reviewAnswer{}, fmt.Errorf("review has no text")
	}
	return answer, nil
}

func parseReviewJSON(raw []byte) (reviewAnswer, error) {
	var answer reviewAnswer
	if err := json.Unmarshal(raw, &answer); err != nil {
		return reviewAnswer{}, err
	}
	if answer.Verdict != "approved" && answer.Verdict != "changes_requested" {
		return reviewAnswer{}, fmt.Errorf("review verdict %q is neither approved nor changes_requested", answer.Verdict)
	}
	return answer, nil
}

// jsonObject returns the outermost {...} in text, which drops any code
// fence or remarks around it.
func jsonObject(text string) []byte {
	start, end := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if start < 0 || end < start {
		return nil
	}
	return []byte(text[start : end+1])
}

func reviewPrompt(input workflow.ReviewInput, checks Checks, commands []string) string {
	previous := "(none - this is the first review)"
	if len(input.PreviousReviews) > 0 {
		var builder strings.Builder
		for index, review := range input.PreviousReviews {
			fmt.Fprintf(&builder, "\n[Round %d review]\n%s\n", index+1, review)
		}
		previous = builder.String()
	}
	diff := input.Diff
	if runes := []rune(diff); len(runes) > maxPromptDiffRunes {
		diff = string(runes[:maxPromptDiffRunes]) + "\n\n(diff truncated - run git diff to read the rest)"
	}
	if strings.TrimSpace(diff) == "" {
		diff = "(no changes on the branch)"
	}
	return fmt.Sprintf(claudeReviewPrompt, input.Branch, input.Round, input.MaxRounds, input.IssueKey, input.Summary, input.Description, input.PRD, input.TRD, previous, diff, contextSection(input.Context)+checksSection(checks, commands))
}

func commandsOrDefault(commands []string) []string {
	if len(commands) == 0 {
		return DefaultAllowedCommands
	}
	return commands
}

var (
	_ workflow.Engineer = (*ClaudeEngineer)(nil)
	_ workflow.Reviewer = (*ClaudeReviewer)(nil)
)
