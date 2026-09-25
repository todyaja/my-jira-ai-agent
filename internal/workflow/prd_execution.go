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
}

type IssueWriter interface {
	UpdateDescription(context.Context, string, json.RawMessage) error
	TransitionTo(context.Context, string, string) error
}

func ExecutePRDRequested(ctx context.Context, key string, reader IssueReader, writer IssueWriter, generator PRDGenerator, logger *log.Logger) error {
	issue, err := reader.GetIssue(ctx, key)
	if err != nil {
		logPRDOperationFailure(logger, key, "get issue", err, 0)
		return fmt.Errorf("get issue: %w", err)
	}
	if _, err := jira.DescriptionText(issue.DescriptionADF); err != nil {
		logPRDOperationFailure(logger, key, "convert description", err, 0)
		return fmt.Errorf("convert description: %w", err)
	}

	startedAt := time.Now()
	if logger != nil {
		logger.Printf("PRD generation started: issue=%s", key)
	}
	prd, err := generator.Generate(ctx, PRDInput{
		IssueKey:    issue.Key,
		Summary:     issue.Summary,
		Description: issue.Description,
	})
	if err != nil {
		logPRDOperationFailure(logger, key, "generate PRD", err, time.Since(startedAt))
		return fmt.Errorf("generate PRD: %w", err)
	}
	if logger != nil {
		logger.Printf("PRD generation completed: issue=%s duration=%s", key, time.Since(startedAt))
	}

	description, err := jira.AppendPRDToADF(issue.DescriptionADF, prd)
	if err != nil {
		logPRDOperationFailure(logger, key, "build description", err, 0)
		return fmt.Errorf("build description: %w", err)
	}
	if err := writer.UpdateDescription(ctx, key, description); err != nil {
		logPRDOperationFailure(logger, key, "update description", err, 0)
		return fmt.Errorf("update description: %w", err)
	}

	if err := writer.TransitionTo(ctx, key, "PRD REVIEW"); err != nil {
		logPRDOperationFailure(logger, key, "transition", err, 0)
		return fmt.Errorf("transition: %w", err)
	}

	return nil
}

func logPRDOperationFailure(logger *log.Logger, key, operation string, err error, duration time.Duration) {
	if logger != nil {
		if duration > 0 {
			logger.Printf("PRD operation failed: issue=%s operation=%s duration=%s error=%v", key, operation, duration, err)
			return
		}
		logger.Printf("PRD operation failed: issue=%s operation=%s error=%v", key, operation, err)
	}
}
