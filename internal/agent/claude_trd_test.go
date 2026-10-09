package agent

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tody-aja/jira-ai-agent/internal/jira"
	"github.com/tody-aja/jira-ai-agent/internal/workflow"
)

func testTRDInput() workflow.TRDInput {
	return workflow.TRDInput{
		IssueKey:    "DEMO-42",
		Summary:     "Build a workflow action",
		Description: "The original issue description.",
		PRD:         "<h2>Problem</h2><p>Users cannot export.</p>",
	}
}

func newTestTRDGenerator(t *testing.T, mode, repoPath string) *ClaudeTRDGenerator {
	t.Helper()
	t.Setenv("CLAUDE_TEST_HELPER", "1")
	t.Setenv("CLAUDE_TEST_MODE", mode)
	t.Setenv("CLAUDE_TEST_EXPECT_MODEL", "")
	t.Setenv("CLAUDE_TEST_EXPECT_DIR", repoPath)
	t.Setenv("ANTHROPIC_API_KEY", "must-not-reach-cli")

	generator, err := NewClaudeTRDGenerator(ClaudeConfig{Executable: os.Args[0], PlanningTimeout: 5 * time.Second}, repoPath)
	if err != nil {
		t.Fatalf("NewClaudeTRDGenerator() error = %v", err)
	}
	return generator
}

func TestClaudeTRDGeneratorRunsPlannerInRepository(t *testing.T) {
	repo := t.TempDir()

	got, err := newTestTRDGenerator(t, "success", repo).GenerateTRD(context.Background(), testTRDInput())

	if err != nil {
		t.Fatalf("GenerateTRD() error = %v", err)
	}
	if got != "# Problem\nGenerated PRD" {
		t.Fatalf("GenerateTRD() = %q, want trimmed result", got)
	}
}

func TestClaudeTRDGeneratorRejectsMissingRepository(t *testing.T) {
	for name, repoPath := range map[string]string{"unset": "", "missing": t.TempDir() + "/does-not-exist"} {
		t.Run(name, func(t *testing.T) {
			generator, err := NewClaudeTRDGenerator(ClaudeConfig{Executable: os.Args[0]}, repoPath)
			if err != nil {
				t.Fatalf("NewClaudeTRDGenerator() error = %v", err)
			}
			if _, err := generator.GenerateTRD(context.Background(), testTRDInput()); err == nil || !strings.Contains(err.Error(), "REPO_PATH") {
				t.Fatalf("GenerateTRD() error = %v, want REPO_PATH error", err)
			}
		})
	}
}

func TestNewClaudeTRDGeneratorResolvesRelativeRepoPath(t *testing.T) {
	generator, err := NewClaudeTRDGenerator(ClaudeConfig{Executable: os.Args[0]}, ".")
	if err != nil {
		t.Fatalf("NewClaudeTRDGenerator() error = %v", err)
	}
	cwd, _ := os.Getwd()
	if generator.repoPath != cwd || generator.timeout != DefaultClaudePlanningTimeout {
		t.Fatalf("generator = (%q, %s), want (%q, default planning timeout)", generator.repoPath, generator.timeout, cwd)
	}
}

func TestTRDPromptAsksForRevisionWhenThereIsFeedback(t *testing.T) {
	input := testTRDInput()
	if got := trdPrompt(input); !strings.HasPrefix(got, "Write the TRD") || strings.Contains(got, "feedback") {
		t.Fatalf("trdPrompt() without feedback = %q, want initial TRD prompt", got)
	}

	input.CurrentTRD = "<h2>Summary</h2>"
	input.Feedback = []workflow.Feedback{{Comment: jira.Comment{Author: "Ana", Created: "2026-09-01"}, Text: "Use localStorage"}}
	got := trdPrompt(input)
	for _, want := range []string{"Revise the TRD", "Approved PRD", "<h2>Problem</h2>", "Current TRD", "<h2>Summary</h2>", "[Comment 1 by Ana at 2026-09-01]\nUse localStorage"} {
		if !strings.Contains(got, want) {
			t.Fatalf("trdPrompt() = %q, want it to contain %q", got, want)
		}
	}
}
