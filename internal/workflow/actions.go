package workflow

import (
	"context"
	"fmt"
	"log"
)

// Dependencies are the collaborators workflow actions may use.
type Dependencies struct {
	Reader    IssueReader
	Writer    IssueWriter
	Generator PRDGenerator
	Planner   TRDGenerator
	Engineer  Engineer
	Reviewer  Reviewer
	Workspace Workspace
	// MaxReviewCycles bounds the AI review rounds per implementation.
	MaxReviewCycles int
	Publisher       PagePublisher
	Logger          *log.Logger
}

type action func(ctx context.Context, issueKey string) error

func noOpAction(status Status, logger *log.Logger) action {
	return func(_ context.Context, issueKey string) error {
		logger.Printf("No AI action implemented for status %s: issue=%s", status, issueKey)
		return nil
	}
}

func backlogAction(logger *log.Logger) action {
	return noOpAction(StatusBacklog, logger)
}

func prdRequestedAction(deps Dependencies) action {
	return func(ctx context.Context, issueKey string) error {
		return ExecutePRDRequested(ctx, issueKey, deps.Reader, deps.Writer, deps.Generator, deps.Publisher, deps.Logger)
	}
}

func prdReviewAction(logger *log.Logger) action {
	return noOpAction(StatusPRDReview, logger)
}

func planningAction(deps Dependencies) action {
	return func(ctx context.Context, issueKey string) error {
		return ExecutePlanning(ctx, issueKey, deps.Reader, deps.Writer, deps.Planner, deps.Publisher, deps.Logger)
	}
}

func planReviewAction(logger *log.Logger) action {
	return noOpAction(StatusPlanReview, logger)
}

func readyToImplementAction(deps Dependencies) action {
	return func(ctx context.Context, issueKey string) error {
		maxReviews := deps.MaxReviewCycles
		if maxReviews <= 0 {
			maxReviews = DefaultMaxReviewCycles
		}
		return ExecuteReadyToImplement(ctx, issueKey, deps.Writer, maxReviews, deps.Logger)
	}
}

func implementingAction(deps Dependencies) action {
	return func(ctx context.Context, issueKey string) error {
		return ExecuteImplementing(ctx, issueKey, deps)
	}
}

func aiReviewAction(deps Dependencies) action {
	return func(ctx context.Context, issueKey string) error {
		return ExecuteAIReview(ctx, issueKey, deps)
	}
}

func prReadyAction(logger *log.Logger) action {
	return noOpAction(StatusPRReady, logger)
}

func doneAction(logger *log.Logger) action {
	return noOpAction(StatusDone, logger)
}

func aiErrorAction(logger *log.Logger) action {
	return noOpAction(StatusAIError, logger)
}

// HasAIAction reports whether an agent works on issues in status. Such an
// action moves the issue to another status when it finishes.
func HasAIAction(status Status) bool {
	switch status {
	case StatusPRDRequested, StatusPlanning, StatusReadyToImplement, StatusImplementing, StatusAIReview:
		return true
	}
	return false
}

// Dispatch runs the action for the status an issue just moved into.
func Dispatch(ctx context.Context, status Status, issueKey string, deps Dependencies) error {
	var selected action
	switch status {
	case StatusBacklog:
		selected = backlogAction(deps.Logger)
	case StatusPRDRequested:
		selected = prdRequestedAction(deps)
	case StatusPRDReview:
		selected = prdReviewAction(deps.Logger)
	case StatusPlanning:
		selected = planningAction(deps)
	case StatusPlanReview:
		selected = planReviewAction(deps.Logger)
	case StatusReadyToImplement:
		selected = readyToImplementAction(deps)
	case StatusImplementing:
		selected = implementingAction(deps)
	case StatusAIReview:
		selected = aiReviewAction(deps)
	case StatusPRReady:
		selected = prReadyAction(deps.Logger)
	case StatusDone:
		selected = doneAction(deps.Logger)
	case StatusAIError:
		selected = aiErrorAction(deps.Logger)
	default:
		return fmt.Errorf("unknown workflow status %q", status)
	}

	return selected(ctx, issueKey)
}
