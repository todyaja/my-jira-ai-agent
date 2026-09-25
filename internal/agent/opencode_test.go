package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tody-aja/jira-ai-agent/internal/workflow"
)

func TestOpenCodeHelperProcess(t *testing.T) {
	if os.Getenv("OPENCODE_TEST_HELPER") == "" {
		return
	}
	runOpenCodeHelper()
}

func TestMain(m *testing.M) {
	if os.Getenv("OPENCODE_TEST_HELPER") != "" {
		runOpenCodeHelper()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runOpenCodeHelper() {
	args := os.Args[1:]
	if len(args) < 6 || args[0] != "run" || args[1] != "--format" || args[2] != "default" || args[3] != "--model" {
		fmt.Fprintln(os.Stderr, "unexpected command arguments")
		os.Exit(2)
	}
	if os.Getenv("OPENCODE_TEST_EXPECT_MODEL") != args[4] {
		fmt.Fprintln(os.Stderr, "unexpected model")
		os.Exit(2)
	}
	prompt := args[5]
	for _, expected := range []string{"DEMO-42", "Build a workflow action", "Description", "Open Questions"} {
		if !strings.Contains(prompt, expected) {
			fmt.Fprintln(os.Stderr, "prompt missing expected content")
			os.Exit(2)
		}
	}

	switch os.Getenv("OPENCODE_TEST_MODE") {
	case "empty":
		return
	case "failure":
		fmt.Fprintln(os.Stderr, "private helper diagnostic")
		os.Exit(17)
	case "timeout":
		time.Sleep(time.Second)
	default:
		fmt.Fprintln(os.Stdout, "  # Problem\nGenerated PRD  ")
	}
}

func TestOpenCodePRDGeneratorGenerate(t *testing.T) {
	generator, err := NewOpenCodePRDGenerator(OpenCodeConfig{
		Executable: os.Args[0],
		Model:      "configured-model",
		Timeout:    time.Second,
	})
	if err != nil {
		t.Fatalf("NewOpenCodePRDGenerator() error = %v", err)
	}
	t.Setenv("OPENCODE_TEST_HELPER", "1")
	t.Setenv("OPENCODE_TEST_EXPECT_MODEL", "configured-model")
	t.Setenv("OPENCODE_TEST_MODE", "success")

	got, err := generator.Generate(context.Background(), workflow.PRDInput{
		IssueKey:    "DEMO-42",
		Summary:     "Build a workflow action",
		Description: "The original issue description.",
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if got != "# Problem\nGenerated PRD" {
		t.Fatalf("Generate() = %q, want trimmed stdout", got)
	}
}

func TestOpenCodePRDGeneratorRejectsEmptyOutput(t *testing.T) {
	generator := newTestOpenCodeGenerator(t, "empty", 0)
	_, err := generator.Generate(context.Background(), testPRDInput())
	if err == nil || !strings.Contains(err.Error(), "no generated output") {
		t.Fatalf("Generate() error = %v, want empty output error", err)
	}
}

func TestOpenCodePRDGeneratorSuppressesFailureDetails(t *testing.T) {
	generator := newTestOpenCodeGenerator(t, "failure", 0)
	_, err := generator.Generate(context.Background(), testPRDInput())
	if err == nil {
		t.Fatal("Generate() error = nil, want command failure")
	}
	if strings.Contains(err.Error(), "private helper diagnostic") || strings.Contains(err.Error(), "DEMO-42") {
		t.Fatalf("Generate() error exposed private details: %v", err)
	}
}

func TestOpenCodePRDGeneratorHonorsTimeout(t *testing.T) {
	generator := newTestOpenCodeGenerator(t, "timeout", 20*time.Millisecond)
	_, err := generator.Generate(context.Background(), testPRDInput())
	if err == nil {
		t.Fatal("Generate() error = nil, want timeout error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Generate() error = %v, want context deadline exceeded", err)
	}
}

func TestNewOpenCodePRDGeneratorUsesDefaults(t *testing.T) {
	generator, err := NewOpenCodePRDGenerator(OpenCodeConfig{})
	if err != nil {
		t.Fatalf("NewOpenCodePRDGenerator() error = %v", err)
	}
	if generator.executable != DefaultOpenCodeExecutable {
		t.Errorf("executable = %q, want %q", generator.executable, DefaultOpenCodeExecutable)
	}
	if generator.model != DefaultOpenCodeModel {
		t.Errorf("model = %q, want %q", generator.model, DefaultOpenCodeModel)
	}
}

func newTestOpenCodeGenerator(t *testing.T, mode string, timeout time.Duration) *OpenCodePRDGenerator {
	t.Helper()
	t.Setenv("OPENCODE_TEST_HELPER", "1")
	t.Setenv("OPENCODE_TEST_EXPECT_MODEL", "configured-model")
	t.Setenv("OPENCODE_TEST_MODE", mode)

	generator, err := NewOpenCodePRDGenerator(OpenCodeConfig{
		Executable: os.Args[0],
		Model:      "configured-model",
		Timeout:    timeout,
	})
	if err != nil {
		t.Fatalf("NewOpenCodePRDGenerator() error = %v", err)
	}
	return generator
}

func testPRDInput() workflow.PRDInput {
	return workflow.PRDInput{
		IssueKey:    "DEMO-42",
		Summary:     "Build a workflow action",
		Description: "The original issue description.",
	}
}
