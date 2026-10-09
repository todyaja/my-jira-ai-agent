package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/tody-aja/jira-ai-agent/internal/jira"
)

type IssueReader interface {
	GetIssue(context.Context, string) (jira.Issue, error)
	GetComments(context.Context, string) ([]jira.Comment, error)
	GetIssueProperty(ctx context.Context, key, propertyKey string) (json.RawMessage, bool, error)
}

type IssueWriter interface {
	UpdateDescription(context.Context, string, json.RawMessage) error
	TransitionTo(context.Context, string, string) error
	AddComment(ctx context.Context, key string, body json.RawMessage) (string, error)
	SetCommentProperty(ctx context.Context, commentID, propertyKey string, value json.RawMessage) error
	SetIssueProperty(ctx context.Context, key, propertyKey string, value json.RawMessage) error
}

// ExecutePRDRequested writes the issue's PRD, publishes it and moves the
// issue to PRD Review.
//
// Without a published PRD it writes one from the issue. Once one exists it
// only revises it based on the feedback comments not handled yet (see
// PRDFeedbackPrefix); each of those gets a "done" reply and is marked handled.
// With a published PRD and no outstanding feedback nothing is regenerated.
func ExecutePRDRequested(ctx context.Context, key string, reader IssueReader, writer IssueWriter, generator PRDGenerator, publisher PagePublisher, logger *log.Logger) error {
	issue, err := reader.GetIssue(ctx, key)
	if err != nil {
		logOperationFailure(logger, "PRD", key, "get issue", err, 0)
		return fmt.Errorf("get issue: %w", err)
	}
	if _, err := jira.DescriptionText(issue.DescriptionADF); err != nil {
		logOperationFailure(logger, "PRD", key, "convert description", err, 0)
		return fmt.Errorf("convert description: %w", err)
	}

	comments, err := reader.GetComments(ctx, key)
	if err != nil {
		logOperationFailure(logger, "PRD", key, "get comments", err, 0)
		return fmt.Errorf("get comments: %w", err)
	}
	input := PRDInput{
		IssueKey:    issue.Key,
		Summary:     issue.Summary,
		Description: issue.Description,
		Feedback:    PRDFeedback(comments),
	}
	currentPRD, found, err := publisher.PageContent(ctx, PRDPageTitle(input))
	if err != nil {
		logOperationFailure(logger, "PRD", key, "get current PRD", err, 0)
		return fmt.Errorf("get current PRD: %w", err)
	}
	input.CurrentPRD = currentPRD

	if found && len(input.Feedback) == 0 {
		return prdFeedback.skipRevision(ctx, key, writer, logger)
	}

	mode := "initial"
	if found {
		mode = "revision"
	}
	startedAt := time.Now()
	if logger != nil {
		logger.Printf("PRD generation started: issue=%s mode=%s feedback=%d", key, mode, len(input.Feedback))
	}
	prd, err := generator.Generate(ctx, input)
	if err != nil {
		logOperationFailure(logger, "PRD", key, "generate PRD", err, time.Since(startedAt))
		return fmt.Errorf("generate PRD: %w", err)
	}
	if logger != nil {
		logger.Printf("PRD generation completed: issue=%s duration=%s", key, time.Since(startedAt))
	}

	pageURL, err := publisher.PublishPage(ctx, PRDPageTitle(input), prd)
	if err != nil {
		logOperationFailure(logger, "PRD", key, "publish PRD", err, 0)
		return fmt.Errorf("publish PRD: %w", err)
	}
	if logger != nil {
		logger.Printf("PRD published: issue=%s url=%s", key, pageURL)
	}

	description, err := jira.AppendPRDLinkToADF(issue.DescriptionADF, pageURL)
	if err != nil {
		logOperationFailure(logger, "PRD", key, "build description", err, 0)
		return fmt.Errorf("build description: %w", err)
	}
	if err := writer.UpdateDescription(ctx, key, description); err != nil {
		logOperationFailure(logger, "PRD", key, "update description", err, 0)
		return fmt.Errorf("update description: %w", err)
	}

	if err := prdFeedback.markAllDone(ctx, key, writer, input.Feedback, pageURL, logger); err != nil {
		return err
	}

	if err := writer.TransitionTo(ctx, key, string(StatusPRDReview)); err != nil {
		logOperationFailure(logger, "PRD", key, "transition", err, 0)
		return fmt.Errorf("transition: %w", err)
	}

	return nil
}

// logOperationFailure logs a failed step of a document workflow (PRD, TRD)
// without any generated text.
func logOperationFailure(logger *log.Logger, document, key, operation string, err error, duration time.Duration) {
	if logger != nil {
		if duration > 0 {
			logger.Printf("%s operation failed: issue=%s operation=%s duration=%s error=%v", document, key, operation, duration, err)
			return
		}
		logger.Printf("%s operation failed: issue=%s operation=%s error=%v", document, key, operation, err)
	}
}
