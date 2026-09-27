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
%s

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
}

func NewClaudeEngineer(config ClaudeConfig) (*ClaudeEngineer, error) {
	cli, err := newClaudeCLI(config)
	if err != nil {
		return nil, err
	}
	return &ClaudeEngineer{cli: cli, timeout: timeoutOrDefault(config.ImplementationTimeout, DefaultClaudeImplementationTimeout), allowedCommands: commandsOrDefault(config.AllowedCommands)}, nil
}

// Implement runs the engineer in the worktree. File edits there are accepted
// automatically and only the allowed commands may run.
func (e *ClaudeEngineer) Implement(ctx context.Context, input workflow.ImplementInput) (string, error) {
	return e.cli.run(ctx, claudeRun{
		agent:          SoftwareEngineerAgent,
		tools:          engineerTools,
		dir:            input.Dir,
		prompt:         implementPrompt(input),
		timeout:        e.timeout,
		permissionMode: "acceptEdits",
		allowedTools:   bashRules(e.allowedCommands),
	})
}

func implementPrompt(input workflow.ImplementInput) string {
	if strings.TrimSpace(input.Review) == "" {
		return fmt.Sprintf(claudeImplementPrompt, input.Branch, input.IssueKey, input.Summary, input.Description, input.PRD, input.TRD)
	}
	return fmt.Sprintf(claudeAddressReviewPrompt, input.Branch, input.IssueKey, input.Summary, input.Description, input.PRD, input.TRD, input.Review)
}

// ClaudeReviewer reviews branches with the code-reviewer agent.
type ClaudeReviewer struct {
	cli             claudeCLI
	timeout         time.Duration
	allowedCommands []string
}

func NewClaudeReviewer(config ClaudeConfig) (*ClaudeReviewer, error) {
	cli, err := newClaudeCLI(config)
	if err != nil {
		return nil, err
	}
	return &ClaudeReviewer{cli: cli, timeout: timeoutOrDefault(config.ReviewTimeout, DefaultClaudeReviewTimeout), allowedCommands: commandsOrDefault(config.AllowedCommands)}, nil
}

type reviewAnswer struct {
	Verdict string `json:"verdict"`
	Review  string `json:"review"`
}

// Review runs the reviewer in the worktree without edit tools and returns
// its verdict and review.
func (r *ClaudeReviewer) Review(ctx context.Context, input workflow.ReviewInput) (workflow.ReviewResult, error) {
	result, err := r.cli.runResult(ctx, claudeRun{
		agent:        CodeReviewerAgent,
		tools:        reviewerTools,
		dir:          input.Dir,
		prompt:       reviewPrompt(input),
		timeout:      r.timeout,
		allowedTools: bashRules(r.allowedCommands),
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

func reviewPrompt(input workflow.ReviewInput) string {
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
	return fmt.Sprintf(claudeReviewPrompt, input.Branch, input.Round, input.MaxRounds, input.IssueKey, input.Summary, input.Description, input.PRD, input.TRD, previous, diff)
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
