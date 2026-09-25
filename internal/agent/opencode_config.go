package agent

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const maxOpenCodeTimeoutSeconds = int64((1<<63 - 1) / int64(time.Second))
const openCodeShellMetacharacters = "&|<>^;()$'\"`"

func LoadOpenCodeConfig(getenv func(string) string) (OpenCodeConfig, error) {
	executable := strings.TrimSpace(getenv("AGENT_COMMAND"))
	if executable == "" {
		executable = DefaultOpenCodeExecutable
	} else if strings.ContainsAny(executable, openCodeShellMetacharacters) || strings.IndexFunc(executable, unicode.IsControl) >= 0 {
		return OpenCodeConfig{}, fmt.Errorf("AGENT_COMMAND must be exactly one executable path")
	}

	model := getenv("OPENCODE_MODEL")
	if strings.TrimSpace(model) == "" {
		model = DefaultOpenCodeModel
	}

	timeout := DefaultOpenCodeTimeout
	timeoutValue := getenv("OPENCODE_TIMEOUT_SECONDS")
	if strings.TrimSpace(timeoutValue) != "" {
		seconds, err := strconv.ParseInt(timeoutValue, 10, 64)
		if err != nil || seconds <= 0 || seconds > maxOpenCodeTimeoutSeconds {
			return OpenCodeConfig{}, fmt.Errorf("OPENCODE_TIMEOUT_SECONDS must be a positive integer")
		}
		timeout = time.Duration(seconds) * time.Second
	}

	return OpenCodeConfig{
		Executable: executable,
		Model:      model,
		Timeout:    timeout,
	}, nil
}
