package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tody-aja/jira-ai-agent/internal/jira"
	"github.com/tody-aja/jira-ai-agent/internal/workflow"
)

// testDeepSeekKey is a fake key, never a real credential.
const testDeepSeekKey = "test-deepseek-key"

func TestMain(m *testing.M) {
	claudeRetryDelay = 0
	if os.Getenv("CLAUDE_TEST_HELPER") != "" {
		runClaudeHelper()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// testClaudeConfig is a DeepSeek configuration whose provider environment is
// built the same way as production.
func testClaudeConfig() ClaudeConfig {
	return ClaudeConfig{Executable: os.Args[0], Provider: ProviderDeepSeek, providerEnv: testDeepSeekEnvironment()}
}

func testDeepSeekEnvironment() []string {
	environment, err := loadProviderEnvironment(func(key string) string {
		if key == "DEEPSEEK_API_KEY" {
			return testDeepSeekKey
		}
		return ""
	}, ProviderDeepSeek)
	if err != nil {
		panic(err)
	}
	return environment
}

func testProviderEnvironmentValues() map[string]string {
	values := map[string]string{}
	for _, entry := range testDeepSeekEnvironment() {
		name, value, _ := strings.Cut(entry, "=")
		values[name] = value
	}
	return values
}

func testPRDInput() workflow.PRDInput {
	return workflow.PRDInput{
		IssueKey:    "DEMO-42",
		Summary:     "Build a workflow action",
		Description: "The original issue description.",
	}
}

// runClaudeHelper stands in for the claude executable when the test binary is
// re-invoked with CLAUDE_TEST_HELPER set.
func runClaudeHelper() {
	settingsPath, args := takeSettingsArg(os.Args[1:])
	checkClaudeHelperSettings(settingsPath)
	agentName, tools := "prd-maker", "Skill"
	expectedPrompt := []string{"Issue key: DEMO-42", "Summary: Build a workflow action", "The original issue description."}
	if dir := os.Getenv("CLAUDE_TEST_EXPECT_DIR"); dir != "" {
		agentName, tools = "technical-planner", "Skill,Read,Grep,Glob"
		expectedPrompt = append(expectedPrompt, "Approved PRD", "<h2>Problem</h2>")
		if cwd, _ := os.Getwd(); !strings.EqualFold(filepath.Clean(cwd), filepath.Clean(dir)) {
			fmt.Fprintf(os.Stderr, "working directory = %q, want repository %q\n", cwd, dir)
			os.Exit(2)
		}
	}
	want := []string{"-p", "--output-format", "json", "--plugin-dir", "<plugin>", "--agent", "jira-ai-agent:" + agentName, "--tools", tools}
	if model := os.Getenv("CLAUDE_TEST_EXPECT_MODEL"); model != "" {
		want = append(want, "--model", model)
	}
	pluginDir := ""
	for index := 0; index+1 < len(args); index++ {
		if args[index] == "--plugin-dir" {
			pluginDir = args[index+1]
			args[index+1] = "<plugin>"
			break
		}
	}
	if _, err := os.Stat(filepath.Join(pluginDir, "agents", agentName+".md")); err != nil {
		fmt.Fprintf(os.Stderr, "plugin directory has no %s agent: %q\n", agentName, pluginDir)
		os.Exit(2)
	}
	if !slices.Equal(args, want) {
		fmt.Fprintf(os.Stderr, "unexpected command arguments: %q\n", args)
		os.Exit(2)
	}
	checkClaudeHelperEnvironment()
	writeClaudeHelperEnvironment()
	attempt := recordClaudeHelperAttempt()
	stdin, _ := io.ReadAll(os.Stdin)
	for _, expected := range expectedPrompt {
		if !strings.Contains(string(stdin), expected) {
			fmt.Fprintln(os.Stderr, "prompt missing expected content")
			os.Exit(2)
		}
	}

	switch os.Getenv("CLAUDE_TEST_MODE") {
	case "empty":
		fmt.Print(`{"type":"result","subtype":"success","is_error":false,"result":"  "}`)
	case "error-result":
		fmt.Print(`{"type":"result","subtype":"error_max_turns","is_error":true,"result":"partial"}`)
		os.Exit(1)
	case "failure":
		fmt.Fprintln(os.Stderr, "helper agent not found")
		fmt.Fprintln(os.Stderr, "private helper diagnostic")
		os.Exit(17)
	case "invalid-json":
		fmt.Print("not json")
	case "transient":
		if attempt == 1 {
			fmt.Fprintln(os.Stderr, "503 Service Unavailable")
			os.Exit(17)
		}
		fmt.Print(`{"type":"result","subtype":"success","is_error":false,"result":"  # Problem\nGenerated PRD  "}`)
	case "always-transient":
		fmt.Fprintln(os.Stderr, "429 rate limit")
		os.Exit(17)
	case "leak-secret":
		fmt.Fprintf(os.Stderr, "auth failed for token %s\n", os.Getenv("ANTHROPIC_AUTH_TOKEN"))
		os.Exit(17)
	case "timeout":
		time.Sleep(time.Second)
	default:
		fmt.Print(`{"type":"result","subtype":"success","is_error":false,"result":"  # Problem\nGenerated PRD  "}`)
	}
}

// takeSettingsArg removes the --settings pair from the helper's arguments and
// returns its path.
func takeSettingsArg(args []string) (string, []string) {
	for index := 0; index+1 < len(args); index++ {
		if args[index] == "--settings" {
			return args[index+1], append(append([]string{}, args[:index]...), args[index+2:]...)
		}
	}
	return "", args
}

// checkClaudeHelperSettings proves the provider environment was passed in a
// settings file, which outranks user/project settings, and that Anthropic
// mode does not create one.
func checkClaudeHelperSettings(path string) {
	if provider := os.Getenv("CLAUDE_TEST_EXPECT_PROVIDER"); provider != ProviderDeepSeek {
		if path != "" {
			fmt.Fprintln(os.Stderr, "unexpected --settings outside DeepSeek mode")
			os.Exit(2)
		}
		return
	}
	if path == "" {
		fmt.Fprintln(os.Stderr, "DeepSeek run has no --settings file")
		os.Exit(2)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "read provider settings: %v\n", err)
		os.Exit(2)
	}
	var settings struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(content, &settings); err != nil {
		fmt.Fprintf(os.Stderr, "decode provider settings: %v\n", err)
		os.Exit(2)
	}
	for name, want := range testProviderEnvironmentValues() {
		if got, found := settings.Env[name]; !found || got != want {
			fmt.Fprintf(os.Stderr, "settings env %s mismatch (set=%t)\n", name, found)
			os.Exit(2)
		}
	}
	if file := os.Getenv("CLAUDE_TEST_SETTINGS_PATH_FILE"); file != "" {
		if err := os.WriteFile(file, []byte(path), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "record settings path: %v\n", err)
			os.Exit(2)
		}
	}
}

// checkClaudeHelperEnvironment proves the parent built the CLI process
// environment the workflow configured, and that inherited Anthropic
// credentials were removed.
func checkClaudeHelperEnvironment() {
	switch provider := os.Getenv("CLAUDE_TEST_EXPECT_PROVIDER"); provider {
	case ProviderDeepSeek:
		if os.Getenv("ANTHROPIC_API_KEY") != "" {
			fmt.Fprintln(os.Stderr, "ANTHROPIC_API_KEY leaked to the CLI")
			os.Exit(2)
		}
		want := map[string]string{
			"ANTHROPIC_BASE_URL":               DefaultDeepSeekBaseURL,
			"ANTHROPIC_AUTH_TOKEN":             os.Getenv("CLAUDE_TEST_EXPECT_TOKEN"),
			"ANTHROPIC_MODEL":                  DefaultDeepSeekModel,
			"ANTHROPIC_DEFAULT_OPUS_MODEL":     DefaultDeepSeekModel,
			"ANTHROPIC_DEFAULT_SONNET_MODEL":   DefaultDeepSeekModel,
			"ANTHROPIC_DEFAULT_HAIKU_MODEL":    DefaultDeepSeekSubagentModel,
			"CLAUDE_CODE_SUBAGENT_MODEL":       DefaultDeepSeekSubagentModel,
			"CLAUDE_CODE_SUBAGENT_MODEL_FORCE": "1",
			"CLAUDE_CODE_EFFORT_LEVEL":         "medium",
			"CLAUDE_CODE_AUTO_MODE_SERVER":     "0",
		}
		for name, value := range want {
			if got := os.Getenv(name); got != value {
				fmt.Fprintf(os.Stderr, "%s = %q, want %q\n", name, got, value)
				os.Exit(2)
			}
		}
	case ProviderAnthropic:
		if os.Getenv("ANTHROPIC_API_KEY") != "" || os.Getenv("ANTHROPIC_AUTH_TOKEN") != "" {
			fmt.Fprintln(os.Stderr, "API credentials reached the CLI")
			os.Exit(2)
		}
	}
}

// writeClaudeHelperEnvironment saves the provider variables the helper
// received, so a test can check them as a set.
func writeClaudeHelperEnvironment() {
	file := os.Getenv("CLAUDE_TEST_ENV_FILE")
	if file == "" {
		return
	}
	var entries []string
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, "ANTHROPIC_") || strings.HasPrefix(upper, "CLAUDE_CODE_") {
			entries = append(entries, entry)
		}
	}
	sort.Strings(entries)
	if err := os.WriteFile(file, []byte(strings.Join(entries, "\n")), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "write environment: %v\n", err)
		os.Exit(2)
	}
}

// recordClaudeHelperAttempt counts how often one test run started the helper.
func recordClaudeHelperAttempt() int {
	file := os.Getenv("CLAUDE_TEST_ATTEMPT_FILE")
	if file == "" {
		return 1
	}
	attempt := 0
	if data, err := os.ReadFile(file); err == nil {
		attempt, _ = strconv.Atoi(strings.TrimSpace(string(data)))
	}
	attempt++
	if err := os.WriteFile(file, []byte(strconv.Itoa(attempt)), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "record attempt: %v\n", err)
		os.Exit(2)
	}
	return attempt
}

func newTestClaudeGenerator(t *testing.T, mode, model string, timeout time.Duration) *ClaudePRDGenerator {
	t.Helper()
	t.Setenv("CLAUDE_TEST_HELPER", "1")
	t.Setenv("CLAUDE_TEST_MODE", mode)
	t.Setenv("CLAUDE_TEST_EXPECT_MODEL", model)
	t.Setenv("CLAUDE_TEST_EXPECT_PROVIDER", ProviderDeepSeek)
	t.Setenv("CLAUDE_TEST_EXPECT_TOKEN", testDeepSeekKey)
	// Inherited credentials and endpoints must not reach the CLI.
	t.Setenv("ANTHROPIC_API_KEY", "must-not-reach-cli")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "must-not-reach-cli")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "must-not-reach-cli")
	t.Setenv("ANTHROPIC_BASE_URL", "https://api.anthropic.com")
	t.Setenv("ANTHROPIC_MODEL", "claude-opus-4-1")

	config := testClaudeConfig()
	config.Model = model
	config.Timeout = timeout
	generator, err := NewClaudePRDGenerator(config)
	if err != nil {
		t.Fatalf("NewClaudePRDGenerator() error = %v", err)
	}
	return generator
}

func TestClaudePRDGeneratorGenerate(t *testing.T) {
	for _, model := range []string{"", "opus"} {
		t.Run("model="+model, func(t *testing.T) {
			got, err := newTestClaudeGenerator(t, "success", model, 5*time.Second).Generate(context.Background(), testPRDInput())
			if err != nil {
				t.Fatalf("Generate() error = %v", err)
			}
			if got != "# Problem\nGenerated PRD" {
				t.Fatalf("Generate() = %q, want trimmed result", got)
			}
		})
	}
}

func TestClaudePRDGeneratorFailures(t *testing.T) {
	tests := []struct {
		mode    string
		wantErr string
	}{
		{mode: "empty", wantErr: "no generated output"},
		{mode: "error-result", wantErr: "Claude command failed: error_max_turns"},
		{mode: "failure", wantErr: "Claude command failed: helper agent not found"},
		{mode: "invalid-json", wantErr: "decode Claude output"},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			_, err := newTestClaudeGenerator(t, tt.mode, "", 5*time.Second).Generate(context.Background(), testPRDInput())
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Generate() error = %v, want %q", err, tt.wantErr)
			}
			if strings.Contains(err.Error(), "private helper diagnostic") || strings.Contains(err.Error(), "partial") {
				t.Fatalf("Generate() error exposed private details: %v", err)
			}
		})
	}
}

func TestClaudePRDGeneratorHonorsTimeout(t *testing.T) {
	_, err := newTestClaudeGenerator(t, "timeout", "", 50*time.Millisecond).Generate(context.Background(), testPRDInput())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Generate() error = %v, want context deadline exceeded", err)
	}
}

func TestClaudeProcessEnvironmentIsDeepSeek(t *testing.T) {
	envFile := filepath.Join(t.TempDir(), "env")
	settingsPathFile := filepath.Join(t.TempDir(), "settings-path")
	t.Setenv("CLAUDE_TEST_ENV_FILE", envFile)
	t.Setenv("CLAUDE_TEST_SETTINGS_PATH_FILE", settingsPathFile)

	if _, err := newTestClaudeGenerator(t, "success", "", 5*time.Second).Generate(context.Background(), testPRDInput()); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	environment := readClaudeHelperEnvironment(t, envFile)
	for name, want := range map[string]string{
		"ANTHROPIC_BASE_URL":             DefaultDeepSeekBaseURL,
		"ANTHROPIC_AUTH_TOKEN":           testDeepSeekKey,
		"ANTHROPIC_MODEL":                DefaultDeepSeekModel,
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   DefaultDeepSeekModel,
		"ANTHROPIC_DEFAULT_SONNET_MODEL": DefaultDeepSeekModel,
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  DefaultDeepSeekSubagentModel,
		"CLAUDE_CODE_SUBAGENT_MODEL":     DefaultDeepSeekSubagentModel,
		"CLAUDE_CODE_EFFORT_LEVEL":       "medium",
		"CLAUDE_CODE_AUTO_MODE_SERVER":   "0",
	} {
		if got := environment[name]; len(got) != 1 || got[0] != want {
			t.Errorf("%s = %q, want exactly %q", name, got, want)
		}
	}
	if got := environment["ANTHROPIC_API_KEY"]; len(got) != 0 {
		t.Errorf("ANTHROPIC_API_KEY reached the CLI: %q", got)
	}
	if got := environment["CLAUDE_CODE_OAUTH_TOKEN"]; len(got) != 0 {
		t.Errorf("CLAUDE_CODE_OAUTH_TOKEN reached the CLI: %q", got)
	}
	if got := environment["ANTHROPIC_AUTH_TOKEN"]; len(got) != 1 || got[0] != testDeepSeekKey {
		t.Errorf("ANTHROPIC_AUTH_TOKEN = %q, want the DeepSeek key only", got)
	}
	for _, name := range []string{"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"} {
		if got := environment[name]; len(got) != 1 || got[0] != "" {
			t.Errorf("%s = %q, want a single empty value that clears it", name, got)
		}
	}

	settingsPath, err := os.ReadFile(settingsPathFile)
	if err != nil {
		t.Fatalf("read settings path: %v", err)
	}
	if _, err := os.Stat(string(settingsPath)); !os.IsNotExist(err) {
		t.Errorf("provider settings file %q was not removed after the run", settingsPath)
	}
}

func readClaudeHelperEnvironment(t *testing.T, path string) map[string][]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read helper environment: %v", err)
	}
	environment := map[string][]string{}
	for _, line := range strings.Split(string(data), "\n") {
		name, value, found := strings.Cut(line, "=")
		if found {
			environment[strings.ToUpper(name)] = append(environment[strings.ToUpper(name)], value)
		}
	}
	return environment
}

func TestEveryAgentUsesTheProviderEnvironment(t *testing.T) {
	config := testClaudeConfig()
	prd, err := NewClaudePRDGenerator(config)
	if err != nil {
		t.Fatal(err)
	}
	trd, err := NewClaudeTRDGenerator(config, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	engineer, err := NewClaudeEngineer(config)
	if err != nil {
		t.Fatal(err)
	}
	reviewer, err := NewClaudeReviewer(config)
	if err != nil {
		t.Fatal(err)
	}
	planner, err := NewClaudeRepoPlanner(config, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writer, err := NewClaudeDocumentWriter(config, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, cli := range map[string]claudeCLI{
		"PRD": prd.cli, "TRD": trd.cli, "engineer": engineer.cli,
		"reviewer": reviewer.cli, "planner": planner.cli, "writer": writer.cli,
	} {
		if !reflect.DeepEqual(cli.environment, config.providerEnv) {
			t.Errorf("%s environment = %q, want the provider configuration", name, cli.environment)
		}
	}
}

func TestClaudeErrorRedactsTheToken(t *testing.T) {
	_, err := newTestClaudeGenerator(t, "leak-secret", "", 5*time.Second).Generate(context.Background(), testPRDInput())
	if err == nil || !strings.Contains(err.Error(), "[redacted]") {
		t.Fatalf("Generate() error = %v, want a redacted token", err)
	}
	if strings.Contains(err.Error(), testDeepSeekKey) {
		t.Fatalf("Generate() error leaked the token: %v", err)
	}
}

func TestTransientFailureRetriesOnce(t *testing.T) {
	for mode, wantErr := range map[string]string{"transient": "", "always-transient": "after 2 attempts"} {
		t.Run(mode, func(t *testing.T) {
			attempts := filepath.Join(t.TempDir(), "attempts")
			t.Setenv("CLAUDE_TEST_ATTEMPT_FILE", attempts)

			got, err := newTestClaudeGenerator(t, mode, "", 5*time.Second).Generate(context.Background(), testPRDInput())
			if wantErr == "" {
				if err != nil || got != "# Problem\nGenerated PRD" {
					t.Fatalf("Generate() = (%q, %v), want it to succeed on the retry", got, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), wantErr) {
				t.Fatalf("Generate() error = %v, want %q", err, wantErr)
			}
			data, err := os.ReadFile(attempts)
			if err != nil || strings.TrimSpace(string(data)) != "2" {
				t.Fatalf("attempts = %q (%v), want exactly 2", data, err)
			}
		})
	}
}

func TestAnthropicOptInUsesTheCLILogin(t *testing.T) {
	t.Setenv("CLAUDE_TEST_HELPER", "1")
	t.Setenv("CLAUDE_TEST_MODE", "success")
	t.Setenv("CLAUDE_TEST_EXPECT_MODEL", "")
	t.Setenv("CLAUDE_TEST_EXPECT_PROVIDER", ProviderAnthropic)
	t.Setenv("ANTHROPIC_API_KEY", "must-not-reach-cli")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "must-not-reach-cli")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "must-not-reach-cli")
	t.Setenv("CLAUDE_CODE_SUBAGENT_MODEL_FORCE", "1")
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "1")
	envFile := filepath.Join(t.TempDir(), "env")
	t.Setenv("CLAUDE_TEST_ENV_FILE", envFile)

	config, err := LoadClaudeConfig(func(key string) string {
		if key == "CLAUDE_PROVIDER" {
			return ProviderAnthropic
		}
		return ""
	})
	if err != nil {
		t.Fatalf("LoadClaudeConfig() error = %v", err)
	}
	config.Executable = os.Args[0]
	config.Timeout = 5 * time.Second
	generator, err := NewClaudePRDGenerator(config)
	if err != nil {
		t.Fatalf("NewClaudePRDGenerator() error = %v", err)
	}
	if _, err := generator.Generate(context.Background(), testPRDInput()); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	environment := readClaudeHelperEnvironment(t, envFile)
	for _, name := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_BASE_URL", "ANTHROPIC_MODEL", "ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL", "CLAUDE_CODE_SUBAGENT_MODEL_FORCE", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY"} {
		if got := environment[name]; len(got) != 0 {
			t.Errorf("%s leaked into the opt-in run: %q", name, got)
		}
	}
}

func TestInstallPluginWritesAgentAndSkill(t *testing.T) {
	root := t.TempDir()
	dir, err := installPlugin(root)
	if err != nil {
		t.Fatalf("installPlugin() error = %v", err)
	}
	for _, path := range []string{
		filepath.Join(".claude-plugin", "plugin.json"),
		filepath.Join("agents", "prd-maker.md"),
		filepath.Join("skills", "prd-writing", "SKILL.md"),
		filepath.Join("agents", "technical-planner.md"),
		filepath.Join("skills", "trd-writing", "SKILL.md"),
	} {
		if _, err := os.Stat(filepath.Join(dir, path)); err != nil {
			t.Errorf("installed plugin is missing %s: %v", path, err)
		}
	}

	again, err := installPlugin(root)
	if err != nil || again != dir {
		t.Fatalf("installPlugin() again = (%q, %v), want reuse of %q", again, err, dir)
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 1 {
		t.Fatalf("root has %d entries, want only the installed plugin", len(entries))
	}
}

func TestStderrSummaryKeepsFirstLineOnly(t *testing.T) {
	if got := stderrSummary("\n  first line  \nsecond line\n"); got != "first line" {
		t.Fatalf("stderrSummary() = %q, want first line", got)
	}
	long := strings.Repeat("x", maxStderrSummary+50)
	if got := stderrSummary(long); got != long[:maxStderrSummary]+"..." {
		t.Fatalf("stderrSummary() = %q, want capped at %d bytes", got, maxStderrSummary)
	}
	if got := stderrSummary("  \n"); got != "" {
		t.Fatalf("stderrSummary() = %q, want empty", got)
	}
}

func TestInstallPluginRepairsEmptiedInstall(t *testing.T) {
	root := t.TempDir()
	dir, err := installPlugin(root)
	if err != nil {
		t.Fatalf("installPlugin() error = %v", err)
	}
	// A temp cleaner deletes the files but leaves the directories.
	agentFile := filepath.Join(dir, "agents", "prd-maker.md")
	if err := os.Remove(agentFile); err != nil {
		t.Fatalf("remove agent file: %v", err)
	}
	skillFile := filepath.Join(dir, "skills", "prd-writing", "SKILL.md")
	if err := os.WriteFile(skillFile, []byte("stale"), 0o644); err != nil {
		t.Fatalf("overwrite skill file: %v", err)
	}

	again, err := installPlugin(root)
	if err != nil || again != dir {
		t.Fatalf("installPlugin() again = (%q, %v), want repair of %q", again, err, dir)
	}
	plugin, _ := fs.Sub(pluginFiles, "plugin")
	if !pluginInstalled(plugin, dir) {
		t.Fatalf("plugin at %q was not restored", dir)
	}
	entries, _ := os.ReadDir(root)
	if len(entries) != 1 {
		t.Fatalf("root has %d entries, want only the installed plugin", len(entries))
	}
}

func TestPluginManifestNameMatchesAgentNamespace(t *testing.T) {
	manifest, err := pluginFiles.ReadFile("plugin/.claude-plugin/plugin.json")
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var plugin struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(manifest, &plugin); err != nil || plugin.Name != PluginName {
		t.Fatalf("manifest name = %q (%v), want %q", plugin.Name, err, PluginName)
	}
	agentFile, _ := pluginFiles.ReadFile("plugin/agents/prd-maker.md")
	if !strings.Contains(string(agentFile), "name: prd-maker") || !strings.Contains(string(agentFile), PluginName+":prd-writing") {
		t.Fatalf("prd-maker agent must be named prd-maker and invoke %s:prd-writing", PluginName)
	}
	plannerFile, _ := pluginFiles.ReadFile("plugin/agents/technical-planner.md")
	if !strings.Contains(string(plannerFile), "name: technical-planner") || !strings.Contains(string(plannerFile), PluginName+":trd-writing") || TechnicalPlannerAgent != PluginName+":technical-planner" {
		t.Fatalf("technical-planner agent must be named technical-planner and invoke %s:trd-writing", PluginName)
	}
	for _, agent := range []struct{ file, name, skill, id string }{
		{"software-engineer.md", "software-engineer", "software-engineering", SoftwareEngineerAgent},
		{"code-reviewer.md", "code-reviewer", "code-review", CodeReviewerAgent},
	} {
		content, err := pluginFiles.ReadFile("plugin/agents/" + agent.file)
		if err != nil || !strings.Contains(string(content), "name: "+agent.name) || !strings.Contains(string(content), PluginName+":"+agent.skill) || agent.id != PluginName+":"+agent.name {
			t.Fatalf("%s agent must be named %s and invoke %s:%s", agent.file, agent.name, PluginName, agent.skill)
		}
		if _, err := pluginFiles.ReadFile("plugin/skills/" + agent.skill + "/SKILL.md"); err != nil {
			t.Fatalf("skill %s is missing: %v", agent.skill, err)
		}
	}
}

func TestLoadClaudeConfig(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    ClaudeConfig
		wantErr string
	}{
		{
			name: "defaults",
			env:  map[string]string{"DEEPSEEK_API_KEY": testDeepSeekKey},
			want: ClaudeConfig{Executable: DefaultClaudeExecutable, Model: DefaultDeepSeekModel, Timeout: DefaultClaudeTimeout, PlanningTimeout: DefaultClaudePlanningTimeout, ImplementationTimeout: DefaultClaudeImplementationTimeout, ReviewTimeout: DefaultClaudeReviewTimeout, AllowedCommands: DefaultAllowedCommands, Provider: ProviderDeepSeek, providerEnv: testDeepSeekEnvironment()},
		},
		{
			name: "configured",
			env:  map[string]string{"DEEPSEEK_API_KEY": " " + testDeepSeekKey + " ", "CLAUDE_COMMAND": `C:\Users\Me\.local\bin\claude.exe`, "CLAUDE_MODEL": " opus ", "CLAUDE_TIMEOUT_SECONDS": "60", "CLAUDE_PLANNING_TIMEOUT_SECONDS": "1200", "CLAUDE_IMPLEMENTATION_TIMEOUT_SECONDS": "3600", "CLAUDE_REVIEW_TIMEOUT_SECONDS": "600", "CLAUDE_ALLOWED_COMMANDS": " go test , go vet ,"},
			want: ClaudeConfig{Executable: `C:\Users\Me\.local\bin\claude.exe`, Model: "opus", Timeout: 60 * time.Second, PlanningTimeout: 1200 * time.Second, ImplementationTimeout: 3600 * time.Second, ReviewTimeout: 600 * time.Second, AllowedCommands: []string{"go test", "go vet"}, Provider: ProviderDeepSeek, providerEnv: testDeepSeekEnvironment()},
		},
		{
			name: "anthropic is opt-in",
			env:  map[string]string{"CLAUDE_PROVIDER": " Anthropic "},
			want: ClaudeConfig{Executable: DefaultClaudeExecutable, Timeout: DefaultClaudeTimeout, PlanningTimeout: DefaultClaudePlanningTimeout, ImplementationTimeout: DefaultClaudeImplementationTimeout, ReviewTimeout: DefaultClaudeReviewTimeout, AllowedCommands: DefaultAllowedCommands, Provider: ProviderAnthropic},
		},
		{name: "deepseek needs a key", wantErr: "DEEPSEEK_API_KEY"},
		{name: "rejects an unknown provider", env: map[string]string{"CLAUDE_PROVIDER": "openai", "DEEPSEEK_API_KEY": testDeepSeekKey}, wantErr: "CLAUDE_PROVIDER"},
		{name: "rejects a key with spaces", env: map[string]string{"DEEPSEEK_API_KEY": "sk-test key"}, wantErr: "DEEPSEEK_API_KEY"},
		{name: "rejects arguments", env: map[string]string{"CLAUDE_COMMAND": "claude --dangerously-skip-permissions"}, wantErr: "CLAUDE_COMMAND"},
		{name: "rejects shell syntax", env: map[string]string{"CLAUDE_COMMAND": "claude & calc"}, wantErr: "CLAUDE_COMMAND"},
		{name: "rejects bad timeout", env: map[string]string{"CLAUDE_TIMEOUT_SECONDS": "-1"}, wantErr: "CLAUDE_TIMEOUT_SECONDS"},
		{name: "rejects bad planning timeout", env: map[string]string{"CLAUDE_PLANNING_TIMEOUT_SECONDS": "soon"}, wantErr: "CLAUDE_PLANNING_TIMEOUT_SECONDS"},
		{name: "rejects bad review timeout", env: map[string]string{"CLAUDE_REVIEW_TIMEOUT_SECONDS": "0"}, wantErr: "CLAUDE_REVIEW_TIMEOUT_SECONDS"},
		{name: "rejects permission rule syntax", env: map[string]string{"CLAUDE_ALLOWED_COMMANDS": "Bash(rm:*)"}, wantErr: "CLAUDE_ALLOWED_COMMANDS"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := LoadClaudeConfig(func(key string) string { return tt.env[key] })
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("LoadClaudeConfig() error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("LoadClaudeConfig() = (%+v, %v), want %+v", got, err, tt.want)
			}
		})
	}
}

func TestPRDPromptAsksForRevisionWhenThereIsFeedback(t *testing.T) {
	input := testPRDInput()
	if got := prdPrompt(input); !strings.HasPrefix(got, "Write the PRD") || strings.Contains(got, "feedback") {
		t.Fatalf("prdPrompt() without feedback = %q, want initial PRD prompt", got)
	}

	input.CurrentPRD = "<h2>Problem</h2>"
	input.Feedback = []workflow.Feedback{
		{Comment: jira.Comment{Author: "Ana", Created: "2026-09-01"}, Text: "CSV only"},
		{Comment: jira.Comment{Author: "Budi", Created: "2026-09-02"}, Text: "Admins only"},
	}
	got := prdPrompt(input)
	for _, want := range []string{"Revise the PRD", "Issue key: DEMO-42", "The original issue description.", "<h2>Problem</h2>", "[Comment 1 by Ana at 2026-09-01]\nCSV only", "[Comment 2 by Budi at 2026-09-02]\nAdmins only"} {
		if !strings.Contains(got, want) {
			t.Fatalf("prdPrompt() = %q, want it to contain %q", got, want)
		}
	}

	input.CurrentPRD = ""
	if got := prdPrompt(input); !strings.Contains(got, "(none published") {
		t.Fatalf("prdPrompt() without current PRD = %q, want note that none is published", got)
	}
}
