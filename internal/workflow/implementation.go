package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/tody-aja/jira-ai-agent/internal/jira"
)

// ImplementationProperty is the issue property holding the state of the
// current implement-and-review cycle.
const ImplementationProperty = "jira-ai-agent.implementation"

// DefaultMaxReviewCycles bounds the AI review rounds when none is configured.
const DefaultMaxReviewCycles = 3

// Workspace is a checkout of the repository where one issue's branch is
// implemented.
type Workspace interface {
	// Prepare returns the directory checked out on branch.
	Prepare(ctx context.Context, branch string) (string, error)
	// CommitAll commits every change in dir and reports whether there was
	// anything to commit.
	CommitAll(ctx context.Context, dir, message string) (bool, error)
	// Diff returns the branch's changes against the base branch.
	Diff(ctx context.Context, dir string) (string, error)
}

type ImplementInput struct {
	IssueKey    string
	Summary     string
	Description string
	// PRD and TRD are the published documents in Confluence storage format.
	PRD string
	TRD string
	// Dir is the worktree to change, checked out on Branch.
	Dir    string
	Branch string
	// Review is the latest AI review to address; empty on the first round.
	Review string
}

// Engineer changes the code in a worktree and returns a Markdown summary
// of what it did. The workflow commits the changes.
type Engineer interface {
	Implement(context.Context, ImplementInput) (string, error)
}

type ReviewInput struct {
	IssueKey    string
	Summary     string
	Description string
	PRD         string
	TRD         string
	Dir         string
	Branch      string
	// Diff is the branch's changes against the base branch.
	Diff string
	// Round counts from 1 up to MaxRounds.
	Round     int
	MaxRounds int
	// PreviousReviews are the earlier reviews of this cycle, oldest first.
	PreviousReviews []string
}

type ReviewResult struct {
	Approved bool
	// Review is the Markdown review.
	Review string
}

// Reviewer reviews a branch against the PRD and TRD.
type Reviewer interface {
	Review(context.Context, ReviewInput) (ReviewResult, error)
}

// ImplementationBranch names the branch an issue is implemented on.
func ImplementationBranch(key string) string {
	return "ai/" + key
}

type implementationState struct {
	Branch  string         `json:"branch"`
	Reviews []reviewRecord `json:"reviews"`
}

type reviewRecord struct {
	Round     int    `json:"round"`
	Approved  bool   `json:"approved"`
	CommentID string `json:"commentId"`
}

// ExecuteReadyToImplement starts a new implement-and-review cycle and moves
// the issue to Implementing.
func ExecuteReadyToImplement(ctx context.Context, key string, writer IssueWriter, maxReviews int, logger *log.Logger) error {
	branch := ImplementationBranch(key)
	if err := saveImplementationState(ctx, writer, key, implementationState{Branch: branch, Reviews: []reviewRecord{}}); err != nil {
		logOperationFailure(logger, "Implementation", key, "save state", err, 0)
		return err
	}
	message := fmt.Sprintf("Implementation started on branch %s. The AI engineer and reviewer will iterate for up to %d review rounds.", branch, maxReviews)
	if _, err := writer.AddComment(ctx, key, jira.TextADF(message)); err != nil {
		logOperationFailure(logger, "Implementation", key, "comment", err, 0)
		return fmt.Errorf("comment: %w", err)
	}
	if err := writer.TransitionTo(ctx, key, string(StatusImplementing)); err != nil {
		logOperationFailure(logger, "Implementation", key, "transition", err, 0)
		return fmt.Errorf("transition: %w", err)
	}
	return nil
}

// ExecuteImplementing has the engineer implement the TRD, or address the
// latest review, in the issue's worktree, commits the result and moves the
// issue to AI Review.
func ExecuteImplementing(ctx context.Context, key string, deps Dependencies) error {
	logger := deps.Logger
	documents, err := loadImplementationDocuments(ctx, key, deps)
	if err != nil {
		logOperationFailure(logger, "Implementation", key, "load documents", err, 0)
		return err
	}
	state, err := loadImplementationState(ctx, deps.Reader, key)
	if err != nil {
		logOperationFailure(logger, "Implementation", key, "load state", err, 0)
		return err
	}

	review := ""
	if last := len(state.Reviews) - 1; last >= 0 && !state.Reviews[last].Approved {
		texts, err := reviewTexts(ctx, deps.Reader, key, state.Reviews[last:])
		if err != nil {
			logOperationFailure(logger, "Implementation", key, "get review", err, 0)
			return err
		}
		review = texts[0]
	}

	dir, err := deps.Workspace.Prepare(ctx, state.Branch)
	if err != nil {
		logOperationFailure(logger, "Implementation", key, "prepare worktree", err, 0)
		return fmt.Errorf("prepare worktree: %w", err)
	}

	round := len(state.Reviews) + 1
	startedAt := time.Now()
	if logger != nil {
		logger.Printf("Implementation started: issue=%s round=%d branch=%s dir=%s", key, round, state.Branch, dir)
	}
	summary, err := deps.Engineer.Implement(ctx, ImplementInput{
		IssueKey:    documents.issue.Key,
		Summary:     documents.issue.Summary,
		Description: documents.issue.Description,
		PRD:         documents.prd,
		TRD:         documents.trd,
		Dir:         dir,
		Branch:      state.Branch,
		Review:      review,
	})
	if err != nil {
		logOperationFailure(logger, "Implementation", key, "implement", err, time.Since(startedAt))
		return fmt.Errorf("implement: %w", err)
	}
	if logger != nil {
		logger.Printf("Implementation completed: issue=%s round=%d duration=%s", key, round, time.Since(startedAt))
	}

	message := fmt.Sprintf("%s: implement %s", key, documents.issue.Summary)
	if review != "" {
		message = fmt.Sprintf("%s: address AI review round %d", key, round-1)
	}
	committed, err := deps.Workspace.CommitAll(ctx, dir, message)
	if err != nil {
		logOperationFailure(logger, "Implementation", key, "commit", err, 0)
		return fmt.Errorf("commit: %w", err)
	}
	header := fmt.Sprintf("# Implementation round %d\n\nCommitted to branch %s.", round, state.Branch)
	if !committed {
		header = fmt.Sprintf("# Implementation round %d\n\nNo code changes this round.", round)
	}
	if _, err := deps.Writer.AddComment(ctx, key, jira.MarkdownADF(header+"\n\n"+summary)); err != nil {
		logOperationFailure(logger, "Implementation", key, "comment", err, 0)
		return fmt.Errorf("comment: %w", err)
	}

	if err := deps.Writer.TransitionTo(ctx, key, string(StatusAIReview)); err != nil {
		logOperationFailure(logger, "Implementation", key, "transition", err, 0)
		return fmt.Errorf("transition: %w", err)
	}
	return nil
}

// ExecuteAIReview has the reviewer review the branch, posts the review, and
// moves the issue back to Implementing when changes are requested and rounds
// remain, or to PR Ready when the branch is approved or the last round is
// done.
func ExecuteAIReview(ctx context.Context, key string, deps Dependencies) error {
	logger := deps.Logger
	maxRounds := deps.MaxReviewCycles
	if maxRounds <= 0 {
		maxRounds = DefaultMaxReviewCycles
	}
	documents, err := loadImplementationDocuments(ctx, key, deps)
	if err != nil {
		logOperationFailure(logger, "Review", key, "load documents", err, 0)
		return err
	}
	state, err := loadImplementationState(ctx, deps.Reader, key)
	if err != nil {
		logOperationFailure(logger, "Review", key, "load state", err, 0)
		return err
	}
	previous, err := reviewTexts(ctx, deps.Reader, key, state.Reviews)
	if err != nil {
		logOperationFailure(logger, "Review", key, "get reviews", err, 0)
		return err
	}

	dir, err := deps.Workspace.Prepare(ctx, state.Branch)
	if err != nil {
		logOperationFailure(logger, "Review", key, "prepare worktree", err, 0)
		return fmt.Errorf("prepare worktree: %w", err)
	}
	diff, err := deps.Workspace.Diff(ctx, dir)
	if err != nil {
		logOperationFailure(logger, "Review", key, "diff", err, 0)
		return fmt.Errorf("diff: %w", err)
	}

	round := len(state.Reviews) + 1
	startedAt := time.Now()
	if logger != nil {
		logger.Printf("Review started: issue=%s round=%d/%d", key, round, maxRounds)
	}
	result, err := deps.Reviewer.Review(ctx, ReviewInput{
		IssueKey:        documents.issue.Key,
		Summary:         documents.issue.Summary,
		Description:     documents.issue.Description,
		PRD:             documents.prd,
		TRD:             documents.trd,
		Dir:             dir,
		Branch:          state.Branch,
		Diff:            diff,
		Round:           round,
		MaxRounds:       maxRounds,
		PreviousReviews: previous,
	})
	if err != nil {
		logOperationFailure(logger, "Review", key, "review", err, time.Since(startedAt))
		return fmt.Errorf("review: %w", err)
	}

	next, outcome := StatusImplementing, "changes requested - the AI engineer will address them"
	switch {
	case result.Approved:
		next, outcome = StatusPRReady, "approved - moving to PR Ready"
	case round >= maxRounds:
		next, outcome = StatusPRReady, "changes requested, but this was the last review round - moving to PR Ready with these findings open"
	}
	if logger != nil {
		logger.Printf("Review completed: issue=%s round=%d/%d approved=%t next=%s duration=%s", key, round, maxRounds, result.Approved, next, time.Since(startedAt))
	}

	header := fmt.Sprintf("# AI review round %d of %d\n\n%s.", round, maxRounds, outcome)
	commentID, err := deps.Writer.AddComment(ctx, key, jira.MarkdownADF(header+"\n\n"+result.Review))
	if err != nil {
		logOperationFailure(logger, "Review", key, "comment", err, 0)
		return fmt.Errorf("comment: %w", err)
	}
	state.Reviews = append(state.Reviews, reviewRecord{Round: round, Approved: result.Approved, CommentID: commentID})
	if err := saveImplementationState(ctx, deps.Writer, key, state); err != nil {
		logOperationFailure(logger, "Review", key, "save state", err, 0)
		return err
	}

	if err := deps.Writer.TransitionTo(ctx, key, string(next)); err != nil {
		logOperationFailure(logger, "Review", key, "transition", err, 0)
		return fmt.Errorf("transition: %w", err)
	}
	return nil
}

type implementationDocuments struct {
	issue jira.Issue
	prd   string
	trd   string
}

// loadImplementationDocuments reads the issue and its published PRD and TRD,
// which both must exist.
func loadImplementationDocuments(ctx context.Context, key string, deps Dependencies) (implementationDocuments, error) {
	issue, err := deps.Reader.GetIssue(ctx, key)
	if err != nil {
		return implementationDocuments{}, fmt.Errorf("get issue: %w", err)
	}
	documents := implementationDocuments{issue: issue}
	for _, document := range []struct {
		title  string
		target *string
	}{
		{PRDPageTitle(PRDInput{IssueKey: issue.Key, Summary: issue.Summary}), &documents.prd},
		{TRDPageTitle(TRDInput{IssueKey: issue.Key, Summary: issue.Summary}), &documents.trd},
	} {
		content, found, err := deps.Publisher.PageContent(ctx, document.title)
		if err != nil {
			return implementationDocuments{}, fmt.Errorf("get page %q: %w", document.title, err)
		}
		if !found {
			return implementationDocuments{}, fmt.Errorf("no page titled %q", document.title)
		}
		*document.target = content
	}
	return documents, nil
}

// loadImplementationState returns the current cycle, or a new one when the
// issue reached Implementing or AI Review without Ready to Implement.
func loadImplementationState(ctx context.Context, reader IssueReader, key string) (implementationState, error) {
	state := implementationState{Branch: ImplementationBranch(key), Reviews: []reviewRecord{}}
	raw, found, err := reader.GetIssueProperty(ctx, key, ImplementationProperty)
	if err != nil {
		return state, fmt.Errorf("get state: %w", err)
	}
	if !found {
		return state, nil
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return state, fmt.Errorf("decode state: %w", err)
	}
	if state.Branch == "" {
		state.Branch = ImplementationBranch(key)
	}
	return state, nil
}

func saveImplementationState(ctx context.Context, writer IssueWriter, key string, state implementationState) error {
	value, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	if err := writer.SetIssueProperty(ctx, key, ImplementationProperty, value); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	return nil
}

// reviewTexts returns the text of the given reviews' comments, in order.
func reviewTexts(ctx context.Context, reader IssueReader, key string, reviews []reviewRecord) ([]string, error) {
	if len(reviews) == 0 {
		return []string{}, nil
	}
	comments, err := reader.GetComments(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("get comments: %w", err)
	}
	byID := map[string]string{}
	for _, comment := range comments {
		byID[comment.ID] = comment.Body
	}
	texts := make([]string, 0, len(reviews))
	for _, review := range reviews {
		text, ok := byID[review.CommentID]
		if !ok {
			return nil, fmt.Errorf("review comment %s for round %d not found", review.CommentID, review.Round)
		}
		texts = append(texts, text)
	}
	return texts, nil
}
