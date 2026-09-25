package agent

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/tody-aja/jira-ai-agent/internal/workflow"
)

const (
	DefaultOpenCodeExecutable = "opencode"
	DefaultOpenCodeModel      = "openai/gpt-5.6-luna"
	DefaultOpenCodeTimeout    = 120 * time.Second
)

type OpenCodeConfig struct {
	Executable string
	Model      string
	Timeout    time.Duration
}

type OpenCodePRDGenerator struct {
	executable string
	model      string
	timeout    time.Duration
}

func NewOpenCodePRDGenerator(config OpenCodeConfig) (*OpenCodePRDGenerator, error) {
	executable := strings.TrimSpace(config.Executable)
	if executable == "" {
		executable = DefaultOpenCodeExecutable
	}
	model := strings.TrimSpace(config.Model)
	if model == "" {
		model = DefaultOpenCodeModel
	}
	timeout := config.Timeout
	if timeout <= 0 {
		timeout = DefaultOpenCodeTimeout
	}

	return &OpenCodePRDGenerator{
		executable: executable,
		model:      model,
		timeout:    timeout,
	}, nil
}

const opencodePRDPrompt = `Create a structured product requirements document from this Jira issue.

Use exactly these sections: Problem, Goals, Non-Goals, Functional Requirements, Non-Functional Requirements, Acceptance Criteria, Risks/Assumptions, and Open Questions.
Preserve uncertainty explicitly. Never invent missing business requirements; put unresolved information in Risks/Assumptions or Open Questions instead.

Issue key: %s
Summary: %s
Description:
%s`

func (g *OpenCodePRDGenerator) Generate(ctx context.Context, input workflow.PRDInput) (string, error) {
	commandContext, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()

	prompt := fmt.Sprintf(opencodePRDPrompt, input.IssueKey, input.Summary, input.Description)
	command := exec.CommandContext(commandContext, g.executable, "run", "--format", "default", "--model", g.model, prompt)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr

	if err := command.Run(); err != nil {
		if commandContext.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("OpenCode command timed out: %w", context.DeadlineExceeded)
		}
		if commandContext.Err() == context.Canceled {
			return "", fmt.Errorf("OpenCode command canceled: %w", context.Canceled)
		}
		return "", fmt.Errorf("OpenCode command failed")
	}

	result := strings.TrimSpace(stdout.String())
	if result == "" {
		return "", fmt.Errorf("OpenCode command produced no generated output")
	}
	return result, nil
}

var _ workflow.PRDGenerator = (*OpenCodePRDGenerator)(nil)
