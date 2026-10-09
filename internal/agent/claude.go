package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/tody-aja/jira-ai-agent/internal/workflow"
)

const (
	DefaultClaudeExecutable      = "claude"
	DefaultClaudeTimeout         = 300 * time.Second
	DefaultClaudePlanningTimeout = 900 * time.Second
	// Implementing and reviewing read, change and test code, so they get
	// the longest limits.
	DefaultClaudeImplementationTimeout = 1800 * time.Second
	DefaultClaudeReviewTimeout         = 900 * time.Second
)

// DefaultAllowedCommands are the shell command prefixes the engineer and
// reviewer may run: installing dependencies, testing, linting, building and
// reading git history. Nothing that commits or pushes.
var DefaultAllowedCommands = []string{
	"npm ci", "npm install", "npm test", "npm run lint", "npm run build",
	"npx vitest", "npx tsc", "npx eslint",
	"git status", "git diff", "git log", "git show",
}

// The prd-maker agent and its skill (plugin/) define how the PRD is written;
// the issue itself is sent on stdin.
const claudePRDPrompt = `Write the PRD for this Jira issue.

Issue key: %s
Summary: %s
Description:
%s`

const claudeRevisionPrompt = `Revise the PRD for this Jira issue based on the reviewer feedback below. Change only what the feedback asks for and keep the rest of the current PRD as it is. Cross-check every comment against the current PRD first and skip any it already addresses.

Issue key: %s
Summary: %s
Description:
%s

Current PRD (Confluence storage format):
%s

Reviewer feedback, oldest first:
%s`

type ClaudeConfig struct {
	Executable string
	Model      string
	Timeout    time.Duration
	// PlanningTimeout bounds the technical planner, which reads the
	// repository and so runs longer than PRD writing.
	PlanningTimeout       time.Duration
	ImplementationTimeout time.Duration
	ReviewTimeout         time.Duration
	// AllowedCommands are the shell command prefixes the engineer and
	// reviewer may run.
	AllowedCommands []string
}

// claudeCLI runs one agent of the embedded plugin through the Claude Code
// CLI in print mode, so it uses the plan the CLI is logged in with rather
// than an API key.
type claudeCLI struct {
	executable string
	pluginDir  string
	model      string
}

func newClaudeCLI(config ClaudeConfig) (claudeCLI, error) {
	executable := strings.TrimSpace(config.Executable)
	if executable == "" {
		executable = DefaultClaudeExecutable
	}
	pluginDir, err := installPlugin(os.TempDir())
	if err != nil {
		return claudeCLI{}, err
	}
	return claudeCLI{executable: executable, pluginDir: pluginDir, model: strings.TrimSpace(config.Model)}, nil
}

// ClaudePRDGenerator writes PRDs with the prd-maker agent.
type ClaudePRDGenerator struct {
	cli     claudeCLI
	timeout time.Duration
}

func NewClaudePRDGenerator(config ClaudeConfig) (*ClaudePRDGenerator, error) {
	cli, err := newClaudeCLI(config)
	if err != nil {
		return nil, err
	}
	return &ClaudePRDGenerator{cli: cli, timeout: timeoutOrDefault(config.Timeout, DefaultClaudeTimeout)}, nil
}

type claudeResult struct {
	Subtype          string          `json:"subtype"`
	IsError          bool            `json:"is_error"`
	Result           string          `json:"result"`
	StructuredOutput json.RawMessage `json:"structured_output"`
}

// Generate runs the prd-maker agent from a scratch directory. Skill is its
// only tool, so it can load its skill but never read files: the PRD comes
// from the issue text alone.
func (g *ClaudePRDGenerator) Generate(ctx context.Context, input workflow.PRDInput) (string, error) {
	return g.cli.run(ctx, claudeRun{
		agent:   PRDMakerAgent,
		tools:   "Skill",
		dir:     os.TempDir(),
		prompt:  prdPrompt(input),
		timeout: g.timeout,
	})
}

// prdPrompt asks for a new PRD, or for a revision when the issue has
// reviewer feedback.
func prdPrompt(input workflow.PRDInput) string {
	if len(input.Feedback) == 0 {
		return fmt.Sprintf(claudePRDPrompt, input.IssueKey, input.Summary, input.Description)
	}
	currentPRD := input.CurrentPRD
	if strings.TrimSpace(currentPRD) == "" {
		currentPRD = "(none published - write the PRD from the issue and the feedback)"
	}
	return fmt.Sprintf(claudeRevisionPrompt, input.IssueKey, input.Summary, input.Description, currentPRD, workflow.FormatFeedback(input.Feedback))
}

type claudeRun struct {
	agent   string
	tools   string
	dir     string
	prompt  string
	timeout time.Duration
	// permissionMode, when set, is passed to --permission-mode.
	permissionMode string
	// allowedTools are permission rules that skip the prompt, such as
	// "Bash(npm test:*)".
	allowedTools []string
	// jsonSchema, when set, makes the agent answer with structured output
	// matching it.
	jsonSchema string
}

// run executes one agent with only the given tools, in dir, and returns its
// final answer.
func (c claudeCLI) run(ctx context.Context, spec claudeRun) (string, error) {
	result, err := c.runResult(ctx, spec)
	if err != nil {
		return "", err
	}
	output := strings.TrimSpace(result.Result)
	if output == "" {
		return "", fmt.Errorf("Claude command produced no generated output")
	}
	return output, nil
}

// runResult executes one agent and returns its decoded result.
func (c claudeCLI) runResult(ctx context.Context, spec claudeRun) (claudeResult, error) {
	commandContext, cancel := context.WithTimeout(ctx, spec.timeout)
	defer cancel()

	command := exec.CommandContext(commandContext, c.executable, c.args(spec)...)
	// Issue text goes on stdin, never on the command line.
	command.Stdin = strings.NewReader(spec.prompt)
	command.Dir = spec.dir
	command.Env = withoutAPIKeys(os.Environ())
	command.WaitDelay = 5 * time.Second
	var stdout bytes.Buffer
	command.Stdout = &stdout

	if err := command.Run(); err != nil {
		if commandContext.Err() == context.DeadlineExceeded {
			return claudeResult{}, fmt.Errorf("Claude command timed out: %w", context.DeadlineExceeded)
		}
		if commandContext.Err() == context.Canceled {
			return claudeResult{}, fmt.Errorf("Claude command canceled: %w", context.Canceled)
		}
		if message := claudeErrorMessage(stdout.Bytes()); message != "" {
			return claudeResult{}, fmt.Errorf("Claude command failed: %s", message)
		}
		return claudeResult{}, fmt.Errorf("Claude command failed")
	}

	var result claudeResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		return claudeResult{}, fmt.Errorf("decode Claude output: %w", err)
	}
	if result.IsError || result.Subtype != "success" {
		return claudeResult{}, fmt.Errorf("Claude command failed: %s", result.Subtype)
	}
	return result, nil
}

func (c claudeCLI) args(spec claudeRun) []string {
	args := []string{"-p", "--output-format", "json", "--plugin-dir", c.pluginDir, "--agent", spec.agent, "--tools", spec.tools}
	if c.model != "" {
		args = append(args, "--model", c.model)
	}
	if spec.permissionMode != "" {
		args = append(args, "--permission-mode", spec.permissionMode)
	}
	if len(spec.allowedTools) > 0 {
		args = append(args, "--allowedTools", strings.Join(spec.allowedTools, ","))
	}
	if spec.jsonSchema != "" {
		args = append(args, "--json-schema", spec.jsonSchema)
	}
	return args
}

// bashRules turns command prefixes into Bash permission rules.
func bashRules(commands []string) []string {
	rules := make([]string, 0, len(commands))
	for _, command := range commands {
		if command = strings.TrimSpace(command); command != "" {
			rules = append(rules, "Bash("+command+":*)")
		}
	}
	return rules
}

func timeoutOrDefault(timeout, fallback time.Duration) time.Duration {
	if timeout <= 0 {
		return fallback
	}
	return timeout
}

// claudeErrorMessage extracts the result subtype from a failed run's JSON
// output without echoing any generated text.
func claudeErrorMessage(output []byte) string {
	var result claudeResult
	if err := json.Unmarshal(output, &result); err != nil {
		return ""
	}
	return result.Subtype
}

// withoutAPIKeys drops Anthropic credentials so the CLI falls back to the
// logged-in subscription instead of billing an API key.
func withoutAPIKeys(environment []string) []string {
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(name, "ANTHROPIC_API_KEY") || strings.EqualFold(name, "ANTHROPIC_AUTH_TOKEN") {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

var _ workflow.PRDGenerator = (*ClaudePRDGenerator)(nil)
