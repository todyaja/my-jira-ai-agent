package agent

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// PluginName is the Claude Code plugin holding one agent per AI workflow
// step, each with the skill it follows.
const PluginName = "jira-ai-agent"

// PRDMakerAgent is the agent for the PRD Requested step.
const PRDMakerAgent = PluginName + ":prd-maker"

// TechnicalPlannerAgent is the agent for the Planning step.
const TechnicalPlannerAgent = PluginName + ":technical-planner"

// SoftwareEngineerAgent is the agent for the Implementing step.
const SoftwareEngineerAgent = PluginName + ":software-engineer"

// CodeReviewerAgent is the agent for the AI Review step.
const CodeReviewerAgent = PluginName + ":code-reviewer"

// The all: prefix keeps the .claude-plugin manifest directory.
//
//go:embed all:plugin
var pluginFiles embed.FS

// installPlugin writes the embedded plugin to a directory named after its
// content, so the CLI always loads the agents this binary was built with.
func installPlugin(root string) (string, error) {
	plugin, err := fs.Sub(pluginFiles, "plugin")
	if err != nil {
		return "", fmt.Errorf("install Claude plugin: %w", err)
	}

	hash := sha256.New()
	err = fs.WalkDir(plugin, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		content, err := fs.ReadFile(plugin, path)
		if err != nil {
			return err
		}
		fmt.Fprintf(hash, "%s\x00%d\x00", path, len(content))
		hash.Write(content)
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("install Claude plugin: %w", err)
	}
	dir := filepath.Join(root, PluginName+"-plugin-"+hex.EncodeToString(hash.Sum(nil))[:12])

	if _, err := os.Stat(dir); err == nil {
		return dir, nil
	}
	// Copy beside the target and rename, so an interrupted copy is never
	// mistaken for an installed plugin.
	staging, err := os.MkdirTemp(root, PluginName+"-staging-")
	if err != nil {
		return "", fmt.Errorf("install Claude plugin: %w", err)
	}
	defer os.RemoveAll(staging)
	if err := os.CopyFS(staging, plugin); err != nil {
		return "", fmt.Errorf("install Claude plugin: %w", err)
	}
	if err := os.Rename(staging, dir); err != nil {
		if _, statErr := os.Stat(dir); statErr == nil {
			return dir, nil // installed concurrently
		}
		return "", fmt.Errorf("install Claude plugin: %w", err)
	}
	return dir, nil
}
