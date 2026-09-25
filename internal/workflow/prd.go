package workflow

import "context"

type PRDInput struct {
	IssueKey    string
	Summary     string
	Description string
}

type PRDGenerator interface {
	Generate(context.Context, PRDInput) (string, error)
}
