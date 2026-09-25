package workflow

import (
	"context"
	"fmt"
	"log"
)

type action func(context.Context, PRDInput) error

func noOpAction(status Status, logger *log.Logger) action {
	return func(context.Context, PRDInput) error {
		logger.Printf("No AI action implemented for status %s", status)
		return nil
	}
}

func backlogAction(logger *log.Logger) action {
	return noOpAction(StatusBacklog, logger)
}

func prdRequestedAction(generator PRDGenerator, logger *log.Logger) action {
	return func(ctx context.Context, input PRDInput) error {
		if _, err := generator.Generate(ctx, input); err != nil {
			return err
		}
		logger.Printf("PRD generation completed for %s", input.IssueKey)
		return nil
	}
}

func prdReviewAction(logger *log.Logger) action {
	return noOpAction(StatusPRDReview, logger)
}

func planningAction(logger *log.Logger) action {
	return noOpAction(StatusPlanning, logger)
}

func planReviewAction(logger *log.Logger) action {
	return noOpAction(StatusPlanReview, logger)
}

func readyToImplementAction(logger *log.Logger) action {
	return noOpAction(StatusReadyToImplement, logger)
}

func implementingAction(logger *log.Logger) action {
	return noOpAction(StatusImplementing, logger)
}

func aiReviewAction(logger *log.Logger) action {
	return noOpAction(StatusAIReview, logger)
}

func prReadyAction(logger *log.Logger) action {
	return noOpAction(StatusPRReady, logger)
}

func doneAction(logger *log.Logger) action {
	return noOpAction(StatusDone, logger)
}

func aiFailedAction(logger *log.Logger) action {
	return noOpAction(StatusAIFailed, logger)
}

func Dispatch(ctx context.Context, status Status, input PRDInput, generator PRDGenerator, logger *log.Logger) error {
	var selected action
	switch status {
	case StatusBacklog:
		selected = backlogAction(logger)
	case StatusPRDRequested:
		selected = prdRequestedAction(generator, logger)
	case StatusPRDReview:
		selected = prdReviewAction(logger)
	case StatusPlanning:
		selected = planningAction(logger)
	case StatusPlanReview:
		selected = planReviewAction(logger)
	case StatusReadyToImplement:
		selected = readyToImplementAction(logger)
	case StatusImplementing:
		selected = implementingAction(logger)
	case StatusAIReview:
		selected = aiReviewAction(logger)
	case StatusPRReady:
		selected = prReadyAction(logger)
	case StatusDone:
		selected = doneAction(logger)
	case StatusAIFailed:
		selected = aiFailedAction(logger)
	default:
		return fmt.Errorf("unknown workflow status %q", status)
	}

	return selected(ctx, input)
}
