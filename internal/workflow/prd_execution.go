package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

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
		logPRDOperationFailure(logger, key, "get issue", err)
		return fmt.Errorf("get issue: %w", err)
	}
	if _, err := jira.DescriptionText(issue.DescriptionADF); err != nil {
		logPRDOperationFailure(logger, key, "convert description", err)
		return fmt.Errorf("convert description: %w", err)
	}

	prd, err := generator.Generate(ctx, PRDInput{
		IssueKey:    issue.Key,
		Summary:     issue.Summary,
		Description: issue.Description,
	})
	if err != nil {
		logPRDOperationFailure(logger, key, "generate PRD", err)
		return fmt.Errorf("generate PRD: %w", err)
	}

	description, err := jira.AppendPRDToADF(issue.DescriptionADF, prd)
	if err != nil {
		logPRDOperationFailure(logger, key, "build description", err)
		return fmt.Errorf("build description: %w", err)
	}
	if err := writer.UpdateDescription(ctx, key, description); err != nil {
		logPRDOperationFailure(logger, key, "update description", err)
		return fmt.Errorf("update description: %w", err)
	}

	if err := writer.TransitionTo(ctx, key, "PRD REVIEW"); err != nil {
		logPRDOperationFailure(logger, key, "transition", err)
		return fmt.Errorf("transition: %w", err)
	}

	return nil
}

func logPRDOperationFailure(logger *log.Logger, key, operation string, err error) {
	if logger != nil {
		logger.Printf("PRD operation failed: issue=%s operation=%s error=%v", key, operation, err)
	}
}
