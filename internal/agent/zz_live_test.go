package agent

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tody-aja/jira-ai-agent/internal/workflow"
)

// TestLiveDeepSeek makes one small, read-only request through the same
// process configuration the workflow uses. It is opt-in so `go test ./...`
// never spends tokens:
//
//	CLAUDE_LIVE_TEST=1 DEEPSEEK_API_KEY=... go test ./internal/agent -run TestLiveDeepSeek -v
func TestLiveDeepSeek(t *testing.T) {
	if os.Getenv("CLAUDE_LIVE_TEST") == "" {
		t.Skip("set CLAUDE_LIVE_TEST=1 to make a real DeepSeek request")
	}
	if provider := strings.TrimSpace(os.Getenv("CLAUDE_PROVIDER")); provider != "" && !strings.EqualFold(provider, ProviderDeepSeek) {
		t.Fatalf("CLAUDE_PROVIDER=%q; this test only verifies DeepSeek", provider)
	}
	config, err := LoadClaudeConfig(os.Getenv)
	if err != nil {
		t.Fatalf("LoadClaudeConfig() error = %v", err)
	}
	if config.ProviderName() != ProviderDeepSeek {
		t.Fatalf("provider = %q, want %q", config.ProviderName(), ProviderDeepSeek)
	}
	config.Timeout = 2 * time.Minute
	generator, err := NewClaudePRDGenerator(config)
	if err != nil {
		t.Fatalf("NewClaudePRDGenerator() error = %v", err)
	}

	answer, err := generator.Generate(context.Background(), workflow.PRDInput{
		IssueKey:    "SMOKE-1",
		Summary:     "Provider smoke test",
		Description: "This is a connectivity check: reply with one short sentence acknowledging it.",
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if strings.TrimSpace(answer) == "" {
		t.Fatal("Generate() returned an empty answer")
	}
	// The answer is not logged: only its size proves a response came back.
	t.Logf("DeepSeek answered with %d bytes", len(answer))
}
