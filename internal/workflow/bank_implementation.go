package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/tody-aja/jira-ai-agent/internal/jira"
	"github.com/tody-aja/jira-ai-agent/internal/repos"
)

// BankImplementationProperty is the issue property holding each
// repository's implement-and-review state.
const BankImplementationProperty = "jira-ai-agent.bank-implementation"

// bankRepoContext is what the engineer and reviewer are told about working
// in one repository of a bank issue.
const bankRepoContext = `This issue changes these repositories, in merge order: %s. You are working in %s only; the overview TRD describes how the repositories fit together.

The "PRD" above is the approved RFC for the whole change, and the "TRD" is the overview followed by the TRD for %s.

The application cannot run locally, so verify the change with unit tests only - never start the application or call other systems. In a multi-module Maven project, run only the modules you changed by appending -pl <module path> -am to the test command. Every class or function you add or change must be covered by unit tests. If the repository has no unit tests yet, add the test setup its build tool expects (for Maven, JUnit tests under src/test/java and the test dependencies in pom.xml).`

// bankSQLContext gives the engineer and reviewer the approved SQL migration
// plan, which the workflow applied on the development database before
// implementation started.
const bankSQLContext = `The approved SQL migration plan below is already applied on the development database. The code must match it exactly: table and column names, types, keys and the rows it inserts. Never change the database and never write SQL scripts into the repository; table definitions in the code (such as tables/*.tables.xml) are code and must agree with the plan's DDL. If the code needs something the plan does not have, do not invent it - report it under Deviations from the TRD so the plan is revised.

Approved SQL migration plan (Confluence storage format):
%s`

type repoImplementation struct {
	Repo       string         `json:"repo"`
	Branch     string         `json:"branch"`
	BaseBranch string         `json:"baseBranch"`
	Reviews    []reviewRecord `json:"reviews"`
	// Implemented is set once the engineer has run; Changed once the branch
	// has commits, and NoChanges when review found nothing to review.
	Implemented  bool   `json:"implemented"`
	Changed      bool   `json:"changed"`
	NoChanges    bool   `json:"noChanges"`
	Pushed       bool   `json:"pushed"`
	MergeRequest string `json:"mergeRequest,omitempty"`
	// Error is the last failure in this repository, shown on the issue.
	Error string `json:"error,omitempty"`
}

func (r repoImplementation) approved() bool {
	last := len(r.Reviews) - 1
	return r.NoChanges || (last >= 0 && r.Reviews[last].Approved)
}

// needsWork reports whether the engineer has more to do in the repository.
func (r repoImplementation) needsWork(maxRounds int) bool {
	return !r.approved() && len(r.Reviews) < maxRounds
}

type bankImplementation struct {
	Repos []repoImplementation `json:"repos"`
}

// ExecuteBankReadyToImplement applies the approved SQL migration plan on the
// development database, starts a new implement-and-review cycle for every
// ticked repository and moves the issue to Implementing.
func ExecuteBankReadyToImplement(ctx context.Context, key string, deps Dependencies) error {
	fail := failer(deps, "Implementation", key)
	issue, err := deps.Reader.GetIssue(ctx, key)
	if err != nil {
		return fail("get issue", err)
	}
	ticked, err := tickedRepos(issue, deps.Bank)
	if err != nil {
		return fail("select repositories", err)
	}
	documents, err := loadDocumentsState(ctx, deps.Reader, key)
	if err != nil {
		return fail("load documents", err)
	}
	if err := applySQLPlan(ctx, key, deps, documents); err != nil {
		return fail("apply SQL plan", err)
	}
	branch := bankBranch(issue, documents, deps.Bank.BranchPrefix)
	state := bankImplementation{}
	for _, repo := range ticked {
		state.Repos = append(state.Repos, newRepoImplementation(branch, repo))
	}
	if err := saveBankImplementation(ctx, deps.Writer, key, state); err != nil {
		return fail("save state", err)
	}
	if err := updateRepositoryStatus(ctx, issue, deps, state); err != nil {
		return fail("update description", err)
	}
	message := fmt.Sprintf("Implementation started on branch %s in %s. The AI engineer and reviewer will iterate for up to %d review rounds per repository.", branch, strings.Join(repoNames(ticked), ", "), maxReviewRounds(deps))
	if _, err := deps.Writer.AddComment(ctx, key, jira.TextADF(message)); err != nil {
		return fail("comment", err)
	}
	if err := deps.Writer.TransitionTo(ctx, key, string(BankImplementing)); err != nil {
		return fail("transition", err)
	}
	return nil
}

func newRepoImplementation(branch string, repo repos.Repo) repoImplementation {
	return repoImplementation{Repo: repo.Name, Branch: branch, BaseBranch: repo.BaseBranch, Reviews: []reviewRecord{}}
}

// ticketLine is the description line naming the ticket a branch is for,
// e.g. "Ticket: CORE-312".
var ticketLine = regexp.MustCompile(`(?im)^[ \t]*ticket[ \t]*:[ \t]*([a-z][a-z0-9]*-[0-9]+)\b`)

// maxBranchSlug bounds the what-changes part of a branch name.
const maxBranchSlug = 50

// bankBranch names an issue's implementation branch
// <prefix>/<ticket>-<what changes>, e.g. tody/feature/core-312-bulk-overpayment-withdraw.
// The ticket comes from the description's "Ticket:" line, else the issue
// key; what changes is the TRD planner's branch name, else the summary.
func bankBranch(issue jira.Issue, documents documentsState, prefix string) string {
	if prefix = strings.Trim(strings.TrimSpace(prefix), "/"); prefix == "" {
		prefix = "feature"
	}
	ticket := issue.Key
	if match := ticketLine.FindStringSubmatch(issue.Description); match != nil {
		ticket = match[1]
	}
	slug := ""
	if documents.TRD != nil {
		slug = branchSlug(documents.TRD.BranchName)
	}
	if slug == "" {
		slug = branchSlug(issue.Summary)
	}
	branch := prefix + "/" + strings.ToLower(ticket)
	if slug != "" {
		branch += "-" + slug
	}
	return branch
}

// branchSlug turns text into lowercase words joined by hyphens, cut at a
// word to at most maxBranchSlug characters.
func branchSlug(text string) string {
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9')
	})
	slug := ""
	for _, word := range words {
		next := word
		if slug != "" {
			next = slug + "-" + word
		}
		if len(next) > maxBranchSlug {
			break
		}
		slug = next
	}
	return slug
}

type repoOutcome struct {
	summary   string
	committed bool
	err       error
}

// ExecuteBankImplementing has the engineer implement each repository's TRD,
// or address its latest review, in parallel, commits each repository's
// changes and moves the issue to AI Review. Repositories already approved
// are left alone. When any repository fails the issue goes to AI ERROR,
// keeping what the others committed.
func ExecuteBankImplementing(ctx context.Context, key string, deps Dependencies) error {
	fail := failer(deps, "Implementation", key)
	work, err := loadBankWork(ctx, key, deps)
	if err != nil {
		return fail("load", err)
	}
	maxRounds := maxReviewRounds(deps)
	targets := []int{}
	for index, repo := range work.state.Repos {
		if repo.needsWork(maxRounds) {
			targets = append(targets, index)
		}
	}

	outcomes := make([]repoOutcome, len(targets))
	runParallel(len(targets), func(slot int) {
		outcomes[slot] = implementRepo(ctx, key, deps, work, &work.state.Repos[targets[slot]])
	})

	report := []string{}
	failed := []string{}
	for slot, index := range targets {
		repo := &work.state.Repos[index]
		outcome := outcomes[slot]
		round := len(repo.Reviews) + 1
		if outcome.err != nil {
			repo.Error = outcome.err.Error()
			failed = append(failed, repo.Repo)
			report = append(report, fmt.Sprintf("## %s - round %d failed\n\n%s", repo.Repo, round, repo.Error))
			continue
		}
		repo.Error = ""
		repo.Implemented = true
		repo.Changed = repo.Changed || outcome.committed
		status := fmt.Sprintf("Committed to branch %s.", repo.Branch)
		if !outcome.committed {
			status = "No code changes this round."
		}
		report = append(report, fmt.Sprintf("## %s - round %d\n\n%s\n\n%s", repo.Repo, round, status, outcome.summary))
	}
	if err := saveBankImplementation(ctx, deps.Writer, key, work.state); err != nil {
		return fail("save state", err)
	}
	if err := updateRepositoryStatus(ctx, work.issue, deps, work.state); err != nil {
		return fail("update description", err)
	}
	if len(report) > 0 {
		if _, err := deps.Writer.AddComment(ctx, key, jira.MarkdownADF("# Implementation\n\n"+strings.Join(report, "\n\n"))); err != nil {
			return fail("comment", err)
		}
	}
	if len(failed) > 0 {
		return fail("implement", fmt.Errorf("implementation failed in %s", strings.Join(failed, ", ")))
	}
	if err := deps.Writer.TransitionTo(ctx, key, string(BankAIReview)); err != nil {
		return fail("transition", err)
	}
	return nil
}

func implementRepo(ctx context.Context, key string, deps Dependencies, work bankWork, state *repoImplementation) repoOutcome {
	repo, workspace, err := work.repoWorkspace(state.Repo, deps)
	if err != nil {
		return repoOutcome{err: err}
	}
	review := ""
	if last := len(state.Reviews) - 1; last >= 0 && !state.Reviews[last].Approved {
		texts, err := reviewTexts(ctx, deps.Reader, key, state.Reviews[last:])
		if err != nil {
			return repoOutcome{err: fmt.Errorf("get review: %w", err)}
		}
		review = texts[0]
	}
	dir, err := workspace.Prepare(ctx, state.Branch)
	if err != nil {
		return repoOutcome{err: fmt.Errorf("prepare worktree: %w", err)}
	}

	round := len(state.Reviews) + 1
	startedAt := time.Now()
	if deps.Logger != nil {
		deps.Logger.Printf("Implementation started: issue=%s repo=%s round=%d branch=%s dir=%s", key, repo.Name, round, state.Branch, dir)
	}
	summary, err := deps.Engineer.Implement(ctx, ImplementInput{
		IssueKey:    work.issue.Key,
		Summary:     work.issue.Summary,
		Description: work.issue.Description,
		PRD:         work.rfc,
		TRD:         work.trdFor(repo.Name),
		Dir:         dir,
		Branch:      state.Branch,
		Review:      review,
		Context:     work.context(repo.Name),
		// The database may be read but never changed: the approved SQL plan
		// is already applied.
		ReadDatabase: true,
	})
	if err != nil {
		logOperationFailure(deps.Logger, "Implementation", key, "implement "+repo.Name, err, time.Since(startedAt))
		return repoOutcome{err: fmt.Errorf("implement: %w", err)}
	}
	if deps.Logger != nil {
		deps.Logger.Printf("Implementation completed: issue=%s repo=%s round=%d duration=%s", key, repo.Name, round, time.Since(startedAt))
	}
	message := fmt.Sprintf("%s: implement %s", key, work.issue.Summary)
	if review != "" {
		message = fmt.Sprintf("%s: address AI review round %d", key, round-1)
	}
	committed, err := workspace.CommitAll(ctx, dir, message)
	if err != nil {
		return repoOutcome{err: fmt.Errorf("commit: %w", err)}
	}
	return repoOutcome{summary: summary, committed: committed}
}

type reviewOutcome struct {
	result    ReviewResult
	noChanges bool
	err       error
}

// ExecuteBankAIReview reviews every repository still under review, in
// parallel, and posts one review per repository. When a repository has
// changes requested and rounds left the issue goes back to Implementing;
// otherwise every changed branch is pushed - only when GitLab is configured,
// so nothing leaves the machine otherwise - and the issue moves to Engineer
// Review.
func ExecuteBankAIReview(ctx context.Context, key string, deps Dependencies) error {
	fail := failer(deps, "Review", key)
	work, err := loadBankWork(ctx, key, deps)
	if err != nil {
		return fail("load", err)
	}
	maxRounds := maxReviewRounds(deps)
	targets := []int{}
	for index, repo := range work.state.Repos {
		if repo.needsWork(maxRounds) {
			targets = append(targets, index)
		}
	}

	outcomes := make([]reviewOutcome, len(targets))
	runParallel(len(targets), func(slot int) {
		outcomes[slot] = reviewRepo(ctx, key, deps, work, work.state.Repos[targets[slot]], maxRounds)
	})

	failed := []string{}
	for slot, index := range targets {
		repo := &work.state.Repos[index]
		outcome := outcomes[slot]
		if outcome.err != nil {
			repo.Error = outcome.err.Error()
			failed = append(failed, repo.Repo)
			continue
		}
		repo.Error = ""
		if outcome.noChanges {
			repo.NoChanges = true
			continue
		}
		round := len(repo.Reviews) + 1
		verdict := "changes requested - the AI engineer will address them"
		switch {
		case outcome.result.Approved:
			verdict = "approved"
		case round >= maxRounds:
			verdict = "changes requested, but this was the last review round - the findings stay open for the engineer review"
		}
		header := fmt.Sprintf("# AI review of %s, round %d of %d\n\n%s.", repo.Repo, round, maxRounds, verdict)
		commentID, err := deps.Writer.AddComment(ctx, key, jira.MarkdownADF(header+"\n\n"+outcome.result.Review))
		if err != nil {
			return fail("comment", err)
		}
		repo.Reviews = append(repo.Reviews, reviewRecord{Round: round, Approved: outcome.result.Approved, CommentID: commentID})
	}

	next := BankEngineerReview
	for _, repo := range work.state.Repos {
		if repo.needsWork(maxRounds) {
			next = BankImplementing
		}
	}
	if len(failed) == 0 && next == BankEngineerReview {
		for index := range work.state.Repos {
			repo := &work.state.Repos[index]
			if !repo.Changed || repo.NoChanges || repo.Pushed {
				continue
			}
			_, workspace, err := work.repoWorkspace(repo.Repo, deps)
			if err == nil && deps.Bank.MergeRequests != nil {
				if err = workspace.Push(ctx, repo.Branch); err == nil {
					repo.Pushed = true
				}
			}
			if err != nil {
				repo.Error = "push: " + err.Error()
				failed = append(failed, repo.Repo)
				continue
			}
			// The agents are done with the worktree; a leftover one is only
			// clutter, so failing to remove it does not fail the review.
			if err := workspace.Remove(ctx, repo.Branch); err != nil {
				logOperationFailure(deps.Logger, "Review", key, "remove worktree "+repo.Repo, err, 0)
			}
		}
	}
	if err := saveBankImplementation(ctx, deps.Writer, key, work.state); err != nil {
		return fail("save state", err)
	}
	if err := updateRepositoryStatus(ctx, work.issue, deps, work.state); err != nil {
		return fail("update description", err)
	}
	if len(failed) > 0 {
		return fail("review", fmt.Errorf("review failed in %s", strings.Join(failed, ", ")))
	}
	if deps.Logger != nil {
		deps.Logger.Printf("Review completed: issue=%s repos=%d next=%s", key, len(targets), next)
	}
	if err := deps.Writer.TransitionTo(ctx, key, string(next)); err != nil {
		return fail("transition", err)
	}
	return nil
}

func reviewRepo(ctx context.Context, key string, deps Dependencies, work bankWork, state repoImplementation, maxRounds int) reviewOutcome {
	repo, workspace, err := work.repoWorkspace(state.Repo, deps)
	if err != nil {
		return reviewOutcome{err: err}
	}
	previous, err := reviewTexts(ctx, deps.Reader, key, state.Reviews)
	if err != nil {
		return reviewOutcome{err: fmt.Errorf("get reviews: %w", err)}
	}
	dir, err := workspace.Prepare(ctx, state.Branch)
	if err != nil {
		return reviewOutcome{err: fmt.Errorf("prepare worktree: %w", err)}
	}
	diff, err := workspace.Diff(ctx, dir)
	if err != nil {
		return reviewOutcome{err: fmt.Errorf("diff: %w", err)}
	}
	if strings.TrimSpace(diff) == "" {
		// The TRD found nothing to change here; there is nothing to review
		// and no merge request to open.
		return reviewOutcome{noChanges: true}
	}
	round := len(state.Reviews) + 1
	startedAt := time.Now()
	if deps.Logger != nil {
		deps.Logger.Printf("Review started: issue=%s repo=%s round=%d/%d", key, repo.Name, round, maxRounds)
	}
	result, err := deps.Reviewer.Review(ctx, ReviewInput{
		IssueKey:        work.issue.Key,
		Summary:         work.issue.Summary,
		Description:     work.issue.Description,
		PRD:             work.rfc,
		TRD:             work.trdFor(repo.Name),
		Dir:             dir,
		Branch:          state.Branch,
		Diff:            diff,
		Round:           round,
		MaxRounds:       maxRounds,
		PreviousReviews: previous,
		Context:         work.context(repo.Name),
	})
	if err != nil {
		logOperationFailure(deps.Logger, "Review", key, "review "+repo.Name, err, time.Since(startedAt))
		return reviewOutcome{err: fmt.Errorf("review: %w", err)}
	}
	if deps.Logger != nil {
		deps.Logger.Printf("Review completed: issue=%s repo=%s round=%d/%d approved=%t duration=%s", key, repo.Name, round, maxRounds, result.Approved, time.Since(startedAt))
	}
	return reviewOutcome{result: result}
}

// ExecuteBankPRReview pushes every changed repository's branch and opens a
// merge request for it, then lists them on the issue. The issue stays in PR
// Review for the reviewers. Repositories that already have a merge request
// are left alone, so a restarted server does not open another.
func ExecuteBankPRReview(ctx context.Context, key string, deps Dependencies) error {
	fail := failer(deps, "Merge request", key)
	if deps.Bank.MergeRequests == nil {
		if deps.Logger != nil {
			deps.Logger.Printf("GitLab is not configured, so no merge request was opened: issue=%s", key)
		}
		return nil
	}
	work, err := loadBankWork(ctx, key, deps)
	if err != nil {
		return fail("load", err)
	}
	opened, failed := []string{}, []string{}
	for index := range work.state.Repos {
		repo := &work.state.Repos[index]
		if !repo.Changed || repo.NoChanges || repo.MergeRequest != "" {
			continue
		}
		registered, workspace, err := work.repoWorkspace(repo.Repo, deps)
		if err == nil && !repo.Pushed {
			err = workspace.Push(ctx, repo.Branch)
		}
		if err != nil {
			repo.Error = "push: " + err.Error()
			failed = append(failed, repo.Repo)
			continue
		}
		repo.Pushed = true
		title := fmt.Sprintf("%s [%s]: %s", work.issue.Key, repo.Repo, work.issue.Summary)
		url, err := deps.Bank.MergeRequests.OpenMergeRequest(ctx, registered, repo.Branch, repo.BaseBranch, title, mergeRequestBody(work, *repo, deps))
		if err != nil {
			repo.Error = "merge request: " + err.Error()
			failed = append(failed, repo.Repo)
			continue
		}
		repo.Error = ""
		repo.MergeRequest = url
		opened = append(opened, fmt.Sprintf("%s: %s", repo.Repo, url))
		if deps.Logger != nil {
			deps.Logger.Printf("Merge request opened: issue=%s repo=%s url=%s", key, repo.Repo, url)
		}
	}
	if err := saveBankImplementation(ctx, deps.Writer, key, work.state); err != nil {
		return fail("save state", err)
	}
	if err := updateRepositoryStatus(ctx, work.issue, deps, work.state); err != nil {
		return fail("update description", err)
	}
	if len(opened) > 0 {
		message := "Merge requests opened, in merge order:\n" + strings.Join(opened, "\n")
		if _, err := deps.Writer.AddComment(ctx, key, jira.TextADF(message)); err != nil {
			return fail("comment", err)
		}
	}
	if len(failed) > 0 {
		return fail("open merge requests", fmt.Errorf("could not open a merge request for %s", strings.Join(failed, ", ")))
	}
	return nil
}

func mergeRequestBody(work bankWork, repo repoImplementation, deps Dependencies) string {
	reference := work.issue.Key
	if base := strings.TrimRight(strings.TrimSpace(deps.JiraBaseURL), "/"); base != "" {
		reference = fmt.Sprintf("[%s](%s/browse/%s)", work.issue.Key, base, work.issue.Key)
	}
	lines := []string{fmt.Sprintf("Implements the %s part of %s: %s", repo.Repo, reference, work.issue.Summary), ""}
	if len(work.mergeOrder) > 1 {
		lines = append(lines, "Merge order: "+strings.Join(work.mergeOrder, " -> "), "")
	}
	if last := len(repo.Reviews) - 1; last >= 0 {
		if repo.Reviews[last].Approved {
			lines = append(lines, fmt.Sprintf("The AI reviewer approved this branch in round %d.", repo.Reviews[last].Round))
		} else {
			lines = append(lines, fmt.Sprintf("**The AI review rounds ran out with findings still open** (round %d). See the last review on the Jira issue before merging.", repo.Reviews[last].Round))
		}
		lines = append(lines, "")
	}
	lines = append(lines, "The RFC, TRDs and every implementation and review round are on the Jira issue.")
	return strings.Join(lines, "\n")
}

// bankWork is what the implementation steps read before working.
type bankWork struct {
	issue      jira.Issue
	state      bankImplementation
	rfc        string
	overview   string
	repoTRDs   map[string]string
	mergeOrder []string
	// sqlPlan is the approved SQL migration plan page, empty without one.
	sqlPlan string
	repos   map[string]repos.Repo
}

// loadBankWork reads the issue, its documents and its implementation
// state, adding a state for every ticked repository that has none.
func loadBankWork(ctx context.Context, key string, deps Dependencies) (bankWork, error) {
	work := bankWork{repoTRDs: map[string]string{}, repos: map[string]repos.Repo{}}
	issue, err := deps.Reader.GetIssue(ctx, key)
	if err != nil {
		return work, fmt.Errorf("get issue: %w", err)
	}
	work.issue = issue
	ticked, err := tickedRepos(issue, deps.Bank)
	if err != nil {
		return work, err
	}
	documents, err := loadDocumentsState(ctx, deps.Reader, key)
	if err != nil {
		return work, err
	}
	if documents.RFC == nil || documents.TRD == nil {
		return work, fmt.Errorf("the issue needs a published RFC and TRD before implementation")
	}
	if work.rfc, err = pageContent(ctx, deps.Bank.Publisher, documents.RFC.Title); err != nil {
		return work, err
	}
	if work.overview, err = pageContent(ctx, deps.Bank.Publisher, documents.TRD.Overview.Title); err != nil {
		return work, err
	}
	for _, repo := range documents.TRD.Repos {
		if work.repoTRDs[strings.ToLower(repo.Repo)], err = pageContent(ctx, deps.Bank.Publisher, repo.Title); err != nil {
			return work, err
		}
	}
	work.mergeOrder = documents.TRD.MergeOrder
	if documents.SQL != nil {
		if work.sqlPlan, err = pageContent(ctx, deps.Bank.Publisher, documents.SQL.Title); err != nil {
			return work, err
		}
	}

	if work.state, err = loadBankImplementation(ctx, deps.Reader, key); err != nil {
		return work, err
	}
	known := map[string]bool{}
	for _, repo := range work.state.Repos {
		known[strings.ToLower(repo.Repo)] = true
	}
	for _, repo := range ticked {
		work.repos[strings.ToLower(repo.Name)] = repo
		if !known[strings.ToLower(repo.Name)] {
			work.state.Repos = append(work.state.Repos, newRepoImplementation(key, repo))
		}
	}
	for _, repo := range work.state.Repos {
		if _, found := work.repos[strings.ToLower(repo.Repo)]; !found {
			selected, err := deps.Bank.Repos.Select([]string{repo.Repo})
			if err != nil {
				return work, err
			}
			work.repos[strings.ToLower(repo.Repo)] = selected[0]
		}
	}
	return work, nil
}

func (w bankWork) repoWorkspace(name string, deps Dependencies) (repos.Repo, Workspace, error) {
	repo, found := w.repos[strings.ToLower(name)]
	if !found {
		return repos.Repo{}, nil, fmt.Errorf("repository %s is not configured", name)
	}
	if deps.Bank.Workspace == nil {
		return repos.Repo{}, nil, fmt.Errorf("no workspaces are configured")
	}
	return repo, deps.Bank.Workspace(repo), nil
}

// trdFor is the overview TRD followed by the repository's own TRD.
func (w bankWork) trdFor(repo string) string {
	plan, found := w.repoTRDs[strings.ToLower(repo)]
	if !found {
		plan = "(no TRD was written for " + repo + "; follow the overview)"
	}
	return "Overview TRD:\n" + w.overview + "\n\nTRD for " + repo + ":\n" + plan
}

func (w bankWork) context(repo string) string {
	order := w.mergeOrder
	if len(order) == 0 {
		for _, state := range w.state.Repos {
			order = append(order, state.Repo)
		}
	}
	notes := fmt.Sprintf(bankRepoContext, strings.Join(order, " -> "), repo, repo)
	if strings.TrimSpace(w.sqlPlan) != "" {
		notes += "\n\n" + fmt.Sprintf(bankSQLContext, w.sqlPlan)
	}
	return notes
}

// updateRepositoryStatus rewrites the repository status table at the end of
// the issue's description.
func updateRepositoryStatus(ctx context.Context, issue jira.Issue, deps Dependencies, state bankImplementation) error {
	documents, err := loadDocumentsState(ctx, deps.Reader, issue.Key)
	if err != nil {
		return err
	}
	maxRounds := maxReviewRounds(deps)
	rows := [][]string{{"Repository", "Branch", "Implementation", "AI review", "Merge request"}}
	for _, repo := range orderedRepos(state, documents) {
		rows = append(rows, []string{repo.Repo, repo.Branch, implementationCell(repo), reviewCell(repo, maxRounds), mergeRequestCell(repo)})
	}
	note := "Updated by the Jira AI Agent."
	if documents.TRD != nil && len(documents.TRD.MergeOrder) > 1 {
		note = "Merge order: " + strings.Join(documents.TRD.MergeOrder, " -> ") + ". " + note
	}
	description, err := jira.SetRepositoryStatus(issue.DescriptionADF, rows, note)
	if err != nil {
		return err
	}
	return deps.Writer.UpdateDescription(ctx, issue.Key, description)
}

// orderedRepos lists the repositories in merge order when there is one.
func orderedRepos(state bankImplementation, documents documentsState) []repoImplementation {
	if documents.TRD == nil {
		return state.Repos
	}
	ordered := []repoImplementation{}
	used := map[int]bool{}
	for _, name := range documents.TRD.MergeOrder {
		for index, repo := range state.Repos {
			if !used[index] && strings.EqualFold(repo.Repo, name) {
				used[index] = true
				ordered = append(ordered, repo)
			}
		}
	}
	for index, repo := range state.Repos {
		if !used[index] {
			ordered = append(ordered, repo)
		}
	}
	return ordered
}

func implementationCell(repo repoImplementation) string {
	switch {
	case repo.Error != "" && !repo.approved():
		return "failed: " + repo.Error
	case repo.NoChanges:
		return "no changes needed"
	case repo.Changed:
		return "committed"
	case repo.Implemented:
		return "no changes yet"
	default:
		return "waiting"
	}
}

func reviewCell(repo repoImplementation, maxRounds int) string {
	if repo.NoChanges {
		return "-"
	}
	last := len(repo.Reviews) - 1
	switch {
	case last < 0:
		return "waiting"
	case repo.Reviews[last].Approved:
		return fmt.Sprintf("approved (round %d)", repo.Reviews[last].Round)
	case len(repo.Reviews) >= maxRounds:
		return fmt.Sprintf("findings open after %d rounds", len(repo.Reviews))
	default:
		return fmt.Sprintf("changes requested (round %d of %d)", repo.Reviews[last].Round, maxRounds)
	}
}

func mergeRequestCell(repo repoImplementation) string {
	switch {
	case repo.MergeRequest != "":
		return repo.MergeRequest
	case repo.Error != "" && repo.approved():
		return "failed: " + repo.Error
	case repo.Pushed:
		return "branch pushed"
	case repo.approved() && repo.Changed && !repo.NoChanges:
		return "branch ready (local)"
	default:
		return "-"
	}
}

// implementationReport describes each repository's branch, review and
// merge request for the FSD and runbook writers.
func implementationReport(state bankImplementation, documents documentsState) string {
	lines := []string{}
	if documents.TRD != nil && len(documents.TRD.MergeOrder) > 0 {
		lines = append(lines, "Merge and deploy order: "+strings.Join(documents.TRD.MergeOrder, " -> "))
	}
	for _, repo := range orderedRepos(state, documents) {
		line := fmt.Sprintf("- %s: branch %s from %s", repo.Repo, repo.Branch, repo.BaseBranch)
		switch {
		case repo.NoChanges:
			line += ", no changes needed"
		case repo.MergeRequest != "":
			line += ", merge request " + repo.MergeRequest
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func loadBankImplementation(ctx context.Context, reader IssueReader, key string) (bankImplementation, error) {
	state := bankImplementation{}
	raw, found, err := reader.GetIssueProperty(ctx, key, BankImplementationProperty)
	if err != nil {
		return state, fmt.Errorf("get state: %w", err)
	}
	if !found {
		return state, nil
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return state, fmt.Errorf("decode state: %w", err)
	}
	return state, nil
}

func saveBankImplementation(ctx context.Context, writer IssueWriter, key string, state bankImplementation) error {
	value, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	return writer.SetIssueProperty(ctx, key, BankImplementationProperty, value)
}

func maxReviewRounds(deps Dependencies) int {
	if deps.MaxReviewCycles <= 0 {
		return DefaultMaxReviewCycles
	}
	return deps.MaxReviewCycles
}

// failer logs a failed operation and wraps its error.
func failer(deps Dependencies, document, key string) func(string, error) error {
	return func(operation string, err error) error {
		logOperationFailure(deps.Logger, document, key, operation, err, 0)
		return fmt.Errorf("%s: %w", operation, err)
	}
}

// runParallel calls work for 0..n-1 concurrently and waits for all.
func runParallel(n int, work func(int)) {
	var group sync.WaitGroup
	for index := 0; index < n; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			work(index)
		}()
	}
	group.Wait()
}
