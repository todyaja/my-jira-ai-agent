package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/tody-aja/jira-ai-agent/internal/jira"
	"github.com/tody-aja/jira-ai-agent/internal/workflow"
)

func TestMain(m *testing.M) {
	if os.Getenv("CLAUDE_TEST_HELPER") != "" {
		runClaudeHelper()
		os.Exit(0)
	}
	os.Exit(m.Run())
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
	args := os.Args[1:]
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
	if len(args) > 4 {
		pluginDir, args[4] = args[4], "<plugin>"
	}
	if _, err := os.Stat(filepath.Join(pluginDir, "agents", agentName+".md")); err != nil {
		fmt.Fprintf(os.Stderr, "plugin directory has no %s agent: %q\n", agentName, pluginDir)
		os.Exit(2)
	}
	if !slices.Equal(args, want) {
		fmt.Fprintf(os.Stderr, "unexpected command arguments: %q\n", args)
		os.Exit(2)
	}
	if os.Getenv("ANTHROPIC_API_KEY") != "" || os.Getenv("ANTHROPIC_AUTH_TOKEN") != "" {
		fmt.Fprintln(os.Stderr, "API credentials leaked to the CLI")
		os.Exit(2)
	}
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
		fmt.Fprintln(os.Stderr, "private helper diagnostic")
		os.Exit(17)
	case "invalid-json":
		fmt.Print("not json")
	case "timeout":
		time.Sleep(time.Second)
	default:
		fmt.Print(`{"type":"result","subtype":"success","is_error":false,"result":"  # Problem\nGenerated PRD  "}`)
	}
}

func newTestClaudeGenerator(t *testing.T, mode, model string, timeout time.Duration) *ClaudePRDGenerator {
	t.Helper()
	t.Setenv("CLAUDE_TEST_HELPER", "1")
	t.Setenv("CLAUDE_TEST_MODE", mode)
	t.Setenv("CLAUDE_TEST_EXPECT_MODEL", model)
	t.Setenv("ANTHROPIC_API_KEY", "must-not-reach-cli")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "must-not-reach-cli")

	generator, err := NewClaudePRDGenerator(ClaudeConfig{Executable: os.Args[0], Model: model, Timeout: timeout})
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
		{mode: "failure", wantErr: "Claude command failed"},
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
		{name: "defaults", want: ClaudeConfig{Executable: DefaultClaudeExecutable, Timeout: DefaultClaudeTimeout, PlanningTimeout: DefaultClaudePlanningTimeout, ImplementationTimeout: DefaultClaudeImplementationTimeout, ReviewTimeout: DefaultClaudeReviewTimeout, AllowedCommands: DefaultAllowedCommands}},
		{
			name: "configured",
			env:  map[string]string{"CLAUDE_COMMAND": `C:\Users\Me\.local\bin\claude.exe`, "CLAUDE_MODEL": " opus ", "CLAUDE_TIMEOUT_SECONDS": "60", "CLAUDE_PLANNING_TIMEOUT_SECONDS": "1200", "CLAUDE_IMPLEMENTATION_TIMEOUT_SECONDS": "3600", "CLAUDE_REVIEW_TIMEOUT_SECONDS": "600", "CLAUDE_ALLOWED_COMMANDS": " go test , go vet ,"},
			want: ClaudeConfig{Executable: `C:\Users\Me\.local\bin\claude.exe`, Model: "opus", Timeout: 60 * time.Second, PlanningTimeout: 1200 * time.Second, ImplementationTimeout: 3600 * time.Second, ReviewTimeout: 600 * time.Second, AllowedCommands: []string{"go test", "go vet"}},
		},
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
