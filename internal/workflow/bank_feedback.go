package workflow

import (
	"context"
	"fmt"
	"log"

	"github.com/tody-aja/jira-ai-agent/internal/jira"
)

// PageComment is a reviewer's comment on a published page that has not been
// applied yet.
type PageComment struct {
	ID     string
	PageID string
	// Inline comments highlight Selection on the page; Dangling ones lost
	// their highlight because the text was changed.
	Inline    bool
	Dangling  bool
	Created   string
	Text      string
	Selection string
}

// pageReview is how reviewers give feedback on a bank document: every open
// comment on its Confluence pages is feedback, with no prefix.
type pageReview struct {
	// document names the document in replies and logs, e.g. "RFC".
	document string
	// requestLabel is the Jira status that asks for a revision.
	requestLabel string
	reviewStatus Status
}

var (
	rfcReview     = pageReview{document: "RFC", requestLabel: string(BankRequestRFC), reviewStatus: BankRFCReview}
	trdReview     = pageReview{document: "TRD", requestLabel: string(BankRequestTRD), reviewStatus: BankTRDReview}
	fsdReview     = pageReview{document: "FSD", requestLabel: string(BankFSDRequest), reviewStatus: BankFSDReview}
	runbookReview = pageReview{document: "runbook", requestLabel: string(BankRunbookRequest), reviewStatus: BankRunbookReview}
)

// pageFeedback returns the open comments on the published pages, page by
// page, each oldest first.
func pageFeedback(ctx context.Context, publisher DocumentPublisher, pages []documentRecord) ([]Feedback, error) {
	feedback := []Feedback{}
	for _, page := range pages {
		if page.PageID == "" {
			continue
		}
		comments, err := publisher.OpenComments(ctx, page.PageID)
		if err != nil {
			return nil, fmt.Errorf("comments on %q: %w", page.Title, err)
		}
		for index := range comments {
			comment := comments[index]
			if comment.Text == "" {
				continue
			}
			feedback = append(feedback, Feedback{
				Comment:     jira.Comment{ID: comment.ID, Created: comment.Created},
				Text:        comment.Text,
				Page:        page.Title,
				Selection:   comment.Selection,
				pageComment: &comment,
			})
		}
	}
	return feedback, nil
}

// closeAll replies "done" to every applied comment and resolves it. It runs
// before the revised pages are published, because republishing removes the
// highlights of inline comments and Confluence cannot resolve them after.
func (r pageReview) closeAll(ctx context.Context, key string, publisher DocumentPublisher, feedback []Feedback, logger *log.Logger) error {
	reply := "Done - applied in the revised " + r.document + "."
	for _, item := range feedback {
		if item.pageComment == nil {
			continue
		}
		if err := publisher.CloseComment(ctx, *item.pageComment, reply); err != nil {
			logOperationFailure(logger, r.document, key, "close comment", err, 0)
			return fmt.Errorf("close comment %s on %q: %w", item.pageComment.ID, item.Page, err)
		}
	}
	return nil
}

// skipRevision leaves an existing document untouched when its pages have no
// open comments, tells the reviewer why, and moves the issue back to review.
func (r pageReview) skipRevision(ctx context.Context, key string, writer IssueWriter, logger *log.Logger) error {
	if logger != nil {
		logger.Printf("%s unchanged, no open comments: issue=%s", r.document, key)
	}
	message := fmt.Sprintf("Nothing to revise: the %s pages in Confluence have no open comments, so the %s is unchanged. Comment on the page and move the issue back to %s to request changes.", r.document, r.document, r.requestLabel)
	if _, err := writer.AddComment(ctx, key, jira.TextADF(message)); err != nil {
		logOperationFailure(logger, r.document, key, "comment", err, 0)
		return fmt.Errorf("comment: %w", err)
	}
	if err := writer.TransitionTo(ctx, key, string(r.reviewStatus)); err != nil {
		logOperationFailure(logger, r.document, key, "transition", err, 0)
		return fmt.Errorf("transition: %w", err)
	}
	return nil
}
