package agent

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const maxClaudeTimeoutSeconds = int64((1<<63 - 1) / int64(time.Second))
const shellMetacharacters = "&|<>^;()$'\"`"

func LoadClaudeConfig(getenv func(string) string) (ClaudeConfig, error) {
	executable := strings.TrimSpace(getenv("CLAUDE_COMMAND"))
	if executable == "" {
		executable = DefaultClaudeExecutable
	} else if strings.ContainsAny(executable, shellMetacharacters) || strings.IndexFunc(executable, unicode.IsControl) >= 0 || hasCommandArguments(executable) {
		return ClaudeConfig{}, fmt.Errorf("CLAUDE_COMMAND must be exactly one executable path")
	}

	timeout, err := loadTimeout(getenv, "CLAUDE_TIMEOUT_SECONDS", DefaultClaudeTimeout)
	if err != nil {
		return ClaudeConfig{}, err
	}
	planningTimeout, err := loadTimeout(getenv, "CLAUDE_PLANNING_TIMEOUT_SECONDS", DefaultClaudePlanningTimeout)
	if err != nil {
		return ClaudeConfig{}, err
	}
	implementationTimeout, err := loadTimeout(getenv, "CLAUDE_IMPLEMENTATION_TIMEOUT_SECONDS", DefaultClaudeImplementationTimeout)
	if err != nil {
		return ClaudeConfig{}, err
	}
	reviewTimeout, err := loadTimeout(getenv, "CLAUDE_REVIEW_TIMEOUT_SECONDS", DefaultClaudeReviewTimeout)
	if err != nil {
		return ClaudeConfig{}, err
	}
	allowedCommands, err := loadAllowedCommands(getenv)
	if err != nil {
		return ClaudeConfig{}, err
	}

	stepModels, err := loadStepModels(getenv)
	if err != nil {
		return ClaudeConfig{}, err
	}

	return ClaudeConfig{
		Executable:            executable,
		Model:                 strings.TrimSpace(getenv("CLAUDE_MODEL")),
		Timeout:               timeout,
		PlanningTimeout:       planningTimeout,
		ImplementationTimeout: implementationTimeout,
		ReviewTimeout:         reviewTimeout,
		AllowedCommands:       allowedCommands,
		StepModels:            stepModels,
	}, nil
}

// Steps whose model CLAUDE_<step>_MODEL can set apart from CLAUDE_MODEL.
const (
	StepPRD      = "PRD"
	StepTRD      = "TRD"
	StepEngineer = "ENGINEER"
	StepReviewer = "REVIEWER"
	StepRFC      = "RFC"
	StepFSD      = "FSD"
	StepRunbook  = "RUNBOOK"
)

var modelSteps = []string{StepPRD, StepTRD, StepEngineer, StepReviewer, StepRFC, StepFSD, StepRunbook}

// ModelFor returns the model a step runs on: its own setting, else Model,
// else empty for the CLI's default.
func (c ClaudeConfig) ModelFor(step string) string {
	if model := c.StepModels[step]; model != "" {
		return model
	}
	return strings.TrimSpace(c.Model)
}

// loadStepModels reads CLAUDE_<step>_MODEL for every step, so cheaper
// models can run the steps that need less judgment. It returns nil when none
// is set.
func loadStepModels(getenv func(string) string) (map[string]string, error) {
	var models map[string]string
	for _, step := range modelSteps {
		key := "CLAUDE_" + step + "_MODEL"
		value := strings.TrimSpace(getenv(key))
		if value == "" {
			continue
		}
		if strings.HasPrefix(value, "-") || strings.ContainsAny(value, shellMetacharacters+" 	") {
			return nil, fmt.Errorf("%s must be one model alias or name, e.g. sonnet", key)
		}
		if models == nil {
			models = map[string]string{}
		}
		models[step] = value
	}
	return models, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// loadAllowedCommands reads CLAUDE_ALLOWED_COMMANDS, a comma-separated list
// of extra command prefixes, defaulting to DefaultAllowedCommands.
func loadAllowedCommands(getenv func(string) string) ([]string, error) {
	value := strings.TrimSpace(getenv("CLAUDE_ALLOWED_COMMANDS"))
	if value == "" {
		return append([]string(nil), DefaultAllowedCommands...), nil
	}
	commands := []string{}
	for _, command := range strings.Split(value, ",") {
		command = strings.TrimSpace(command)
		if command == "" {
			continue
		}
		if strings.ContainsAny(command, "()*") || strings.IndexFunc(command, unicode.IsControl) >= 0 {
			return nil, fmt.Errorf(`CLAUDE_ALLOWED_COMMANDS entries must be plain command prefixes like "npm test"`)
		}
		commands = append(commands, command)
	}
	return commands, nil
}

func loadTimeout(getenv func(string) string, key string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(getenv(key))
	if value == "" {
		return fallback, nil
	}
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil || seconds <= 0 || seconds > maxClaudeTimeoutSeconds {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return time.Duration(seconds) * time.Second, nil
}

// Spaces are valid in executable paths; a whitespace-delimited component
// beginning with '-' is an appended CLI argument rather than part of a path.
func hasCommandArguments(executable string) bool {
	for _, part := range strings.Fields(executable) {
		if strings.HasPrefix(part, "-") {
			return true
		}
	}
	return false
}
