package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
// reviewer may run besides the checks worked out from the worktree (see
// Checks): reading git history. Nothing that commits or pushes.
var DefaultAllowedCommands = []string{
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
	// reviewer may run besides the worktree's checks.
	AllowedCommands []string
	// StepModels overrides Model for single steps, by Step* name.
	StepModels map[string]string
	// DevDB, when set, lets the bank planner read and the engineer change
	// the development database.
	DevDB DevDB
	// Provider is the AI provider every run uses: ProviderDeepSeek (the
	// default) or ProviderAnthropic, which is opt-in.
	Provider string
	// providerEnv is added to every Claude Code process. In DeepSeek mode it
	// carries the endpoint, model aliases and API token; it is never logged.
	providerEnv []string
}

// ProviderName returns the configured provider, defaulting to DeepSeek.
func (c ClaudeConfig) ProviderName() string {
	if c.Provider == "" {
		return ProviderDeepSeek
	}
	return c.Provider
}

// claudeCLI runs one agent of the embedded plugin through the Claude Code
// CLI in print mode, with the provider environment the workflow configured.
type claudeCLI struct {
	executable string
	pluginDir  string
	model      string
	// environment is the provider configuration every run gets, including
	// the API token, so it is never written to a log or the command line.
	environment []string
}

// newClaudeCLI runs agents with the model configured for step.
func newClaudeCLI(config ClaudeConfig, step string) (claudeCLI, error) {
	executable := strings.TrimSpace(config.Executable)
	if executable == "" {
		executable = DefaultClaudeExecutable
	}
	provider := config.ProviderName()
	if provider == ProviderDeepSeek && len(config.providerEnv) == 0 {
		return claudeCLI{}, fmt.Errorf("CLAUDE_PROVIDER=deepseek needs DEEPSEEK_API_KEY; load the configuration with LoadClaudeConfig")
	}
	pluginDir, err := installPlugin(pluginRoot())
	if err != nil {
		return claudeCLI{}, err
	}
	return claudeCLI{executable: executable, pluginDir: pluginDir, model: config.ModelFor(step), environment: config.providerEnv}, nil
}

// ClaudePRDGenerator writes PRDs with the prd-maker agent.
type ClaudePRDGenerator struct {
	cli     claudeCLI
	timeout time.Duration
}

func NewClaudePRDGenerator(config ClaudeConfig) (*ClaudePRDGenerator, error) {
	cli, err := newClaudeCLI(config, StepPRD)
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
	// addDirs are directories the agent may use besides dir.
	addDirs []string
	// model, when set, overrides the CLI's model for this run.
	model string
	// env are extra NAME=value entries for the agent's environment.
	env []string
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

// maxClaudeAttempts bounds one agent's attempts. Only failures that may clear
// up on their own are retried; a timeout or a deterministic failure is
// reported at once so the workflow parks the issue in AI ERROR.
const maxClaudeAttempts = 2

// claudeRetryDelay is the pause before a retry; tests set it to zero.
var claudeRetryDelay = 2 * time.Second

// claudeFailure is a failed agent run. transient marks failures, such as a
// rate limit or a dropped connection, that may succeed when retried.
type claudeFailure struct {
	message   string
	transient bool
}

func (f claudeFailure) Error() string { return f.message }

// runResult executes one agent, retrying a transient failure once, and
// returns its decoded result.
func (c claudeCLI) runResult(ctx context.Context, spec claudeRun) (claudeResult, error) {
	var lastErr error
	attempts := 0
	for attempts < maxClaudeAttempts {
		attempts++
		result, err := c.runOnce(ctx, spec)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if attempts == maxClaudeAttempts || !isTransientClaudeFailure(err) {
			break
		}
		select {
		case <-ctx.Done():
			return claudeResult{}, lastErr
		case <-time.After(claudeRetryDelay):
		}
	}
	if attempts > 1 {
		return claudeResult{}, fmt.Errorf("%w (after %d attempts)", lastErr, attempts)
	}
	return claudeResult{}, lastErr
}

func isTransientClaudeFailure(err error) bool {
	var failure claudeFailure
	return errors.As(err, &failure) && failure.transient
}

func (c claudeCLI) runOnce(ctx context.Context, spec claudeRun) (claudeResult, error) {
	commandContext, cancel := context.WithTimeout(ctx, spec.timeout)
	defer cancel()

	args := c.args(spec)
	if len(c.environment) > 0 {
		settingsPath, err := writeProviderSettings(c.environment)
		if err != nil {
			return claudeResult{}, fmt.Errorf("write Claude provider settings: %w", err)
		}
		defer os.RemoveAll(filepath.Dir(settingsPath))
		args = append(args, "--settings", settingsPath)
	}
	command := exec.CommandContext(commandContext, c.executable, args...)
	// Issue text goes on stdin, never on the command line.
	command.Stdin = strings.NewReader(spec.prompt)
	command.Dir = spec.dir
	command.Env = append(claudeProcessEnv(os.Environ(), c.environment), spec.env...)
	command.WaitDelay = 5 * time.Second
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr

	if err := command.Run(); err != nil {
		if commandContext.Err() == context.DeadlineExceeded {
			return claudeResult{}, fmt.Errorf("Claude command timed out: %w", context.DeadlineExceeded)
		}
		if commandContext.Err() == context.Canceled {
			return claudeResult{}, fmt.Errorf("Claude command canceled: %w", context.Canceled)
		}
		redactedStderr := redactSecrets(stderr.String(), c.environment)
		if message := claudeErrorMessage(stdout.Bytes()); message != "" {
			stderrLine := stderrSummary(redactedStderr)
			transient := transientSubtype(message) || (stderrLine != "" && transientProviderMessage(stderrLine))
			return claudeResult{}, claudeFailure{message: "Claude command failed: " + message, transient: transient}
		}
		if line := stderrSummary(redactedStderr); line != "" {
			return claudeResult{}, claudeFailure{message: "Claude command failed: " + line, transient: transientProviderMessage(line)}
		}
		return claudeResult{}, claudeFailure{message: "Claude command failed"}
	}

	var result claudeResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		// A malformed answer may mean the run already did its work, so it is
		// reported rather than repeated.
		return claudeResult{}, claudeFailure{message: fmt.Sprintf("decode Claude output: %v", err)}
	}
	if result.IsError || result.Subtype != "success" {
		return claudeResult{}, claudeFailure{message: "Claude command failed: " + result.Subtype, transient: transientSubtype(result.Subtype)}
	}
	return result, nil
}

// transientSubtype reports whether a CLI result subtype names a failure that
// may clear up on a retry. Unknown and deterministic failures are not
// retried: the run may already have changed the worktree.
func transientSubtype(subtype string) bool {
	lower := strings.ToLower(subtype)
	for _, marker := range []string{"rate_limit", "ratelimit", "overloaded", "unavailable", "timeout"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// redactSecrets removes provider credentials from text that may reach an
// error or a log, even if the CLI echoes its environment.
func redactSecrets(text string, environment []string) string {
	for _, entry := range environment {
		name, value, found := strings.Cut(entry, "=")
		if !found || value == "" {
			continue
		}
		upper := strings.ToUpper(name)
		if strings.Contains(upper, "TOKEN") || strings.Contains(upper, "KEY") {
			text = strings.ReplaceAll(text, value, "[redacted]")
		}
	}
	return text
}

// transientProviderMessage recognizes rate limits, overloads and connection
// failures in the CLI's first stderr line. Anything unrecognized is reported
// at once rather than retried blindly.
func transientProviderMessage(line string) bool {
	lower := strings.ToLower(line)
	for _, marker := range []string{
		"429", "rate limit", "too many requests", "overloaded",
		"502", "503", "504", "connection reset", "econnreset",
		"etimedout", "temporarily unavailable", "timeout",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// managedProviderEnv are the routing variables that always come from the
// workflow, never from the parent shell. They are removed even in Anthropic
// mode, so an inherited DeepSeek configuration cannot leak into a run that
// opted into the CLI's own login.
var managedProviderEnv = []string{
	"ANTHROPIC_API_KEY",
	"ANTHROPIC_AUTH_TOKEN",
	"CLAUDE_CODE_OAUTH_TOKEN",
	"ANTHROPIC_BASE_URL",
	"ANTHROPIC_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL",
	"ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL",
	"CLAUDE_CODE_SUBAGENT_MODEL",
	"CLAUDE_CODE_SUBAGENT_MODEL_FORCE",
	"CLAUDE_CODE_USE_BEDROCK",
	"CLAUDE_CODE_USE_VERTEX",
	"CLAUDE_CODE_USE_FOUNDRY",
}

// claudeProcessEnv builds one agent's environment. The variables the workflow
// controls are dropped from the parent and replaced by providerEnv, so an
// inherited Anthropic credential or base URL cannot override the configured
// provider, and the token only reaches the CLI process and its private
// settings file.
func claudeProcessEnv(parent, providerEnv []string) []string {
	managed := make(map[string]bool, len(managedProviderEnv)+len(providerEnv))
	for _, name := range managedProviderEnv {
		managed[name] = true
	}
	for _, entry := range providerEnv {
		if name, _, found := strings.Cut(entry, "="); found {
			managed[strings.ToUpper(name)] = true
		}
	}
	environment := make([]string, 0, len(parent)+len(providerEnv))
	for _, entry := range parent {
		if name, _, found := strings.Cut(entry, "="); found && managed[strings.ToUpper(name)] {
			continue
		}
		environment = append(environment, entry)
	}
	return append(environment, providerEnv...)
}

// writeProviderSettings writes the provider environment to a private settings
// file. A settings env block outranks the process environment, so this is
// what keeps user, project or local settings from pointing a run at another
// provider. The file holds the API token, so it lives in a private per-run
// directory and is removed when the run ends; the token is never logged or
// passed as an argument value.
func writeProviderSettings(environment []string) (string, error) {
	dir, err := os.MkdirTemp("", "jira-ai-agent-provider-")
	if err != nil {
		return "", err
	}
	values := make(map[string]string, len(environment))
	for _, entry := range environment {
		if name, value, found := strings.Cut(entry, "="); found {
			values[name] = value
		}
	}
	content, err := json.Marshal(map[string]map[string]string{"env": values})
	if err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		os.RemoveAll(dir)
		return "", err
	}
	return path, nil
}

func (c claudeCLI) args(spec claudeRun) []string {
	args := []string{"-p", "--output-format", "json", "--plugin-dir", c.pluginDir, "--agent", spec.agent, "--tools", spec.tools}
	if model := firstNonEmpty(spec.model, c.model); model != "" {
		args = append(args, "--model", model)
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
	for _, dir := range spec.addDirs {
		args = append(args, "--add-dir", dir)
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

// maxStderrSummary caps how much of the CLI's stderr reaches an error.
const maxStderrSummary = 200

// stderrSummary returns the first line of a failed run's stderr, capped at
// maxStderrSummary bytes. That line names startup failures such as an unknown
// agent or a logged-out CLI; the rest is dropped so issue text the CLI
// might echo stays out of errors and logs.
func stderrSummary(stderr string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(stderr), "\n")
	line = strings.TrimSpace(line)
	if len(line) > maxStderrSummary {
		line = strings.ToValidUTF8(line[:maxStderrSummary], "") + "..."
	}
	return line
}

var _ workflow.PRDGenerator = (*ClaudePRDGenerator)(nil)
