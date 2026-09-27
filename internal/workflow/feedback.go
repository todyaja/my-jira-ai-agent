package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/tody-aja/jira-ai-agent/internal/jira"
)

// Reviewers address an agent by starting a Jira comment with its prefix.
// Once a revision applies a comment, the agent replies "done" and marks the
// comment with the done property so later revisions ignore it.
const (
	PRDFeedbackPrefix       = "!prd-maker"
	PRDFeedbackDoneProperty = "jira-ai-agent.prd-maker"
	TRDFeedbackPrefix       = "!tech-planner"
	TRDFeedbackDoneProperty = "jira-ai-agent.tech-planner"
)

// Feedback is a comment addressed to an agent.
type Feedback struct {
	Comment jira.Comment
	// Text is the comment without its prefix.
	Text string
}

type feedbackDoneValue struct {
	Done    bool   `json:"done"`
	PageURL string `json:"pageUrl"`
}

// feedbackChannel is how reviewers give feedback on one document.
type feedbackChannel struct {
	prefix       string
	doneProperty string
	// document names the document in replies and logs, e.g. "PRD".
	document string
	// requestLabel is the Jira status that asks for a revision, as
	// reviewers see it.
	requestLabel string
	reviewStatus Status
}

var (
	prdFeedback = feedbackChannel{prefix: PRDFeedbackPrefix, doneProperty: PRDFeedbackDoneProperty, document: "PRD", requestLabel: "PRD Requested", reviewStatus: StatusPRDReview}
	trdFeedback = feedbackChannel{prefix: TRDFeedbackPrefix, doneProperty: TRDFeedbackDoneProperty, document: "TRD", requestLabel: "Planning", reviewStatus: StatusPlanReview}
)

// PRDFeedback returns the outstanding comments addressed to the PRD agent.
func PRDFeedback(comments []jira.Comment) []Feedback {
	return prdFeedback.outstanding(comments)
}

// TRDFeedback returns the outstanding comments addressed to the technical
// planner.
func TRDFeedback(comments []jira.Comment) []Feedback {
	return trdFeedback.outstanding(comments)
}

// outstanding keeps the comments that start with the prefix, ignoring case,
// and are not marked done, oldest first. It strips the prefix and any
// separator after it.
func (c feedbackChannel) outstanding(comments []jira.Comment) []Feedback {
	feedback := []Feedback{}
	for _, comment := range comments {
		if _, done := comment.Properties[c.doneProperty]; done {
			continue
		}
		text, ok := cutPrefix(comment.Body, c.prefix)
		if !ok || text == "" {
			continue
		}
		feedback = append(feedback, Feedback{Comment: comment, Text: text})
	}
	return feedback
}

func cutPrefix(body, prefix string) (string, bool) {
	body = strings.TrimSpace(body)
	if len(body) < len(prefix) || !strings.EqualFold(body[:len(prefix)], prefix) {
		return "", false
	}
	rest := body[len(prefix):]
	// "!prd-makers" or "!prd-maker2" name someone else.
	if next, _ := utf8.DecodeRuneInString(rest); next == '-' || next == '_' || unicode.IsLetter(next) || unicode.IsDigit(next) {
		return "", false
	}
	return strings.TrimSpace(strings.TrimLeft(rest, " \t\n:,-")), true
}

// markAllDone replies "done" to every applied comment and marks it handled.
func (c feedbackChannel) markAllDone(ctx context.Context, key string, writer IssueWriter, feedback []Feedback, pageURL string, logger *log.Logger) error {
	for _, item := range feedback {
		if err := c.markDone(ctx, key, writer, item, pageURL); err != nil {
			logOperationFailure(logger, c.document, key, "mark feedback done", err, 0)
			return fmt.Errorf("mark feedback %s done: %w", item.Comment.ID, err)
		}
	}
	return nil
}

// markDone replies "done" to a feedback comment and then marks it handled.
// If marking fails the comment stays outstanding, so a retry may reply twice
// but never skips a comment.
func (c feedbackChannel) markDone(ctx context.Context, key string, writer IssueWriter, feedback Feedback, pageURL string) error {
	reply := jira.ReplyADF(feedback.Comment, "Done - applied in the "+c.document+":", "view "+c.document, pageURL)
	if _, err := writer.AddComment(ctx, key, reply); err != nil {
		return fmt.Errorf("reply: %w", err)
	}
	value, err := json.Marshal(feedbackDoneValue{Done: true, PageURL: pageURL})
	if err != nil {
		return fmt.Errorf("encode property: %w", err)
	}
	if err := writer.SetCommentProperty(ctx, feedback.Comment.ID, c.doneProperty, value); err != nil {
		return fmt.Errorf("set property: %w", err)
	}
	return nil
}

// skipRevision leaves an existing document untouched when no feedback is
// outstanding, tells the reviewer why, and moves the issue back to review.
func (c feedbackChannel) skipRevision(ctx context.Context, key string, writer IssueWriter, logger *log.Logger) error {
	if logger != nil {
		logger.Printf("%s unchanged, no outstanding feedback: issue=%s", c.document, key)
	}
	message := fmt.Sprintf("Nothing to revise: every %s comment is already done, so the %s is unchanged. Add a comment starting with %s and move the issue back to %s to request changes.", c.prefix, c.document, c.prefix, c.requestLabel)
	if _, err := writer.AddComment(ctx, key, jira.TextADF(message)); err != nil {
		logOperationFailure(logger, c.document, key, "comment", err, 0)
		return fmt.Errorf("comment: %w", err)
	}
	if err := writer.TransitionTo(ctx, key, string(c.reviewStatus)); err != nil {
		logOperationFailure(logger, c.document, key, "transition", err, 0)
		return fmt.Errorf("transition: %w", err)
	}
	return nil
}

// FormatFeedback lists feedback for an agent prompt, oldest first.
func FormatFeedback(feedback []Feedback) string {
	var output strings.Builder
	for index, item := range feedback {
		fmt.Fprintf(&output, "\n[Comment %d by %s at %s]\n%s\n", index+1, item.Comment.Author, item.Comment.Created, item.Text)
	}
	return output.String()
}
