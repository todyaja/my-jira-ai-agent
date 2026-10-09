package agent

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/tody-aja/jira-ai-agent/internal/workflow"
)

func TestEngineerAndReviewerArguments(t *testing.T) {
	config := ClaudeConfig{Executable: os.Args[0], AllowedCommands: []string{"npm test", "git diff"}}
	engineer, err := NewClaudeEngineer(config)
	if err != nil {
		t.Fatalf("NewClaudeEngineer() error = %v", err)
	}
	reviewer, err := NewClaudeReviewer(config)
	if err != nil {
		t.Fatalf("NewClaudeReviewer() error = %v", err)
	}

	engineerArgs := engineer.cli.args(claudeRun{agent: SoftwareEngineerAgent, tools: engineerTools, permissionMode: "acceptEdits", allowedTools: bashRules(engineer.allowedCommands)})
	for _, want := range [][]string{
		{"--agent", "jira-ai-agent:software-engineer"},
		{"--tools", "Skill,Read,Grep,Glob,Edit,Write,Bash"},
		{"--permission-mode", "acceptEdits"},
		{"--allowedTools", "Bash(npm test:*),Bash(git diff:*)"},
	} {
		if !containsPair(engineerArgs, want) {
			t.Fatalf("engineer args = %q, want %q", engineerArgs, want)
		}
	}

	reviewerArgs := reviewer.cli.args(claudeRun{agent: CodeReviewerAgent, tools: reviewerTools, allowedTools: bashRules(reviewer.allowedCommands), jsonSchema: reviewSchema})
	if !containsPair(reviewerArgs, []string{"--tools", "Skill,Read,Grep,Glob,Bash"}) || !containsPair(reviewerArgs, []string{"--json-schema", reviewSchema}) {
		t.Fatalf("reviewer args = %q, want read-only tools and the verdict schema", reviewerArgs)
	}
	if slices.Contains(reviewerArgs, "--permission-mode") || strings.Contains(strings.Join(reviewerArgs, " "), "Edit") {
		t.Fatalf("reviewer args = %q, want no edit access", reviewerArgs)
	}
	if engineer.timeout != DefaultClaudeImplementationTimeout || reviewer.timeout != DefaultClaudeReviewTimeout {
		t.Fatalf("timeouts = (%s, %s), want defaults", engineer.timeout, reviewer.timeout)
	}
}

func containsPair(args, pair []string) bool {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == pair[0] && args[index+1] == pair[1] {
			return true
		}
	}
	return false
}

func TestImplementPromptSwitchesToReviewMode(t *testing.T) {
	input := workflow.ImplementInput{IssueKey: "DEMO-1", Summary: "Export", Branch: "ai/DEMO-1", PRD: "<p>PRD</p>", TRD: "<p>TRD</p>"}
	if got := implementPrompt(input); !strings.HasPrefix(got, "Implement the approved TRD") || !strings.Contains(got, "branch ai/DEMO-1") || !strings.Contains(got, "<p>TRD</p>") {
		t.Fatalf("implementPrompt() = %q, want TRD implementation prompt", got)
	}
	input.Review = "Blocking: add a test"
	if got := implementPrompt(input); !strings.HasPrefix(got, "Address the AI code review") || !strings.Contains(got, "Blocking: add a test") {
		t.Fatalf("implementPrompt() = %q, want review prompt", got)
	}
}

func TestReviewPromptIncludesRoundHistoryAndDiff(t *testing.T) {
	got := reviewPrompt(workflow.ReviewInput{Branch: "ai/DEMO-1", Round: 2, MaxRounds: 3, PreviousReviews: []string{"first review"}, Diff: "+added"})
	for _, want := range []string{"review round 2 of 3", "[Round 1 review]\nfirst review", "+added"} {
		if !strings.Contains(got, want) {
			t.Fatalf("reviewPrompt() = %q, want %q", got, want)
		}
	}
	long := reviewPrompt(workflow.ReviewInput{Diff: strings.Repeat("x", maxPromptDiffRunes+1)})
	if !strings.Contains(long, "diff truncated") || !strings.Contains(reviewPrompt(workflow.ReviewInput{}), "(no changes on the branch)") {
		t.Fatal("reviewPrompt() does not mark truncated or empty diffs")
	}
}

func TestDecodeReviewAnswer(t *testing.T) {
	structured := claudeResult{StructuredOutput: json.RawMessage(`{"verdict":"approved","review":"LGTM"}`)}
	if answer, err := decodeReviewAnswer(structured); err != nil || answer.Verdict != "approved" || answer.Review != "LGTM" {
		t.Fatalf("decodeReviewAnswer(structured) = (%+v, %v)", answer, err)
	}
	fromText := claudeResult{Result: `{"verdict":"changes_requested","review":"Fix it"}`}
	if answer, err := decodeReviewAnswer(fromText); err != nil || answer.Verdict != "changes_requested" {
		t.Fatalf("decodeReviewAnswer(text) = (%+v, %v)", answer, err)
	}
	fenced := claudeResult{Result: "Here is my review:\n```json\n{\"verdict\": \"approved\", \"review\": \"## Summary\\nFine\"}\n```"}
	if answer, err := decodeReviewAnswer(fenced); err != nil || answer.Verdict != "approved" || answer.Review != "## Summary\nFine" {
		t.Fatalf("decodeReviewAnswer(fenced) = (%+v, %v)", answer, err)
	}
	for text, want := range map[string]string{
		"**Verdict:** Changes requested\n\n## Blocking findings\n1. Bug": "changes_requested",
		"## Summary\nAll good.\n\n**VERDICT: APPROVED**":                 "approved",
		"Verdict - changes_requested\nFix the test":                      "changes_requested",
	} {
		answer, err := decodeReviewAnswer(claudeResult{Result: text})
		if err != nil || answer.Verdict != want || answer.Review != text {
			t.Fatalf("decodeReviewAnswer(%q) = (%+v, %v), want %s with the text as review", text, answer, err, want)
		}
	}
	for _, bad := range []claudeResult{
		{Result: "looks fine to me"},
		{Result: "{not json} and no verdict line"},
		{StructuredOutput: json.RawMessage(`{"verdict":"maybe","review":"x"}`)},
		{StructuredOutput: json.RawMessage(`{"verdict":"approved","review":" "}`)},
	} {
		if _, err := decodeReviewAnswer(bad); err == nil {
			t.Fatalf("decodeReviewAnswer(%+v) error = nil, want rejection", bad)
		}
	}
}
