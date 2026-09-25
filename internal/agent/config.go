package agent

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultOpenAITimeout    = 60 * time.Second
	maxOpenAITimeoutSeconds = int64((1<<63 - 1) / int64(time.Second))
)

func LoadOpenAIConfig(getenv func(string) string) (OpenAIConfig, error) {
	apiKey := getenv("OPENAI_API_KEY")
	if strings.TrimSpace(apiKey) == "" {
		return OpenAIConfig{}, fmt.Errorf("OPENAI_API_KEY is required")
	}

	timeout := DefaultOpenAITimeout
	timeoutValue := getenv("OPENAI_TIMEOUT_SECONDS")
	if strings.TrimSpace(timeoutValue) != "" {
		seconds, err := strconv.Atoi(timeoutValue)
		if err != nil || seconds <= 0 || int64(seconds) > maxOpenAITimeoutSeconds {
			return OpenAIConfig{}, fmt.Errorf("OPENAI_TIMEOUT_SECONDS must be a positive integer")
		}
		timeout = time.Duration(seconds) * time.Second
	}

	model := getenv("OPENAI_MODEL")
	if strings.TrimSpace(model) == "" {
		model = DefaultOpenAIModel
	}
	baseURL := getenv("OPENAI_BASE_URL")
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultOpenAIBaseURL
	}

	return OpenAIConfig{
		APIKey:  apiKey,
		Model:   model,
		BaseURL: baseURL,
		Timeout: timeout,
	}, nil
}
