package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tody-aja/jira-ai-agent/internal/workflow"
)

func TestOpenAIPRDGeneratorGenerate(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "sends constrained request and extracts first choice",
			body: `{"choices":[{"message":{"content":"# Problem\nGenerated PRD"}},{"message":{"content":"ignore"}}]}`,
			want: "# Problem\nGenerated PRD",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := workflow.PRDInput{
				IssueKey:    "DEMO-42",
				Summary:     "Build a workflow action",
				Description: "The original issue description.",
			}
			var requestBody map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Errorf("method = %q, want POST", r.Method)
				}
				if r.URL.Path != "/v1/chat/completions" {
					t.Errorf("path = %q, want /v1/chat/completions", r.URL.Path)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
					t.Errorf("authorization = %q, want Bearer test-key", got)
				}
				if got := r.Header.Get("Content-Type"); got != "application/json" {
					t.Errorf("content type = %q, want application/json", got)
				}
				if err := json.NewDecoder(r.Body).Decode(&requestBody); err != nil {
					t.Errorf("decode request: %v", err)
				}
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, tt.body)
			}))
			defer server.Close()

			generator, err := NewOpenAIPRDGenerator(OpenAIConfig{
				APIKey:  "test-key",
				Model:   "configured-model",
				BaseURL: server.URL + "/v1/",
			})
			if err != nil {
				t.Fatalf("NewOpenAIPRDGenerator() error = %v", err)
			}
			got, err := generator.Generate(context.Background(), input)
			if err != nil {
				t.Fatalf("Generate() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("Generate() = %q, want %q", got, tt.want)
			}
			if got := requestBody["model"]; got != "configured-model" {
				t.Errorf("model = %v, want configured-model", got)
			}
			messages, ok := requestBody["messages"].([]any)
			if !ok || len(messages) != 2 {
				t.Fatalf("messages = %#v, want two messages", requestBody["messages"])
			}
			encodedMessages, _ := json.Marshal(messages)
			messageText := string(encodedMessages)
			for _, value := range []string{input.IssueKey, input.Summary, input.Description, "Problem", "Goals", "Non-Goals", "Functional Requirements", "Non-Functional Requirements", "Acceptance Criteria", "Risks/Assumptions", "Open Questions", "never invent"} {
				if !strings.Contains(messageText, value) {
					t.Errorf("request messages do not contain %q: %s", value, messageText)
				}
			}
		})
	}
}

func TestNewOpenAIPRDGeneratorRejectsMissingAPIKey(t *testing.T) {
	_, err := NewOpenAIPRDGenerator(OpenAIConfig{})
	if err == nil || !strings.Contains(err.Error(), "API key") {
		t.Fatalf("NewOpenAIPRDGenerator() error = %v, want missing API key error", err)
	}
}

func TestNewOpenAIPRDGeneratorUsesDefaults(t *testing.T) {
	generator, err := NewOpenAIPRDGenerator(OpenAIConfig{APIKey: "test-key"})
	if err != nil {
		t.Fatalf("NewOpenAIPRDGenerator() error = %v", err)
	}
	if generator.model != DefaultOpenAIModel {
		t.Errorf("model = %q, want %q", generator.model, DefaultOpenAIModel)
	}
	if generator.baseURL != DefaultOpenAIBaseURL {
		t.Errorf("base URL = %q, want %q", generator.baseURL, DefaultOpenAIBaseURL)
	}
}

func TestOpenAIPRDGeneratorGenerateRejectsResponses(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
	}{
		{name: "http error", statusCode: http.StatusBadGateway, body: `{"error":{"message":"upstream failure"}}`},
		{name: "malformed json", statusCode: http.StatusOK, body: `{"choices":`},
		{name: "missing choices", statusCode: http.StatusOK, body: `{}`},
		{name: "missing content", statusCode: http.StatusOK, body: `{"choices":[{"message":{}}]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.statusCode)
				io.WriteString(w, tt.body)
			}))
			defer server.Close()
			generator, err := NewOpenAIPRDGenerator(OpenAIConfig{APIKey: "test-key", BaseURL: server.URL})
			if err != nil {
				t.Fatalf("NewOpenAIPRDGenerator() error = %v", err)
			}
			_, err = generator.Generate(context.Background(), workflow.PRDInput{IssueKey: "DEMO-1"})
			if err == nil {
				t.Fatal("Generate() error = nil, want error")
			}
		})
	}
}

func TestOpenAIPRDGeneratorGenerateHonorsTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
	}))
	defer server.Close()

	generator, err := NewOpenAIPRDGenerator(OpenAIConfig{
		APIKey:  "test-key",
		BaseURL: server.URL,
		Timeout: 10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("NewOpenAIPRDGenerator() error = %v", err)
	}
	_, err = generator.Generate(context.Background(), workflow.PRDInput{IssueKey: "DEMO-1"})
	if err == nil {
		t.Fatal("Generate() error = nil, want timeout error")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("Generate() error = %v, want deadline exceeded", err)
	}
}

func TestOpenAIPRDGeneratorDoesNotExposeSecretsOrGeneratedContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":"test-key secret generated PRD"}`)
	}))
	defer server.Close()

	generator, err := NewOpenAIPRDGenerator(OpenAIConfig{APIKey: "test-key", BaseURL: server.URL})
	if err != nil {
		t.Fatalf("NewOpenAIPRDGenerator() error = %v", err)
	}
	_, err = generator.Generate(context.Background(), workflow.PRDInput{IssueKey: "DEMO-1", Description: "private description"})
	if err == nil {
		t.Fatal("Generate() error = nil, want error")
	}
	if strings.Contains(err.Error(), "test-key") || strings.Contains(err.Error(), "secret generated PRD") {
		t.Fatal("error exposed a secret or generated content")
	}
}
