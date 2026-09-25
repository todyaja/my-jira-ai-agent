package agent

import (
	"strings"
	"testing"
	"time"
)

func TestLoadOpenCodeConfigUsesDefaults(t *testing.T) {
	config, err := LoadOpenCodeConfig(func(string) string { return "" })
	if err != nil {
		t.Fatalf("LoadOpenCodeConfig() error = %v", err)
	}

	if config.Executable != DefaultOpenCodeExecutable {
		t.Errorf("executable = %q, want %q", config.Executable, DefaultOpenCodeExecutable)
	}
	if config.Model != DefaultOpenCodeModel {
		t.Errorf("model = %q, want %q", config.Model, DefaultOpenCodeModel)
	}
	if config.Timeout != DefaultOpenCodeTimeout {
		t.Errorf("timeout = %s, want %s", config.Timeout, DefaultOpenCodeTimeout)
	}
}

func TestLoadOpenCodeConfigPreservesConfiguredValues(t *testing.T) {
	values := map[string]string{
		"AGENT_COMMAND":            "custom-opencode",
		"OPENCODE_MODEL":           "custom/model",
		"OPENCODE_TIMEOUT_SECONDS": "17",
	}

	config, err := LoadOpenCodeConfig(func(key string) string { return values[key] })
	if err != nil {
		t.Fatalf("LoadOpenCodeConfig() error = %v", err)
	}

	if config.Executable != "custom-opencode" || config.Model != "custom/model" {
		t.Errorf("executable = %q, model = %q, want configured values", config.Executable, config.Model)
	}
	if config.Timeout != 17*time.Second {
		t.Errorf("timeout = %s, want 17s", config.Timeout)
	}
}

func TestLoadOpenCodeConfigAcceptsExecutablePathWithSpaces(t *testing.T) {
	config, err := LoadOpenCodeConfig(func(key string) string {
		if key == "AGENT_COMMAND" {
			return `C:\Program Files\OpenCode\opencode.exe`
		}
		return ""
	})
	if err != nil {
		t.Fatalf("LoadOpenCodeConfig() error = %v", err)
	}
	if config.Executable != `C:\Program Files\OpenCode\opencode.exe` {
		t.Errorf("executable = %q, want Windows path with spaces", config.Executable)
	}
}

func TestLoadOpenCodeConfigRejectsInvalidTimeout(t *testing.T) {
	_, err := LoadOpenCodeConfig(func(key string) string {
		if key == "OPENCODE_TIMEOUT_SECONDS" {
			return "not-a-number"
		}
		return ""
	})
	if err == nil {
		t.Fatal("LoadOpenCodeConfig() error = nil, want invalid timeout error")
	}
	if !strings.Contains(err.Error(), "OPENCODE_TIMEOUT_SECONDS") {
		t.Errorf("error = %q, want timeout variable name", err)
	}
}

func TestLoadOpenCodeConfigRejectsShellFragments(t *testing.T) {
	_, err := LoadOpenCodeConfig(func(key string) string {
		if key == "AGENT_COMMAND" {
			return "opencode;echo unsafe"
		}
		return ""
	})
	if err == nil {
		t.Fatal("LoadOpenCodeConfig() error = nil, want command validation error")
	}
	if !strings.Contains(err.Error(), "AGENT_COMMAND") {
		t.Errorf("error = %q, want command variable name", err)
	}
}

func TestLoadOpenCodeConfigRejectsShellMetacharacters(t *testing.T) {
	for _, command := range []string{
		"opencode && echo unsafe",
		"opencode | echo unsafe",
		"opencode > output.txt",
		"$(echo unsafe)",
		"`echo unsafe`",
	} {
		t.Run(command, func(t *testing.T) {
			_, err := LoadOpenCodeConfig(func(key string) string {
				if key == "AGENT_COMMAND" {
					return command
				}
				return ""
			})
			if err == nil {
				t.Fatalf("LoadOpenCodeConfig(%q) error = nil, want shell metacharacter error", command)
			}
		})
	}
}

func TestLoadOpenCodeConfigAcceptsMaximumSafeTimeout(t *testing.T) {
	config, err := LoadOpenCodeConfig(func(key string) string {
		if key == "OPENCODE_TIMEOUT_SECONDS" {
			return "9223372036"
		}
		return ""
	})
	if err != nil {
		t.Fatalf("LoadOpenCodeConfig() error = %v, want maximum safe timeout accepted", err)
	}
	if config.Timeout != time.Duration(maxOpenCodeTimeoutSeconds)*time.Second {
		t.Errorf("timeout = %s, want maximum safe timeout", config.Timeout)
	}
}

func TestLoadOpenCodeConfigRejectsTimeoutOverflow(t *testing.T) {
	_, err := LoadOpenCodeConfig(func(key string) string {
		if key == "OPENCODE_TIMEOUT_SECONDS" {
			return "9223372037"
		}
		return ""
	})
	if err == nil {
		t.Fatal("LoadOpenCodeConfig() error = nil, want timeout overflow error")
	}
	if !strings.Contains(err.Error(), "OPENCODE_TIMEOUT_SECONDS") {
		t.Errorf("error = %q, want timeout variable name", err)
	}
}
