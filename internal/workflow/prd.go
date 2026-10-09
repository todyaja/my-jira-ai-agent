package workflow

import "context"

type PRDInput struct {
	IssueKey    string
	Summary     string
	Description string
	// CurrentPRD is the published PRD in Confluence storage format. It is
	// set only when Feedback asks for a revision of an existing page.
	CurrentPRD string
	// Feedback holds the reviewers' comments not handled yet, oldest first.
	Feedback []Feedback
}

type PRDGenerator interface {
	Generate(context.Context, PRDInput) (string, error)
}

// PagePublisher publishes a Markdown document as a page, replacing the page
// with the same title if there is one, and returns the page's URL. It also
// reads back a page's current storage-format content.
type PagePublisher interface {
	PublishPage(ctx context.Context, title, markdown string) (string, error)
	PageContent(ctx context.Context, title string) (content string, found bool, err error)
}

// PRDPageTitle names an issue's PRD page; it is stable per issue so a
// regenerated PRD replaces the previous page.
func PRDPageTitle(issue PRDInput) string {
	return issue.IssueKey + ": " + issue.Summary + " PRD"
}
