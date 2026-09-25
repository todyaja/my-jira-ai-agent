package workflow

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
	StatusAIFailed         Status = "AI FAILED"
)
