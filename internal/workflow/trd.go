package workflow

import "context"

type TRDInput struct {
	IssueKey    string
	Summary     string
	Description string
	// PRD is the published PRD in Confluence storage format.
	PRD string
	// CurrentTRD is the published TRD in Confluence storage format, set when
	// Feedback asks for a revision of an existing page.
	CurrentTRD string
	// Feedback holds the reviewers' comments not handled yet, oldest first.
	Feedback []Feedback
}

// TRDGenerator studies the repository and writes a TRD for the PRD.
type TRDGenerator interface {
	GenerateTRD(context.Context, TRDInput) (string, error)
}

// TRDPageTitle names an issue's TRD page; it is stable per issue so a
// regenerated TRD replaces the previous page.
func TRDPageTitle(issue TRDInput) string {
	return issue.IssueKey + ": " + issue.Summary + " TRD"
}
