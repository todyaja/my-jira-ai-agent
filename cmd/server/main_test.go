package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadPollInterval(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    time.Duration
		wantErr bool
	}{
		{name: "default when unset", value: "", want: defaultPollInterval},
		{name: "configured seconds", value: "15", want: 15 * time.Second},
		{name: "rejects zero", value: "0", wantErr: true},
		{name: "rejects negative", value: "-5", wantErr: true},
		{name: "rejects non-integer", value: "1m", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := loadPollInterval(func(key string) string {
				if key == "JIRA_POLL_INTERVAL_SECONDS" {
					return tt.value
				}
				return ""
			})
			if tt.wantErr {
				if err == nil {
					t.Fatalf("loadPollInterval() = %s, want error", got)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("loadPollInterval() = (%s, %v), want %s", got, err, tt.want)
			}
		})
	}
}

func TestLoadConfluenceClient(t *testing.T) {
	jiraEnv := map[string]string{
		"JIRA_BASE_URL":        "https://example.atlassian.net",
		"JIRA_EMAIL":           "jira@example.com",
		"JIRA_API_TOKEN":       "jira-token",
		"CONFLUENCE_SPACE_KEY": " ENG ",
	}

	client, err := loadConfluenceClient(func(key string) string { return jiraEnv[key] })
	if err != nil {
		t.Fatalf("loadConfluenceClient() error = %v", err)
	}
	if client.BaseURL != "https://example.atlassian.net" || client.Email != "jira@example.com" || client.Token != "jira-token" || client.SpaceKey != "ENG" || client.ParentPageID != "" {
		t.Fatalf("client = %+v, want Jira site and credentials", client)
	}

	overrides := map[string]string{
		"CONFLUENCE_BASE_URL":       "https://docs.atlassian.net",
		"CONFLUENCE_EMAIL":          "docs@example.com",
		"CONFLUENCE_API_TOKEN":      "docs-token",
		"CONFLUENCE_SPACE_KEY":      "DOCS",
		"CONFLUENCE_PARENT_PAGE_ID": "123",
	}
	client, err = loadConfluenceClient(func(key string) string {
		if value, ok := overrides[key]; ok {
			return value
		}
		return jiraEnv[key]
	})
	if err != nil {
		t.Fatalf("loadConfluenceClient() error = %v", err)
	}
	if client.BaseURL != "https://docs.atlassian.net" || client.Email != "docs@example.com" || client.Token != "docs-token" || client.SpaceKey != "DOCS" || client.ParentPageID != "123" {
		t.Fatalf("client = %+v, want Confluence overrides", client)
	}

	for name, env := range map[string]map[string]string{
		"missing space": {"JIRA_BASE_URL": "https://example.atlassian.net"},
		"missing site":  {"CONFLUENCE_SPACE_KEY": "ENG"},
	} {
		_, err := loadConfluenceClient(func(key string) string { return env[key] })
		if err == nil || !strings.Contains(err.Error(), "CONFLUENCE_") {
			t.Errorf("%s: loadConfluenceClient() error = %v, want configuration error", name, err)
		}
	}
}

func TestLoadWorkspaceAndReviewCycles(t *testing.T) {
	env := map[string]string{"REPO_PATH": "../to-do-app"}
	workspace, err := loadWorkspace(func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("loadWorkspace() error = %v", err)
	}
	if workspace.BaseBranch != "main" || !filepath.IsAbs(workspace.RepoPath) || workspace.Root != workspace.RepoPath+"-worktrees" {
		t.Fatalf("workspace = %+v, want main, absolute repo and sibling worktrees", workspace)
	}

	env = map[string]string{"REPO_PATH": "../to-do-app", "BASE_BRANCH": "master", "WORKTREES_PATH": "../trees"}
	workspace, _ = loadWorkspace(func(key string) string { return env[key] })
	if workspace.BaseBranch != "master" || filepath.Base(workspace.Root) != "trees" {
		t.Fatalf("workspace = %+v, want configured base branch and root", workspace)
	}

	for value, want := range map[string]int{"": 3, "5": 5} {
		if got, err := loadMaxReviewCycles(func(string) string { return value }); err != nil || got != want {
			t.Fatalf("loadMaxReviewCycles(%q) = (%d, %v), want %d", value, got, err, want)
		}
	}
	if _, err := loadMaxReviewCycles(func(string) string { return "0" }); err == nil {
		t.Fatal("loadMaxReviewCycles(0) error = nil, want rejection")
	}
}
