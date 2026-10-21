package workflow

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tody-aja/jira-ai-agent/internal/jira"
	"github.com/tody-aja/jira-ai-agent/internal/repos"
)

// Statuses of the bank workflow, in board order. A Story is documented with
// an RFC and per-repository TRDs, implemented in every repository ticked in
// its description, then documented again with an FSD and a runbook.
const (
	BankBacklog          Status = "Back Log"
	BankRequestRFC       Status = "Request RFC"
	BankRFCReview        Status = "RFC Review"
	BankRequestTRD       Status = "Request TRD"
	BankTRDReview        Status = "TRD Review"
	BankReadyToImplement Status = "Ready To Implement"
	BankImplementing     Status = "Implementing"
	BankAIReview         Status = "AI Review"
	BankEngineerReview   Status = "Engineer Review"
	BankPRReview         Status = "PR Review"
	BankDevelopmentDone  Status = "Development Done"
	BankFSDRequest       Status = "FSD Request"
	BankFSDReview        Status = "FSD Review"
	BankRunbookRequest   Status = "Runbook Request"
	BankRunbookReview    Status = "Runbook Review"
	BankGoLive           Status = "Go Live"
	BankAIError          Status = "AI ERROR"
)

// bankAliases are earlier names of bank statuses that are still accepted.
var bankAliases = map[string]Status{
	"frd request": BankFSDRequest,
	"frd review":  BankFSDReview,
}

// Feedback prefixes of the bank documents; the TRD keeps the technical
// planner's prefix.
const (
	RFCFeedbackPrefix           = "!rfc-writer"
	RFCFeedbackDoneProperty     = "jira-ai-agent.rfc-writer"
	FSDFeedbackPrefix           = "!fsd-writer"
	FSDFeedbackDoneProperty     = "jira-ai-agent.fsd-writer"
	RunbookFeedbackPrefix       = "!runbook-writer"
	RunbookFeedbackDoneProperty = "jira-ai-agent.runbook-writer"
)

// RepoSelector resolves the repositories a ticket names.
type RepoSelector interface {
	Select(names []string) ([]repos.Repo, error)
	All() []repos.Repo
}

// DocumentPublisher files documents in Confluence.
type DocumentPublisher interface {
	PublishPageUnder(ctx context.Context, parents []string, title, markdown string) (PublishedPage, error)
	PageContent(ctx context.Context, title string) (content string, found bool, err error)
	AttachFile(ctx context.Context, pageID, path string) error
	AddLabel(ctx context.Context, pageID, label string) error
}

// PublishedPage is a page in Confluence.
type PublishedPage struct {
	ID  string
	URL string
}

// MergeRequestOpener opens a merge request for a repository's branch into
// target and returns its URL, reusing an open one.
type MergeRequestOpener interface {
	OpenMergeRequest(ctx context.Context, repo repos.Repo, source, target, title, description string) (string, error)
}

// BankDependencies are the collaborators only the bank workflow needs; it
// also uses the shared Reader, Writer, Engineer, Reviewer, MaxReviewCycles,
// JiraBaseURL and Logger.
type BankDependencies struct {
	Repos RepoSelector
	// RepoFieldID is the checkbox field listing the repositories a ticket
	// changes; empty, or empty on a ticket, falls back to the checklist in
	// the description.
	RepoFieldID string
	Documents DocumentWriter
	Planner   RepoPlanner
	// Workspace returns the worktree manager of a repository.
	Workspace     func(repos.Repo) Workspace
	Publisher     DocumentPublisher
	MergeRequests MergeRequestOpener
	// Now dates new documents; nil means time.Now.
	Now func() time.Time
}

func (b BankDependencies) now() time.Time {
	if b.Now == nil {
		return time.Now()
	}
	return b.Now()
}

// BankWorkflow is the bank's RFC > TRD > implementation > FSD > runbook
// workflow. Reviews and Go Live are people's steps.
type BankWorkflow struct{}

func (BankWorkflow) Step(name string) (Step, bool) {
	key := strings.ToLower(strings.TrimSpace(name))
	status, found := bankAliases[key]
	if !found {
		for _, candidate := range bankStatuses {
			if strings.ToLower(string(candidate)) == key {
				status, found = candidate, true
			}
		}
	}
	if !found {
		return Step{}, false
	}
	step, found := bankSteps[status]
	if !found {
		step = bankStep{run: noBankAction(status)}
	}
	return Step{Status: status, AI: step.ai, Run: step.run}, true
}

func (BankWorkflow) ErrorStatus() string {
	return string(BankAIError)
}

var bankStatuses = []Status{
	BankBacklog, BankRequestRFC, BankRFCReview, BankRequestTRD, BankTRDReview,
	BankReadyToImplement, BankImplementing, BankAIReview, BankEngineerReview,
	BankPRReview, BankDevelopmentDone, BankFSDRequest, BankFSDReview,
	BankRunbookRequest, BankRunbookReview, BankGoLive, BankAIError,
}

type bankAction = func(ctx context.Context, key string, deps Dependencies) error

type bankStep struct {
	run bankAction
	// ai is set when the action moves the issue on.
	ai bool
}

// bankSteps are the statuses with an action; every other status is a
// person's step. PR Review opens the merge requests but leaves the issue
// for the reviewers, so it does not move the issue on.
var bankSteps = map[Status]bankStep{
	BankRequestRFC:       {run: documentAction(rfcDocument), ai: true},
	BankRequestTRD:       {run: ExecuteRequestTRD, ai: true},
	BankReadyToImplement: {run: ExecuteBankReadyToImplement, ai: true},
	BankImplementing:     {run: ExecuteBankImplementing, ai: true},
	BankAIReview:         {run: ExecuteBankAIReview, ai: true},
	BankPRReview:         {run: ExecuteBankPRReview},
	BankFSDRequest:       {run: documentAction(fsdDocument), ai: true},
	BankRunbookRequest:   {run: documentAction(runbookDocument), ai: true},
}

func documentAction(kind documentKind) bankAction {
	return func(ctx context.Context, key string, deps Dependencies) error {
		return executeDocument(ctx, key, kind, deps)
	}
}

func noBankAction(status Status) bankAction {
	return func(_ context.Context, key string, deps Dependencies) error {
		if deps.Logger != nil {
			deps.Logger.Printf("No AI action for status %s: issue=%s", status, key)
		}
		return nil
	}
}

// tickedRepos returns the repositories ticked in the issue's repositories
// field, or else in the checklist in its description.
func tickedRepos(issue jira.Issue, bank BankDependencies) ([]repos.Repo, error) {
	selector := bank.Repos
	if selector == nil {
		return nil, fmt.Errorf("no repositories are configured (REPOS_ROOT)")
	}
	names := []string{}
	if bank.RepoFieldID != "" {
		checked, err := issue.CheckedOptions(bank.RepoFieldID)
		if err != nil {
			return nil, fmt.Errorf("read repositories field: %w", err)
		}
		names = checked
	}
	if len(names) == 0 {
		checked, err := jira.CheckedItems(issue.DescriptionADF)
		if err != nil {
			return nil, fmt.Errorf("read repository checklist: %w", err)
		}
		names = checked
	}
	if len(names) == 0 {
		all := []string{}
		for _, repo := range selector.All() {
			all = append(all, repo.Name)
		}
		return nil, fmt.Errorf("no repository is ticked; tick the repositories that change in the Repositories field, or in a checklist in the description (%s)", strings.Join(all, ", "))
	}
	return selector.Select(names)
}

func repoNames(list []repos.Repo) []string {
	names := make([]string, 0, len(list))
	for _, repo := range list {
		names = append(names, repo.Name)
	}
	return names
}
