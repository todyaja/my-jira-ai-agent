package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tody-aja/jira-ai-agent/internal/workflow"
)

const (
	DefaultOpenAIModel   = "gpt-5.6-luna"
	DefaultOpenAIBaseURL = "https://api.openai.com/v1"
)

type OpenAIConfig struct {
	APIKey  string
	Model   string
	BaseURL string
	Timeout time.Duration
}

type OpenAIPRDGenerator struct {
	apiKey  string
	model   string
	baseURL string
	client  *http.Client
}

func NewOpenAIPRDGenerator(config OpenAIConfig) (*OpenAIPRDGenerator, error) {
	if strings.TrimSpace(config.APIKey) == "" {
		return nil, fmt.Errorf("OpenAI API key is required")
	}
	model := config.Model
	if strings.TrimSpace(model) == "" {
		model = DefaultOpenAIModel
	}
	baseURL := strings.TrimRight(config.BaseURL, "/")
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultOpenAIBaseURL
	}

	return &OpenAIPRDGenerator{
		apiKey:  config.APIKey,
		model:   model,
		baseURL: baseURL,
		client:  &http.Client{Timeout: config.Timeout},
	}, nil
}

type chatCompletionRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
}

const prdSystemPrompt = `Create a structured product requirements document from the issue provided by the user.

Use exactly these sections: Problem, Goals, Non-Goals, Functional Requirements, Non-Functional Requirements, Acceptance Criteria, Risks/Assumptions, and Open Questions.
Preserve uncertainty explicitly. never invent missing business requirements; put unresolved information in Risks/Assumptions or Open Questions instead.`

func (g *OpenAIPRDGenerator) Generate(ctx context.Context, input workflow.PRDInput) (string, error) {
	requestBody := chatCompletionRequest{
		Model: g.model,
		Messages: []chatMessage{
			{Role: "system", Content: prdSystemPrompt},
			{Role: "user", Content: fmt.Sprintf("Issue key: %s\nSummary: %s\nDescription:\n%s", input.IssueKey, input.Summary, input.Description)},
		},
	}
	body, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("encode OpenAI request: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, g.baseURL+"/chat/completions", strings.NewReader(string(body)))
	if err != nil {
		return "", fmt.Errorf("create OpenAI request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+g.apiKey)
	request.Header.Set("Content-Type", "application/json")

	response, err := g.client.Do(request)
	if err != nil {
		return "", fmt.Errorf("call OpenAI: %w", err)
	}
	defer response.Body.Close()

	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		return "", fmt.Errorf("read OpenAI response: %w", err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("OpenAI returned HTTP status %d", response.StatusCode)
	}

	var completion chatCompletionResponse
	if err := json.Unmarshal(responseBody, &completion); err != nil {
		return "", fmt.Errorf("decode OpenAI response: %w", err)
	}
	if len(completion.Choices) == 0 || strings.TrimSpace(completion.Choices[0].Message.Content) == "" {
		return "", fmt.Errorf("OpenAI response contains no generated content")
	}
	return completion.Choices[0].Message.Content, nil
}

var _ workflow.PRDGenerator = (*OpenAIPRDGenerator)(nil)
