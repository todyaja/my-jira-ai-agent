package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/joho/godotenv"
	"github.com/tody-aja/jira-ai-agent/internal/agent"
	"github.com/tody-aja/jira-ai-agent/internal/jira"
	"github.com/tody-aja/jira-ai-agent/internal/workflow"
)

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("Warning: .env file not found")
	}

	myAccountID := os.Getenv("JIRA_ACCOUNT_ID")

	if myAccountID == "" {
		log.Fatal("JIRA_ACCOUNT_ID is not configured")
	}

	prdGenerator, err := buildWorkflowDependencies()
	if err != nil {
		log.Fatal(err)
	}
	jiraClient := jira.NewClient(
		os.Getenv("JIRA_BASE_URL"),
		os.Getenv("JIRA_EMAIL"),
		os.Getenv("JIRA_API_TOKEN"),
		nil,
	)

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	http.HandleFunc("/webhooks/jira", func(w http.ResponseWriter, r *http.Request) {
		handleJiraWebhook(w, r, myAccountID, jiraClient, jiraClient, prdGenerator, log.Default())
	})

	log.Println("Jira AI Agent listening on :8080")

	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatal(err)
	}
}

func buildWorkflowDependencies() (workflow.PRDGenerator, error) {
	config, err := agent.LoadOpenCodeConfig(os.Getenv)
	if err != nil {
		return nil, err
	}
	return agent.NewOpenCodePRDGenerator(config)
}

func handleJiraWebhook(w http.ResponseWriter, r *http.Request, accountID string, reader workflow.IssueReader, writer workflow.IssueWriter, generator workflow.PRDGenerator, logger *log.Logger) {
	event, err := jira.ParseWebhookEvent(r.Body)
	if err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}

	if event.WebhookEvent != "jira:issue_updated" {
		w.WriteHeader(http.StatusOK)
		return
	}

	if event.Issue.Fields.Assignee == nil ||
		event.Issue.Fields.Assignee.AccountID != accountID {
		w.WriteHeader(http.StatusOK)
		return
	}

	if !event.StatusChangedTo("PRD Requested") {
		w.WriteHeader(http.StatusOK)
		return
	}

	logger.Printf("PRD WORKFLOW TRIGGERED FOR %s", event.Issue.Key)
	workflowContext := context.WithoutCancel(r.Context())
	go func() {
		err := workflow.ExecutePRDRequested(workflowContext, event.Issue.Key, reader, writer, generator, logger)
		if err != nil {
			logger.Printf("PRD operation failed: issue=%s operation=execute PRD requested", event.Issue.Key)
			return
		}
		logger.Printf("PRD operation succeeded: issue=%s operation=execute PRD requested", event.Issue.Key)
	}()
	w.WriteHeader(http.StatusOK)
}
