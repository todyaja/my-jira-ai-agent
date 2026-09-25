package agent

import (
	"strings"
	"testing"
	"time"
)

func TestLoadOpenAIConfigUsesDefaults(t *testing.T) {
	getenv := func(key string) string {
		if key == "OPENAI_API_KEY" {
			return "test-key"
		}
		return ""
	}

	config, err := LoadOpenAIConfig(getenv)
	if err != nil {
		t.Fatalf("LoadOpenAIConfig() error = %v", err)
	}

	if config.Model != DefaultOpenAIModel {
		t.Errorf("model = %q, want %q", config.Model, DefaultOpenAIModel)
	}
	if config.BaseURL != DefaultOpenAIBaseURL {
		t.Errorf("base URL = %q, want %q", config.BaseURL, DefaultOpenAIBaseURL)
	}
	if config.Timeout != DefaultOpenAITimeout {
		t.Errorf("timeout = %s, want %s", config.Timeout, DefaultOpenAITimeout)
	}
}

func TestLoadOpenAIConfigParsesTimeoutSeconds(t *testing.T) {
	values := map[string]string{
		"OPENAI_API_KEY":         "test-key",
		"OPENAI_MODEL":           "custom-model",
		"OPENAI_BASE_URL":        "https://example.test/v1",
		"OPENAI_TIMEOUT_SECONDS": "17",
	}
	getenv := func(key string) string { return values[key] }

	config, err := LoadOpenAIConfig(getenv)
	if err != nil {
		t.Fatalf("LoadOpenAIConfig() error = %v", err)
	}

	if config.APIKey != "test-key" {
		t.Error("API key was not loaded")
	}
	if config.Model != "custom-model" || config.BaseURL != "https://example.test/v1" {
		t.Errorf("model = %q, base URL = %q, want configured values", config.Model, config.BaseURL)
	}
	if config.Timeout != 17*time.Second {
		t.Errorf("timeout = %s, want 17s", config.Timeout)
	}
}

func TestLoadOpenAIConfigRejectsInvalidTimeout(t *testing.T) {
	getenv := func(key string) string {
		if key == "OPENAI_API_KEY" {
			return "test-key"
		}
		if key == "OPENAI_TIMEOUT_SECONDS" {
			return "not-a-number"
		}
		return ""
	}

	_, err := LoadOpenAIConfig(getenv)
	if err == nil {
		t.Fatal("LoadOpenAIConfig() error = nil, want invalid timeout error")
	}
	if !strings.Contains(err.Error(), "OPENAI_TIMEOUT_SECONDS") {
		t.Errorf("error = %q, want timeout variable name", err)
	}
}

func TestLoadOpenAIConfigRejectsOverflowingTimeout(t *testing.T) {
	getenv := func(key string) string {
		if key == "OPENAI_API_KEY" {
			return "test-key"
		}
		if key == "OPENAI_TIMEOUT_SECONDS" {
			return "9223372037"
		}
		return ""
	}

	_, err := LoadOpenAIConfig(getenv)
	if err == nil {
		t.Fatal("LoadOpenAIConfig() error = nil, want timeout overflow error")
	}
	if !strings.Contains(err.Error(), "OPENAI_TIMEOUT_SECONDS must be a positive integer") {
		t.Errorf("error = %q, want safe timeout configuration error", err)
	}
}

func TestLoadOpenAIConfigRejectsMissingAPIKeyWithoutLeakingValue(t *testing.T) {
	secret := "test-secret-key-value"
	getenv := func(key string) string {
		if key == "OPENAI_API_KEY" {
			return ""
		}
		return secret
	}

	_, err := LoadOpenAIConfig(getenv)
	if err == nil {
		t.Fatal("LoadOpenAIConfig() error = nil, want missing API key error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error contains secret value: %q", err)
	}
	if !strings.Contains(err.Error(), "OPENAI_API_KEY") {
		t.Errorf("error = %q, want API key variable name", err)
	}
}
