package main

import (
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
	// The webhook dispatcher will consume this dependency when PRD execution is wired in.
	_ = prdGenerator

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	http.HandleFunc("/webhooks/jira", func(w http.ResponseWriter, r *http.Request) {
		handleJiraWebhook(w, r, myAccountID, log.Default())
	})

	log.Println("Jira AI Agent listening on :8080")

	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatal(err)
	}
}

func buildWorkflowDependencies() (workflow.PRDGenerator, error) {
	config, err := agent.LoadOpenAIConfig(os.Getenv)
	if err != nil {
		return nil, err
	}
	return agent.NewOpenAIPRDGenerator(config)
}

func handleJiraWebhook(w http.ResponseWriter, r *http.Request, accountID string, logger *log.Logger) {
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
	w.WriteHeader(http.StatusOK)
}
