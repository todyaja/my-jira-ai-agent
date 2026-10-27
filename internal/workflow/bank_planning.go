package workflow

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/tody-aja/jira-ai-agent/internal/jira"
	"github.com/tody-aja/jira-ai-agent/internal/repos"
)

// RepoTRD is the technical plan for one repository.
type RepoTRD struct {
	Repo string
	// Title says what changes in the repository, e.g. "Batch & transaksi
	// bulk withdrawal overpayment".
	Title string
	TRD   string
}

// PlanInput asks for the overview and per-repository TRDs of an issue, or a
// revision of them when Feedback is set.
type PlanInput struct {
	IssueKey    string
	Summary     string
	Description string
	// RFC is the approved RFC page in Confluence storage format.
	RFC string
	// Repos are the repositories the issue changes; AllRepos may be read for
	// context.
	Repos    []repos.Repo
	AllRepos []repos.Repo
	// CurrentOverview, Current and CurrentSQL are the published TRDs and SQL
	// migration plan being revised.
	CurrentOverview string
	Current         []RepoTRD
	CurrentSQL      string
	Feedback        []Feedback
}

// PlanResult is an overview TRD covering what crosses repositories and one
// TRD per repository, with the order the repositories merge and deploy in,
// and the SQL migration plan - the full scripts, applied on the development
// database once approved - or "None". BranchName is a few words naming what
// changes, for the implementation branch; it may be empty.
type PlanResult struct {
	Overview   string
	Repos      []RepoTRD
	MergeOrder []string
	SQL        string
	BranchName string
}

// RepoPlanner studies the repositories and writes the TRDs.
type RepoPlanner interface {
	Plan(context.Context, PlanInput) (PlanResult, error)
}

// ExecuteRequestTRD writes the overview TRD and one TRD per ticked
// repository from the approved RFC, files them under TRDs in Confluence,
// links them from the issue and moves it to TRD Review.
func ExecuteRequestTRD(ctx context.Context, key string, deps Dependencies) error {
	logger := deps.Logger
	fail := func(operation string, err error) error {
		logOperationFailure(logger, "TRD", key, operation, err, 0)
		return fmt.Errorf("%s: %w", operation, err)
	}
	if deps.Bank.Planner == nil || deps.Bank.Publisher == nil {
		return fail("configure", fmt.Errorf("no TRD planner or Confluence publisher is configured"))
	}
	issue, err := deps.Reader.GetIssue(ctx, key)
	if err != nil {
		return fail("get issue", err)
	}
	ticked, err := tickedRepos(issue, deps.Bank)
	if err != nil {
		return fail("select repositories", err)
	}
	state, err := loadDocumentsState(ctx, deps.Reader, key)
	if err != nil {
		return fail("load documents", err)
	}
	if state.RFC == nil {
		return fail("get RFC", fmt.Errorf("the issue has no published RFC yet"))
	}
	rfc, err := pageContent(ctx, deps.Bank.Publisher, state.RFC.Title)
	if err != nil {
		return fail("get RFC", err)
	}
	feedback := []Feedback{}
	if state.TRD != nil {
		pages := []documentRecord{state.TRD.Overview}
		for _, repo := range state.TRD.Repos {
			pages = append(pages, repo.documentRecord)
		}
		if state.SQL != nil {
			pages = append(pages, *state.SQL)
		}
		if feedback, err = pageFeedback(ctx, deps.Bank.Publisher, pages); err != nil {
			return fail("get comments", err)
		}
		if len(feedback) == 0 && sameRepos(state.TRD.Repos, ticked) {
			return trdReview.skipRevision(ctx, key, deps.Writer, logger)
		}
	}

	if ticked, err = baseCheckouts(ctx, deps.Bank, ticked); err != nil {
		return fail("check out base branches", err)
	}
	input := PlanInput{
		IssueKey:    issue.Key,
		Summary:     issue.Summary,
		Description: issue.Description,
		RFC:         rfc,
		Repos:       ticked,
		AllRepos:    deps.Bank.Repos.All(),
		Feedback:    feedback,
	}
	if state.TRD != nil {
		if input.CurrentOverview, err = pageContent(ctx, deps.Bank.Publisher, state.TRD.Overview.Title); err != nil {
			return fail("get current TRD", err)
		}
		for _, repo := range state.TRD.Repos {
			content, err := pageContent(ctx, deps.Bank.Publisher, repo.Title)
			if err != nil {
				return fail("get current TRD", err)
			}
			input.Current = append(input.Current, RepoTRD{Repo: repo.Repo, Title: repo.Title, TRD: content})
		}
		if state.SQL != nil {
			if input.CurrentSQL, err = pageContent(ctx, deps.Bank.Publisher, state.SQL.Title); err != nil {
				return fail("get current SQL plan", err)
			}
		}
	}

	startedAt := time.Now()
	if logger != nil {
		logger.Printf("TRD generation started: issue=%s repos=%s feedback=%d", key, strings.Join(repoNames(ticked), ","), len(feedback))
	}
	plan, err := deps.Bank.Planner.Plan(ctx, input)
	if err != nil {
		logOperationFailure(logger, "TRD", key, "plan", err, time.Since(startedAt))
		return fmt.Errorf("plan: %w", err)
	}
	if logger != nil {
		logger.Printf("TRD generation completed: issue=%s duration=%s", key, time.Since(startedAt))
	}
	repoPlans, err := matchRepoPlans(plan.Repos, ticked)
	if err != nil {
		return fail("check plan", err)
	}

	record := trdRecord{MergeOrder: mergeOrder(plan.MergeOrder, ticked), BranchName: plan.BranchName}
	record.Overview = documentRecord{Title: issue.Key + " TRD - " + issue.Summary}
	record.Overview.Parents = []string{"TRDs", "TRDs " + strconv.Itoa(deps.Bank.now().Year())}
	if state.TRD != nil {
		record.Overview.Title, record.Overview.Parents = state.TRD.Overview.Title, state.TRD.Overview.Parents
	}
	if err := trdReview.closeAll(ctx, key, deps.Bank.Publisher, feedback, logger); err != nil {
		return err
	}
	overview, err := publishLabelled(ctx, deps.Bank.Publisher, record.Overview.Parents, record.Overview.Title, plan.Overview, key)
	if err != nil {
		return fail("publish TRD", err)
	}
	record.Overview.PageID, record.Overview.URL = overview.ID, overview.URL

	links := []jira.DocumentLink{{Label: "Overview", URL: overview.URL}}
	repoParents := append(append([]string{}, record.Overview.Parents...), record.Overview.Title)
	for _, repoPlan := range repoPlans {
		title := repoTRDTitle(issue.Key, repoPlan)
		if previous := previousRepoTRD(state.TRD, repoPlan.Repo); previous != nil {
			title = previous.Title
		}
		page, err := publishLabelled(ctx, deps.Bank.Publisher, repoParents, title, repoPlan.TRD, key)
		if err != nil {
			return fail("publish "+repoPlan.Repo+" TRD", err)
		}
		record.Repos = append(record.Repos, repoTRDRecord{Repo: repoPlan.Repo, documentRecord: documentRecord{Title: title, URL: page.URL, PageID: page.ID, Parents: repoParents}})
		links = append(links, jira.DocumentLink{Label: repoPlan.Repo, URL: page.URL})
	}
	if logger != nil {
		logger.Printf("TRD published: issue=%s url=%s repos=%d", key, overview.URL, len(record.Repos))
	}
	state.TRD = &record

	// The SQL plan gets its own page, so the DBA can review the scripts
	// before they run. A plan that later needs no change keeps its page,
	// saying so, so the link on the issue stays valid.
	description := issue.DescriptionADF
	if !noSQLChanges(plan.SQL) || state.SQL != nil {
		sqlRecord := documentRecord{Title: issue.Key + " SQL Migration Plan - " + issue.Summary, Parents: []string{"SQLs", "SQLs " + strconv.Itoa(deps.Bank.now().Year())}}
		if state.SQL != nil {
			sqlRecord.Title, sqlRecord.Parents = state.SQL.Title, state.SQL.Parents
		}
		body := plan.SQL
		if noSQLChanges(body) {
			body = "No database changes."
		}
		page, err := publishLabelled(ctx, deps.Bank.Publisher, sqlRecord.Parents, sqlRecord.Title, body, key)
		if err != nil {
			return fail("publish SQL plan", err)
		}
		sqlRecord.PageID, sqlRecord.URL = page.ID, page.URL
		state.SQL = &sqlRecord
		if description, err = jira.SetDocumentLinks(description, jira.SQLHeading, []jira.DocumentLink{{Label: sqlRecord.Title, URL: sqlRecord.URL}}); err != nil {
			return fail("build description", err)
		}
		if logger != nil {
			logger.Printf("SQL plan published: issue=%s url=%s", key, page.URL)
		}
	}

	if err := saveDocumentsState(ctx, deps.Writer, key, state); err != nil {
		return fail("save documents", err)
	}
	description, err = jira.SetDocumentLinks(description, jira.TRDHeading, links)
	if err != nil {
		return fail("build description", err)
	}
	if err := deps.Writer.UpdateDescription(ctx, key, description); err != nil {
		return fail("update description", err)
	}
	if err := deps.Writer.TransitionTo(ctx, key, string(BankTRDReview)); err != nil {
		return fail("transition", err)
	}
	return nil
}

// matchRepoPlans returns one plan per ticked repository, in ticked order.
// The planner must cover every ticked repository and no other.
func matchRepoPlans(plans []RepoTRD, ticked []repos.Repo) ([]RepoTRD, error) {
	byName := map[string]RepoTRD{}
	for _, plan := range plans {
		name := strings.ToLower(strings.TrimSpace(plan.Repo))
		if _, duplicate := byName[name]; duplicate {
			return nil, fmt.Errorf("the planner wrote two TRDs for %s", plan.Repo)
		}
		byName[name] = plan
	}
	matched := []RepoTRD{}
	for _, repo := range ticked {
		plan, found := byName[strings.ToLower(repo.Name)]
		if !found || strings.TrimSpace(plan.TRD) == "" {
			return nil, fmt.Errorf("the planner wrote no TRD for %s", repo.Name)
		}
		plan.Repo = repo.Name
		matched = append(matched, plan)
		delete(byName, strings.ToLower(repo.Name))
	}
	for _, plan := range byName {
		return nil, fmt.Errorf("the planner wrote a TRD for %s, which is not ticked", plan.Repo)
	}
	return matched, nil
}

// mergeOrder keeps the planner's order of the ticked repositories and
// appends any it left out, so every repository has a place.
func mergeOrder(order []string, ticked []repos.Repo) []string {
	result := []string{}
	seen := map[string]bool{}
	for _, name := range order {
		for _, repo := range ticked {
			if strings.EqualFold(strings.TrimSpace(name), repo.Name) && !seen[repo.Name] {
				seen[repo.Name] = true
				result = append(result, repo.Name)
			}
		}
	}
	for _, repo := range ticked {
		if !seen[repo.Name] {
			result = append(result, repo.Name)
		}
	}
	return result
}

func repoTRDTitle(key string, plan RepoTRD) string {
	title := strings.TrimSpace(plan.Title)
	if title == "" {
		title = "TRD"
	}
	return fmt.Sprintf("%s [%s] %s", key, plan.Repo, title)
}

func previousRepoTRD(trd *trdRecord, repo string) *repoTRDRecord {
	if trd == nil {
		return nil
	}
	for index := range trd.Repos {
		if strings.EqualFold(trd.Repos[index].Repo, repo) {
			return &trd.Repos[index]
		}
	}
	return nil
}

// sameRepos reports whether the published TRDs cover exactly the ticked
// repositories; ticking another one asks for a new plan even without
// feedback.
func sameRepos(published []repoTRDRecord, ticked []repos.Repo) bool {
	if len(published) != len(ticked) {
		return false
	}
	for _, repo := range ticked {
		if previousRepoTRD(&trdRecord{Repos: published}, repo.Name) == nil {
			return false
		}
	}
	return true
}
