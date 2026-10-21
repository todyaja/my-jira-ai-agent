package workflow

import "context"

// Workflow maps a Jira project's statuses to the actions the poller runs.
type Workflow interface {
	// Step returns the step for a Jira status name, ignoring case, and false
	// when the status is not part of the workflow.
	Step(status string) (Step, bool)
	// ErrorStatus is the status an issue whose action failed is parked in.
	ErrorStatus() string
}

// Step is what happens when an issue enters one status.
type Step struct {
	// Status names the step in logs.
	Status Status
	// AI is set when the action moves the issue on to another status, so
	// the poller checks the issue again afterwards.
	AI  bool
	Run func(ctx context.Context, issueKey string, deps Dependencies) error
}

// ScrumWorkflow is the PRD > TRD > implementation workflow with the
// statuses in Statuses.
type ScrumWorkflow struct{}

func (ScrumWorkflow) Step(name string) (Step, bool) {
	status, ok := ParseStatus(name)
	if !ok {
		return Step{}, false
	}
	return Step{
		Status: status,
		AI:     HasAIAction(status),
		Run: func(ctx context.Context, issueKey string, deps Dependencies) error {
			return Dispatch(ctx, status, issueKey, deps)
		},
	}, true
}

func (ScrumWorkflow) ErrorStatus() string {
	return string(StatusAIError)
}
