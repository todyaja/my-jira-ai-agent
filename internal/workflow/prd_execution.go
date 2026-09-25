package workflow

import (
	"context"
	"fmt"
	"log"

	"github.com/tody-aja/jira-ai-agent/internal/jira"
)

type IssueReader interface {
	GetIssue(context.Context, string) (jira.Issue, error)
}

type IssueWriter interface {
	UpdateDescription(context.Context, string, string) error
	TransitionTo(context.Context, string, string) error
}

func ExecutePRDRequested(ctx context.Context, key string, reader IssueReader, writer IssueWriter, generator PRDGenerator, logger *log.Logger) error {
	issue, err := reader.GetIssue(ctx, key)
	if err != nil {
		logPRDOperationFailure(logger, key, "get issue")
		return fmt.Errorf("get issue: %w", err)
	}

	prd, err := generator.Generate(ctx, PRDInput{
		IssueKey:    issue.Key,
		Summary:     issue.Summary,
		Description: issue.Description,
	})
	if err != nil {
		logPRDOperationFailure(logger, key, "generate PRD")
		return fmt.Errorf("generate PRD: %w", err)
	}

	description := jira.AppendPRD(issue.Description, prd)
	if err := writer.UpdateDescription(ctx, key, description); err != nil {
		logPRDOperationFailure(logger, key, "update description")
		return fmt.Errorf("update description: %w", err)
	}

	if err := writer.TransitionTo(ctx, key, "PRD REVIEW"); err != nil {
		logPRDOperationFailure(logger, key, "transition")
		return fmt.Errorf("transition: %w", err)
	}

	return nil
}

func logPRDOperationFailure(logger *log.Logger, key, operation string) {
	if logger != nil {
		logger.Printf("PRD operation failed: issue=%s operation=%s", key, operation)
	}
}
