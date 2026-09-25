package jira

import (
	"encoding/json"
	"io"
)

type WebhookEvent struct {
	WebhookEvent string `json:"webhookEvent"`

	Issue struct {
		ID  string `json:"id"`
		Key string `json:"key"`

		Fields struct {
			Summary string `json:"summary"`

			Project struct {
				Key string `json:"key"`
			} `json:"project"`

			Status struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"status"`

			Assignee *User `json:"assignee"`
		} `json:"fields"`
	} `json:"issue"`

	Changelog struct {
		Items []ChangelogItem `json:"items"`
	} `json:"changelog"`
}

type User struct {
	AccountID   string `json:"accountId"`
	DisplayName string `json:"displayName"`
}

type ChangelogItem struct {
	Field      string  `json:"field"`
	FieldID    string  `json:"fieldId"`
	FromString *string `json:"fromString"`
	ToString   *string `json:"toString"`
}

func ParseWebhookEvent(r io.Reader) (WebhookEvent, error) {
	var event WebhookEvent
	err := json.NewDecoder(r).Decode(&event)
	return event, err
}

func (e WebhookEvent) StatusChangedTo(status string) bool {
	for _, item := range e.Changelog.Items {
		if item.Field == "status" && item.ToString != nil && *item.ToString == status {
			return true
		}
	}

	return false
}
