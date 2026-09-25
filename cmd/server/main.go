package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"

	"github.com/joho/godotenv"
	"github.com/tody-aja/jira-ai-agent/internal/jira"
)

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("Warning: .env file not found")
	}

	myAccountID := os.Getenv("JIRA_ACCOUNT_ID")

	if myAccountID == "" {
		log.Fatal("JIRA_ACCOUNT_ID is not configured")
	}

	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})

	http.HandleFunc("/webhooks/jira", func(w http.ResponseWriter, r *http.Request) {
		var event jira.WebhookEvent

		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}

		// Ignore unassigned tickets.
		if event.Issue.Fields.Assignee == nil {
			w.WriteHeader(http.StatusOK)
			return
		}

		// Ignore tickets assigned to somebody else.
		if event.Issue.Fields.Assignee.AccountID != myAccountID {
			w.WriteHeader(http.StatusOK)
			return
		}

		// From here down, the ticket is assigned to me.

		log.Println("=== MY JIRA EVENT ===")
		log.Printf("Issue:   %s", event.Issue.Key)
		log.Printf("Summary: %s", event.Issue.Fields.Summary)
		log.Printf("Status:  %s", event.Issue.Fields.Status.Name)
		log.Printf(
			"Assignee: %s",
			event.Issue.Fields.Assignee.DisplayName,
		)

		for _, change := range event.Changelog.Items {
			from := "<empty>"
			to := "<empty>"

			if change.FromString != nil {
				from = *change.FromString
			}

			if change.ToString != nil {
				to = *change.ToString
			}

			log.Printf(
				"Changed: %s | %s -> %s",
				change.Field,
				from,
				to,
			)
		}

		log.Println("=====================")

		w.WriteHeader(http.StatusOK)
	})

	log.Println("Jira AI Agent listening on :8080")

	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatal(err)
	}
}