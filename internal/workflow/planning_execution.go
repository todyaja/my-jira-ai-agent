package workflow

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/tody-aja/jira-ai-agent/internal/jira"
)

// ExecutePlanning reads the issue's published PRD, has the planner study the
// repository and write a TRD, publishes the TRD, links it below the PRD in
// the description and moves the issue to Plan Review.
//
// Once a TRD exists it is only revised based on the feedback comments not
// handled yet (see TRDFeedbackPrefix); each of those gets a "done" reply and
// is marked handled. With a TRD and no outstanding feedback nothing is
// regenerated.
func ExecutePlanning(ctx context.Context, key string, reader IssueReader, writer IssueWriter, planner TRDGenerator, publisher PagePublisher, logger *log.Logger) error {
	issue, err := reader.GetIssue(ctx, key)
	if err != nil {
		logOperationFailure(logger, "TRD", key, "get issue", err, 0)
		return fmt.Errorf("get issue: %w", err)
	}
	if _, err := jira.DescriptionText(issue.DescriptionADF); err != nil {
		logOperationFailure(logger, "TRD", key, "convert description", err, 0)
		return fmt.Errorf("convert description: %w", err)
	}

	prdTitle := PRDPageTitle(PRDInput{IssueKey: issue.Key, Summary: issue.Summary})
	prd, found, err := publisher.PageContent(ctx, prdTitle)
	if err != nil {
		logOperationFailure(logger, "TRD", key, "get PRD", err, 0)
		return fmt.Errorf("get PRD: %w", err)
	}
	if !found {
		err := fmt.Errorf("no PRD page titled %q", prdTitle)
		logOperationFailure(logger, "TRD", key, "get PRD", err, 0)
		return fmt.Errorf("get PRD: %w", err)
	}

	comments, err := reader.GetComments(ctx, key)
	if err != nil {
		logOperationFailure(logger, "TRD", key, "get comments", err, 0)
		return fmt.Errorf("get comments: %w", err)
	}
	input := TRDInput{
		IssueKey:    issue.Key,
		Summary:     issue.Summary,
		Description: issue.Description,
		PRD:         prd,
		Feedback:    TRDFeedback(comments),
	}
	currentTRD, trdFound, err := publisher.PageContent(ctx, TRDPageTitle(input))
	if err != nil {
		logOperationFailure(logger, "TRD", key, "get current TRD", err, 0)
		return fmt.Errorf("get current TRD: %w", err)
	}
	input.CurrentTRD = currentTRD
	if trdFound && len(input.Feedback) == 0 {
		return trdFeedback.skipRevision(ctx, key, writer, logger)
	}

	mode := "initial"
	if trdFound {
		mode = "revision"
	}
	startedAt := time.Now()
	if logger != nil {
		logger.Printf("TRD generation started: issue=%s mode=%s feedback=%d", key, mode, len(input.Feedback))
	}
	trd, err := planner.GenerateTRD(ctx, input)
	if err != nil {
		logOperationFailure(logger, "TRD", key, "generate TRD", err, time.Since(startedAt))
		return fmt.Errorf("generate TRD: %w", err)
	}
	if logger != nil {
		logger.Printf("TRD generation completed: issue=%s duration=%s", key, time.Since(startedAt))
	}

	pageURL, err := publisher.PublishPage(ctx, TRDPageTitle(input), trd)
	if err != nil {
		logOperationFailure(logger, "TRD", key, "publish TRD", err, 0)
		return fmt.Errorf("publish TRD: %w", err)
	}
	if logger != nil {
		logger.Printf("TRD published: issue=%s url=%s", key, pageURL)
	}

	description, err := jira.AppendTRDLinkToADF(issue.DescriptionADF, pageURL)
	if err != nil {
		logOperationFailure(logger, "TRD", key, "build description", err, 0)
		return fmt.Errorf("build description: %w", err)
	}
	if err := writer.UpdateDescription(ctx, key, description); err != nil {
		logOperationFailure(logger, "TRD", key, "update description", err, 0)
		return fmt.Errorf("update description: %w", err)
	}

	if err := trdFeedback.markAllDone(ctx, key, writer, input.Feedback, pageURL, logger); err != nil {
		return err
	}

	if err := writer.TransitionTo(ctx, key, string(StatusPlanReview)); err != nil {
		logOperationFailure(logger, "TRD", key, "transition", err, 0)
		return fmt.Errorf("transition: %w", err)
	}
	return nil
}
