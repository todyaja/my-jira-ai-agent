package workflow

import "strings"

type Status string

const (
	StatusBacklog          Status = "BACKLOG"
	StatusPRDRequested     Status = "PRD REQUESTED"
	StatusPRDReview        Status = "PRD REVIEW"
	StatusPlanning         Status = "PLANNING"
	StatusPlanReview       Status = "PLAN REVIEW"
	StatusReadyToImplement Status = "READY TO IMPLEMENT"
	StatusImplementing     Status = "IMPLEMENTING"
	StatusAIReview         Status = "AI REVIEW"
	StatusPRReady          Status = "PR READY"
	StatusDone             Status = "DONE"
	StatusAIError          Status = "AI ERROR"
)

// Statuses lists every workflow status in board order.
var Statuses = []Status{
	StatusBacklog,
	StatusPRDRequested,
	StatusPRDReview,
	StatusPlanning,
	StatusPlanReview,
	StatusReadyToImplement,
	StatusImplementing,
	StatusAIReview,
	StatusPRReady,
	StatusDone,
	StatusAIError,
}

// ParseStatus maps a Jira status name to a workflow status, ignoring case,
// since Jira shows names like "PRD Requested".
func ParseStatus(name string) (Status, bool) {
	for _, status := range Statuses {
		if strings.EqualFold(strings.TrimSpace(name), string(status)) {
			return status, true
		}
	}
	return "", false
}
