package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tody-aja/jira-ai-agent/internal/confluence"
	"github.com/tody-aja/jira-ai-agent/internal/jira"
	"github.com/tody-aja/jira-ai-agent/internal/repos"
)

// bankFake is one in-memory Jira issue, Confluence space, set of agents,
// repositories and GitLab, enough to walk an issue through the bank flow.
type bankFake struct {
	t  *testing.T
	mu sync.Mutex

	issue      jira.Issue
	status     string
	comments   []jira.Comment
	properties map[string]json.RawMessage

	pages       map[string]PublishedPage
	pageBodies  map[string]string
	pageParents map[string][]string
	attachments map[string]string
	labels      map[string][]string
	publishes   int
	// openComments are the reviewers' comments per page ID; closed records
	// each closed comment with its reply and how many pages were published
	// before it was closed.
	openComments map[string][]PageComment
	closed       []closedComment

	repoList  []repos.Repo
	documents []DocumentInput
	docx      string
	plans     []PlanInput
	plan      PlanResult

	implemented []ImplementInput
	implementFn func(ImplementInput) (string, error)
	reviewed    []ReviewInput
	verdicts    map[string][]bool // per repo, one per round
	diffs       map[string]string
	commits     map[string]int
	pushed      []string
	removed     []string
	merges      map[string]string
	// sql, when set, is the development database.
	sql *fakeSQLRunner
}

// fakeSQLRunner records the scripts run on the development database and
// fails the one containing failOn.
type fakeSQLRunner struct {
	ran    []string
	failOn string
}

func (r *fakeSQLRunner) CheckSQL(database, script string, readOnly bool) error {
	if database != "cbsdb" && database != "ctdb" {
		return fmt.Errorf("no database %s", database)
	}
	return nil
}

func (r *fakeSQLRunner) RunSQL(_ context.Context, database, script string, readOnly bool) (string, error) {
	r.ran = append(r.ran, fmt.Sprintf("%s readOnly=%t: %s", database, readOnly, script))
	if r.failOn != "" && strings.Contains(script, r.failOn) {
		return "partial output", fmt.Errorf("Error 1062: duplicate entry")
	}
	return "-- 1 row(s)", nil
}

func newBankFake(t *testing.T, ticked ...string) *bankFake {
	docx := filepath.Join(t.TempDir(), "doc.docx")
	if err := os.WriteFile(docx, []byte("word"), 0o644); err != nil {
		t.Fatal(err)
	}
	items := []string{}
	for _, name := range []string{"bnc_busi", "bnc_iobus", "sump-vue"} {
		state := "TODO"
		for _, tick := range ticked {
			if tick == name {
				state = "DONE"
			}
		}
		items = append(items, fmt.Sprintf(`{"type":"taskItem","attrs":{"localId":"%s","state":"%s"},"content":[{"type":"text","text":"%s"}]}`, name, state, name))
	}
	description := `{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"Bulk withdrawal."}]},{"type":"taskList","attrs":{"localId":"l"},"content":[` + strings.Join(items, ",") + `]}]}`
	return &bankFake{
		t:            t,
		issue:        jira.Issue{Key: "NBS-12", Summary: "Bulk withdrawal", Description: "Bulk withdrawal.", DescriptionADF: json.RawMessage(description)},
		properties:   map[string]json.RawMessage{},
		pages:        map[string]PublishedPage{},
		pageBodies:   map[string]string{},
		pageParents:  map[string][]string{},
		attachments:  map[string]string{},
		labels:       map[string][]string{},
		openComments: map[string][]PageComment{},
		repoList: []repos.Repo{
			{Name: "bnc_busi", Path: "/codes/bnc_busi", BaseBranch: "master"},
			{Name: "bnc_iobus", Path: "/codes/bnc_iobus", BaseBranch: "master"},
			{Name: "sump-vue", Path: "/codes/sump-vue", BaseBranch: "develop"},
		},
		docx:     docx,
		verdicts: map[string][]bool{},
		diffs:    map[string]string{},
		commits:  map[string]int{},
		merges:   map[string]string{},
	}
}

func (f *bankFake) deps() Dependencies {
	return Dependencies{
		Reader:          f,
		Writer:          f,
		Engineer:        f,
		Reviewer:        f,
		MaxReviewCycles: 3,
		JiraBaseURL:     "https://jira",
		Logger:          log.New(io.Discard, "", 0),
		Bank: BankDependencies{
			Repos:         f,
			Documents:     f,
			Planner:       f,
			Workspace:     func(repo repos.Repo) Workspace { return &fakeRepoWorkspace{fake: f, repo: repo.Name} },
			Publisher:     f,
			MergeRequests: f,
			Now:           func() time.Time { return time.Date(2026, 10, 21, 0, 0, 0, 0, time.UTC) },
		},
	}
}

// run dispatches the issue's current status through the bank workflow.
func (f *bankFake) run(t *testing.T) error {
	t.Helper()
	step, ok := BankWorkflow{}.Step(f.status)
	if !ok {
		t.Fatalf("status %q is not a bank status", f.status)
	}
	deps := f.deps()
	// Only a set runner is passed: a nil *fakeSQLRunner in the interface
	// would not read as nil.
	if f.sql != nil {
		deps.Bank.SQL = f.sql
	}
	return step.Run(context.Background(), f.issue.Key, deps)
}

// IssueReader and IssueWriter.
func (f *bankFake) GetIssue(context.Context, string) (jira.Issue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.issue, nil
}
func (f *bankFake) GetComments(context.Context, string) ([]jira.Comment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]jira.Comment(nil), f.comments...), nil
}
func (f *bankFake) GetIssueProperty(_ context.Context, _ string, key string) (json.RawMessage, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	value, found := f.properties[key]
	return value, found, nil
}
func (f *bankFake) UpdateDescription(_ context.Context, _ string, description json.RawMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.issue.DescriptionADF = description
	return nil
}
func (f *bankFake) TransitionTo(_ context.Context, _ string, status string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status = status
	return nil
}
func (f *bankFake) AddComment(_ context.Context, _ string, body json.RawMessage) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	text, err := jira.DescriptionText(body)
	if err != nil {
		f.t.Errorf("comment is not valid ADF: %v", err)
	}
	id := fmt.Sprintf("c%d", len(f.comments)+1)
	f.comments = append(f.comments, jira.Comment{ID: id, Body: text, Properties: map[string]json.RawMessage{}})
	return id, nil
}
func (f *bankFake) SetCommentProperty(_ context.Context, id, key string, value json.RawMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for index := range f.comments {
		if f.comments[index].ID == id {
			f.comments[index].Properties[key] = value
		}
	}
	return nil
}
func (f *bankFake) SetIssueProperty(_ context.Context, _ string, key string, value json.RawMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.properties[key] = value
	return nil
}

// RepoSelector.
func (f *bankFake) All() []repos.Repo { return f.repoList }
func (f *bankFake) Select(names []string) ([]repos.Repo, error) {
	selected := []repos.Repo{}
	for _, name := range names {
		found := false
		for _, repo := range f.repoList {
			if strings.EqualFold(repo.Name, name) {
				selected, found = append(selected, repo), true
			}
		}
		if !found {
			return nil, fmt.Errorf("unknown repositories %s", name)
		}
	}
	return selected, nil
}

// DocumentPublisher.
func (f *bankFake) PublishPageUnder(_ context.Context, parents []string, title, markdown string) (PublishedPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	page, found := f.pages[title]
	if !found {
		page = PublishedPage{ID: fmt.Sprintf("p%d", len(f.pages)+1), URL: "https://wiki/" + strings.ReplaceAll(title, " ", "+")}
		f.pages[title] = page
		f.pageParents[title] = parents
	}
	f.pageBodies[title] = markdown
	f.publishes++
	return page, nil
}
func (f *bankFake) OpenComments(_ context.Context, pageID string) ([]PageComment, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]PageComment(nil), f.openComments[pageID]...), nil
}
func (f *bankFake) CloseComment(_ context.Context, comment PageComment, reply string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	open := []PageComment{}
	for _, candidate := range f.openComments[comment.PageID] {
		if candidate.ID != comment.ID {
			open = append(open, candidate)
		}
	}
	f.openComments[comment.PageID] = open
	f.closed = append(f.closed, closedComment{id: comment.ID, reply: reply, publishes: f.publishes})
	return nil
}
func (f *bankFake) PageContent(_ context.Context, title string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body, found := f.pageBodies[title]
	return body, found, nil
}
func (f *bankFake) AttachFile(_ context.Context, pageID, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attachments[pageID] = path
	return nil
}
func (f *bankFake) AddLabel(_ context.Context, pageID, label string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.labels[pageID] = append(f.labels[pageID], label)
	return nil
}

// DocumentWriter and RepoPlanner.
func (f *bankFake) WriteDocument(_ context.Context, input DocumentInput) (DocumentResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.documents = append(f.documents, input)
	number := map[DocumentKind]string{RFCKind: "RFC-2026-11", FSDKind: "05CBS-FSD102026", RunbookKind: "20261021 Runbook -"}[input.Kind]
	return DocumentResult{Title: number + " Bulk Withdrawal", Docx: f.docx, Summary: fmt.Sprintf("# %s\nround %d", input.Kind, len(f.documents))}, nil
}
func (f *bankFake) Plan(_ context.Context, input PlanInput) (PlanResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.plans = append(f.plans, input)
	return f.plan, nil
}

// Engineer and Reviewer.
func (f *bankFake) Implement(_ context.Context, input ImplementInput) (string, error) {
	f.mu.Lock()
	f.implemented = append(f.implemented, input)
	implement := f.implementFn
	f.mu.Unlock()
	if implement != nil {
		return implement(input)
	}
	return "changed " + input.Dir, nil
}
func (f *bankFake) Review(_ context.Context, input ReviewInput) (ReviewResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reviewed = append(f.reviewed, input)
	repo := strings.TrimPrefix(input.Dir, "/worktrees/")
	verdicts := f.verdicts[repo]
	approved := true
	if input.Round-1 < len(verdicts) {
		approved = verdicts[input.Round-1]
	}
	return ReviewResult{Approved: approved, Review: fmt.Sprintf("review of %s round %d", repo, input.Round)}, nil
}

// MergeRequestOpener.
func (f *bankFake) OpenMergeRequest(_ context.Context, repo repos.Repo, source, target, title, body string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if url, found := f.merges[repo.Name]; found {
		return url, nil
	}
	if !strings.Contains(title, "["+repo.Name+"]") || target != repo.BaseBranch || source != "feature/nbs-12-bulk-withdraw-batch" || !strings.Contains(body, "Merge order") {
		f.t.Errorf("merge request for %s = (%q, %q, %q, %q)", repo.Name, source, target, title, body)
	}
	url := "http://gitlab/" + repo.Name + "/-/merge_requests/1"
	f.merges[repo.Name] = url
	return url, nil
}

type closedComment struct {
	id, reply string
	publishes int
}

type fakeRepoWorkspace struct {
	fake *bankFake
	repo string
}

func (w *fakeRepoWorkspace) Reference(context.Context) (string, error) {
	return "/reference/" + w.repo, nil
}

func (w *fakeRepoWorkspace) Prepare(context.Context, string) (string, error) {
	return "/worktrees/" + w.repo, nil
}
func (w *fakeRepoWorkspace) CommitAll(context.Context, string, string) (bool, error) {
	w.fake.mu.Lock()
	defer w.fake.mu.Unlock()
	w.fake.commits[w.repo]++
	w.fake.diffs[w.repo] = "diff of " + w.repo
	return true, nil
}
func (w *fakeRepoWorkspace) Diff(context.Context, string) (string, error) {
	w.fake.mu.Lock()
	defer w.fake.mu.Unlock()
	return w.fake.diffs[w.repo], nil
}
func (w *fakeRepoWorkspace) Remove(context.Context, string) error {
	w.fake.mu.Lock()
	defer w.fake.mu.Unlock()
	w.fake.removed = append(w.fake.removed, w.repo)
	return nil
}
func (w *fakeRepoWorkspace) Push(context.Context, string) error {
	w.fake.mu.Lock()
	defer w.fake.mu.Unlock()
	w.fake.pushed = append(w.fake.pushed, w.repo)
	return nil
}

func (f *bankFake) descriptionText(t *testing.T) string {
	t.Helper()
	text, err := jira.DescriptionText(f.issue.DescriptionADF)
	if err != nil {
		t.Fatal(err)
	}
	return text
}

func (f *bankFake) lastComment() string {
	return f.comments[len(f.comments)-1].Body
}

func expectStatus(t *testing.T, f *bankFake, err error, want Status) {
	t.Helper()
	if err != nil {
		t.Fatalf("step error = %v", err)
	}
	if f.status != string(want) {
		t.Fatalf("status = %q, want %q", f.status, want)
	}
}

func TestBankWorkflowWalksAnIssueThroughEveryStep(t *testing.T) {
	f := newBankFake(t, "bnc_busi", "sump-vue")
	f.plan = PlanResult{
		Overview:   "# Overview",
		MergeOrder: []string{"bnc_busi", "sump-vue"},
		BranchName: "Bulk withdraw batch",
		Repos: []RepoTRD{
			{Repo: "SUMP-VUE", Title: "Halaman upload", TRD: "# sump-vue TRD"},
			{Repo: "bnc_busi", Title: "Batch bulk withdrawal", TRD: "# busi TRD"},
		},
	}
	f.verdicts["sump-vue"] = []bool{false, true}

	// Request RFC: the RFC is filed under RFCs > RFCs 2026 with its Word file.
	f.status = "Request RFC"
	expectStatus(t, f, f.run(t), BankRFCReview)
	rfcTitle := "RFC-2026-11 Bulk Withdrawal [NBS-12]"
	rfc, found := f.pages[rfcTitle]
	if !found || strings.Join(f.pageParents[rfcTitle], " > ") != "RFCs > RFCs 2026" {
		t.Fatalf("pages = %v, want %q under RFCs > RFCs 2026", f.pageParents, rfcTitle)
	}
	if f.attachments[rfc.ID] != f.docx || strings.Join(f.labels[rfc.ID], ",") != "NBS-12" {
		t.Fatalf("RFC attachment %q labels %q, want the Word file and the key", f.attachments[rfc.ID], f.labels[rfc.ID])
	}
	if input := f.documents[0]; input.Kind != RFCKind || strings.Join(repoNames(input.Repos), ",") != "bnc_busi,sump-vue" || len(input.AllRepos) != 3 {
		t.Fatalf("RFC input = %+v, want the ticked repositories and all of them for context", input)
	}
	if text := f.descriptionText(t); !strings.Contains(text, "RFC\n=============\n"+rfcTitle+": "+rfc.URL) {
		t.Fatalf("description = %q, want the RFC link", text)
	}

	// Sending it back without feedback leaves the RFC alone.
	f.status = "Request RFC"
	expectStatus(t, f, f.run(t), BankRFCReview)
	if len(f.documents) != 1 || !strings.Contains(f.lastComment(), "Nothing to revise") {
		t.Fatalf("documents written = %d, last comment %q; want no rewrite", len(f.documents), f.lastComment())
	}

	// Request TRD: an overview plus one TRD per ticked repository.
	f.status = "Request TRD"
	expectStatus(t, f, f.run(t), BankTRDReview)
	overviewTitle := "NBS-12 TRD - Bulk withdrawal"
	if strings.Join(f.pageParents[overviewTitle], " > ") != "TRDs > TRDs 2026" {
		t.Fatalf("overview parents = %q", f.pageParents[overviewTitle])
	}
	for _, title := range []string{"NBS-12 [bnc_busi] Batch bulk withdrawal", "NBS-12 [sump-vue] Halaman upload"} {
		if strings.Join(f.pageParents[title], " > ") != "TRDs > TRDs 2026 > "+overviewTitle {
			t.Fatalf("parents of %q = %q, want under the overview", title, f.pageParents[title])
		}
	}
	if !strings.Contains(f.plans[0].RFC, "# rfc") {
		t.Fatalf("planner RFC = %q, want the published RFC", f.plans[0].RFC)
	}
	if text := f.descriptionText(t); !strings.Contains(text, "Overview: ") || !strings.Contains(text, "bnc_busi: https://wiki/NBS-12+[bnc_busi]") {
		t.Fatalf("description = %q, want the TRD links", text)
	}

	// Ready To Implement and Implementing: both repositories, in parallel.
	f.status = "Ready To Implement"
	expectStatus(t, f, f.run(t), BankImplementing)
	expectStatus(t, f, f.run(t), BankAIReview)
	if len(f.implemented) != 2 {
		t.Fatalf("implemented %d repositories, want 2", len(f.implemented))
	}
	for _, input := range f.implemented {
		repo := strings.TrimPrefix(input.Dir, "/worktrees/")
		if !strings.Contains(input.Context, "You are working in "+repo+" only") || !strings.Contains(input.Context, "unit tests") || !strings.Contains(input.TRD, "# "+map[string]string{"bnc_busi": "busi", "sump-vue": "sump-vue"}[repo]+" TRD") || !strings.Contains(input.TRD, "# Overview") {
			t.Fatalf("implement input for %s = %+v, want the repository context and its TRDs", repo, input)
		}
	}

	// AI Review: bnc_busi is approved, sump-vue gets changes requested.
	expectStatus(t, f, f.run(t), BankImplementing)
	if len(f.pushed) != 0 {
		t.Fatalf("pushed %v before every repository was approved", f.pushed)
	}
	text := f.descriptionText(t)
	for _, want := range []string{"REPOSITORY STATUS", "approved (round 1)", "changes requested (round 1 of 3)", "Merge order: bnc_busi -> sump-vue"} {
		if !strings.Contains(text, want) {
			t.Fatalf("description = %q, want %q", text, want)
		}
	}

	// Only sump-vue is implemented again, with its review to address.
	expectStatus(t, f, f.run(t), BankAIReview)
	if len(f.implemented) != 3 || f.implemented[2].Dir != "/worktrees/sump-vue" || f.implemented[2].Review == "" {
		t.Fatalf("third implementation = %+v, want sump-vue addressing its review", f.implemented[len(f.implemented)-1])
	}
	expectStatus(t, f, f.run(t), BankEngineerReview)
	sort.Strings(f.pushed)
	if strings.Join(f.pushed, ",") != "bnc_busi,sump-vue" || len(f.removed) != 2 {
		t.Fatalf("pushed %v removed %v, want both branches pushed and worktrees removed", f.pushed, f.removed)
	}

	// PR Review: one merge request per repository, once.
	f.status = "PR Review"
	expectStatus(t, f, f.run(t), BankPRReview)
	if len(f.merges) != 2 || !strings.Contains(f.lastComment(), "bnc_busi: http://gitlab/bnc_busi/-/merge_requests/1") {
		t.Fatalf("merge requests %v, last comment %q", f.merges, f.lastComment())
	}
	comments := len(f.comments)
	expectStatus(t, f, f.run(t), BankPRReview)
	if len(f.comments) != comments {
		t.Fatal("a second PR Review run commented again")
	}
	if text := f.descriptionText(t); !strings.Contains(text, "http://gitlab/sump-vue/-/merge_requests/1") {
		t.Fatalf("description = %q, want the merge requests in the status table", text)
	}

	// FSD and runbook build on everything before them. The old FRD name
	// still works until the Jira status is renamed.
	f.status = "FRD Request"
	expectStatus(t, f, f.run(t), BankFSDReview)
	fsdSources := titles(f.documents[len(f.documents)-1].Sources)
	if fsdSources != rfcTitle+"|"+overviewTitle+"|NBS-12 [bnc_busi] Batch bulk withdrawal|NBS-12 [sump-vue] Halaman upload|Implementation" {
		t.Fatalf("FSD sources = %q", fsdSources)
	}
	if report := f.documents[len(f.documents)-1].Sources[4].Content; !strings.Contains(report, "merge request http://gitlab/bnc_busi") {
		t.Fatalf("implementation report = %q, want the merge requests", report)
	}
	f.status = "Runbook Request"
	expectStatus(t, f, f.run(t), BankRunbookReview)
	if sources := titles(f.documents[len(f.documents)-1].Sources); !strings.HasPrefix(sources, rfcTitle+"|05CBS-FSD102026 Bulk Withdrawal [NBS-12]|") {
		t.Fatalf("runbook sources = %q, want the RFC and the FSD first", sources)
	}
}

func titles(sources []SourceDocument) string {
	names := []string{}
	for _, source := range sources {
		names = append(names, source.Title)
	}
	return strings.Join(names, "|")
}

func TestBankDocumentRevisionKeepsTitleAndMarksFeedback(t *testing.T) {
	f := newBankFake(t, "bnc_busi")
	f.status = "Request RFC"
	expectStatus(t, f, f.run(t), BankRFCReview)
	rfcTitle := "RFC-2026-11 Bulk Withdrawal [NBS-12]"
	pageID := f.pages[rfcTitle].ID
	// Jira comments are not feedback for bank documents.
	f.comments = append(f.comments, jira.Comment{ID: "jira", Body: "!rfc-writer ignored", Properties: map[string]json.RawMessage{}})
	f.openComments[pageID] = []PageComment{
		{ID: "inline", PageID: pageID, Inline: true, Text: "tambahkan alur lama", Selection: "Alur baru"},
		{ID: "footer", PageID: pageID, Text: "nomor RFC salah"},
	}
	f.status = "Request RFC"
	expectStatus(t, f, f.run(t), BankRFCReview)

	revision := f.documents[1]
	if revision.CurrentTitle != rfcTitle || revision.CurrentDocx != f.docx || revision.CurrentContent != "# rfc\nround 1" {
		t.Fatalf("revision input = %+v, want the current document", revision)
	}
	if len(revision.Feedback) != 2 || revision.Feedback[0].Text != "tambahkan alur lama" || revision.Feedback[0].Selection != "Alur baru" || revision.Feedback[0].Page != rfcTitle || revision.Feedback[1].Text != "nomor RFC salah" {
		t.Fatalf("feedback = %+v, want both page comments", revision.Feedback)
	}
	// Comments are closed before the page is republished, which would
	// unanchor inline comments.
	if len(f.closed) != 2 || f.closed[0].publishes != 1 || f.closed[1].publishes != 1 || !strings.Contains(f.closed[0].reply, "RFC") {
		t.Fatalf("closed = %+v, want both comments closed before republishing", f.closed)
	}

	// With every comment closed, sending it back leaves the RFC alone.
	f.status = "Request RFC"
	expectStatus(t, f, f.run(t), BankRFCReview)
	if len(f.documents) != 2 || !strings.Contains(f.lastComment(), "no open comments") {
		t.Fatalf("documents written = %d, last comment %q; want no rewrite", len(f.documents), f.lastComment())
	}
	if len(f.pages) != 1 || f.pageBodies["RFC-2026-11 Bulk Withdrawal [NBS-12]"] != "# rfc\nround 2" {
		t.Fatalf("pages = %v, want the one RFC page updated in place", f.pageBodies)
	}
}

func TestBankTRDRevisionReadsCommentsOnEveryTRDPage(t *testing.T) {
	f := newBankFake(t, "bnc_busi", "sump-vue")
	f.plan = PlanResult{Overview: "# Overview", Repos: []RepoTRD{{Repo: "bnc_busi", Title: "Batch", TRD: "# busi"}, {Repo: "sump-vue", Title: "Upload", TRD: "# vue"}}}
	for _, status := range []string{"Request RFC", "Request TRD"} {
		f.status = status
		if err := f.run(t); err != nil {
			t.Fatal(err)
		}
	}
	overview, repo := f.pages["NBS-12 TRD - Bulk withdrawal"].ID, f.pages["NBS-12 [sump-vue] Upload"].ID
	f.openComments[overview] = []PageComment{{ID: "o1", PageID: overview, Text: "urutan deploy dibalik"}}
	f.openComments[repo] = []PageComment{{ID: "r1", PageID: repo, Inline: true, Text: "pakai komponen tabel", Selection: "Halaman upload"}}

	f.status = "Request TRD"
	expectStatus(t, f, f.run(t), BankTRDReview)
	feedback := f.plans[1].Feedback
	if len(feedback) != 2 || feedback[0].Page != "NBS-12 TRD - Bulk withdrawal" || feedback[1].Page != "NBS-12 [sump-vue] Upload" || feedback[1].Selection != "Halaman upload" {
		t.Fatalf("feedback = %+v, want the overview and sump-vue comments with their pages", feedback)
	}
	if len(f.closed) != 2 || f.closed[0].publishes != f.closed[1].publishes || !strings.Contains(f.closed[0].reply, "TRD") {
		t.Fatalf("closed = %+v, want both comments closed before republishing", f.closed)
	}
	formatted := FormatFeedback(feedback)
	if !strings.Contains(formatted, `on page "NBS-12 [sump-vue] Upload"`) || !strings.Contains(formatted, `On the highlighted text "Halaman upload":`) {
		t.Fatalf("formatted feedback = %q", formatted)
	}
}

func TestBankStepsNeedTickedRepositoriesAndCompletePlans(t *testing.T) {
	f := newBankFake(t)
	f.status = "Request RFC"
	if err := f.run(t); err == nil || !strings.Contains(err.Error(), "no repository is ticked") || !strings.Contains(err.Error(), "bnc_busi, bnc_iobus, sump-vue") {
		t.Fatalf("error = %v, want the missing checklist explained", err)
	}

	f = newBankFake(t, "bnc_busi", "sump-vue")
	f.status = "Request RFC"
	expectStatus(t, f, f.run(t), BankRFCReview)
	f.plan = PlanResult{Overview: "# Overview", Repos: []RepoTRD{{Repo: "bnc_busi", TRD: "# busi"}}}
	f.status = "Request TRD"
	if err := f.run(t); err == nil || !strings.Contains(err.Error(), "no TRD for sump-vue") {
		t.Fatalf("error = %v, want the missing repository TRD reported", err)
	}
	f.plan.Repos = append(f.plan.Repos, RepoTRD{Repo: "sump-vue", TRD: "#"}, RepoTRD{Repo: "bnc_iobus", TRD: "#"})
	if err := f.run(t); err == nil || !strings.Contains(err.Error(), "bnc_iobus, which is not ticked") {
		t.Fatalf("error = %v, want the extra repository reported", err)
	}
}

func TestBankImplementingKeepsOtherRepositoriesWhenOneFails(t *testing.T) {
	f := newBankFake(t, "bnc_busi", "sump-vue")
	f.plan = PlanResult{Overview: "#", Repos: []RepoTRD{{Repo: "bnc_busi", TRD: "#"}, {Repo: "sump-vue", TRD: "#"}}}
	for _, status := range []string{"Request RFC", "Request TRD", "Ready To Implement"} {
		f.status = status
		if err := f.run(t); err != nil {
			t.Fatal(err)
		}
	}
	f.implementFn = func(input ImplementInput) (string, error) {
		if strings.HasSuffix(input.Dir, "sump-vue") {
			return "", fmt.Errorf("claude timed out")
		}
		return "done", nil
	}
	err := f.run(t)
	if err == nil || !strings.Contains(err.Error(), "implementation failed in sump-vue") || f.status != string(BankImplementing) {
		t.Fatalf("error = %v, status %q; want the failure reported and no move", err, f.status)
	}
	if f.commits["bnc_busi"] != 1 || f.commits["sump-vue"] != 0 {
		t.Fatalf("commits = %v, want bnc_busi kept", f.commits)
	}
	if text := f.descriptionText(t); !strings.Contains(text, "failed: implement: claude timed out") || !strings.Contains(text, "committed") {
		t.Fatalf("description = %q, want each repository's outcome", text)
	}
	if body := f.lastComment(); !strings.Contains(body, "sump-vue - round 1 failed") || !strings.Contains(body, "bnc_busi - round 1") {
		t.Fatalf("comment = %q", body)
	}
}

const testSQLPlan = "Adds the dpb_x table, then registers its service.\n\n" +
	"## cbsdb\n\n### Pre-check\n\n```sql\nSELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'dpb_x';\n```\n\n" +
	"### DDL\n\nFollows dpb_acct.\n\n```sql\nCREATE TABLE IF NOT EXISTS dpb_x (a int)\n```\n\n" +
	"### Verify\n\n```sql\nSELECT COUNT(*) FROM dpb_x;\n```\n\n" +
	"### Rollback\n\n```sql\nDROP TABLE dpb_x;\n```\n\n" +
	"## ctdb\n\n### DML\n\n```sql\nINSERT INTO smp_sys_trans SELECT 'x' FROM dual WHERE NOT EXISTS (SELECT 1 FROM smp_sys_trans WHERE a = 'x' AND b > 1);\n```\n"

func TestRequestTRDPublishesTheSQLPlanForReview(t *testing.T) {
	f := newBankFake(t, "bnc_busi")
	f.status = "Request RFC"
	expectStatus(t, f, f.run(t), BankRFCReview)
	f.plan = PlanResult{Overview: "# Overview", Repos: []RepoTRD{{Repo: "bnc_busi", TRD: "# busi"}}, SQL: testSQLPlan}
	f.status = "Request TRD"
	expectStatus(t, f, f.run(t), BankTRDReview)

	title := "NBS-12 SQL Migration Plan - Bulk withdrawal"
	if body := f.pageBodies[title]; body != testSQLPlan {
		t.Fatalf("SQL plan page = %q", body)
	}
	if parents := f.pageParents[title]; strings.Join(parents, "/") != "SQLs/SQLs 2026" {
		t.Fatalf("parents = %v", parents)
	}
	if labels := f.labels[f.pages[title].ID]; len(labels) != 1 || labels[0] != "NBS-12" {
		t.Fatalf("labels = %v", labels)
	}
	if text := f.descriptionText(t); !strings.Contains(text, "SQL MIGRATION PLAN") || !strings.Contains(text, "TRD") {
		t.Fatalf("description = %q, want the SQL plan and TRDs linked", text)
	}

	// A comment on the SQL page is TRD feedback; the planner sees the plan.
	f.openComments[f.pages[title].ID] = []PageComment{{ID: "s1", PageID: f.pages[title].ID, Text: "pakai shardkey acct_no"}}
	f.status = "Request TRD"
	expectStatus(t, f, f.run(t), BankTRDReview)
	revision := f.plans[len(f.plans)-1]
	if len(revision.Feedback) != 1 || revision.Feedback[0].Page != title || revision.CurrentSQL != testSQLPlan {
		t.Fatalf("revision feedback = %+v current SQL = %q", revision.Feedback, revision.CurrentSQL)
	}
}

func TestRequestTRDWithoutDatabaseChangesPublishesNoSQLPlan(t *testing.T) {
	f := newBankFake(t, "bnc_busi")
	f.status = "Request RFC"
	expectStatus(t, f, f.run(t), BankRFCReview)
	f.plan = PlanResult{Overview: "# Overview", Repos: []RepoTRD{{Repo: "bnc_busi", TRD: "# busi"}}, SQL: "None."}
	f.status = "Request TRD"
	expectStatus(t, f, f.run(t), BankTRDReview)
	for title := range f.pages {
		if strings.Contains(title, "SQL") {
			t.Fatalf("published %q for a change without database changes", title)
		}
	}
}

// approvedSQLIssue walks an issue to TRD Review with testSQLPlan, then puts
// the page in storage format, as Confluence returns it.
func approvedSQLIssue(t *testing.T) *bankFake {
	t.Helper()
	f := newBankFake(t, "bnc_busi")
	f.plan = PlanResult{Overview: "# Overview", Repos: []RepoTRD{{Repo: "bnc_busi", TRD: "# busi"}}, SQL: testSQLPlan}
	for _, status := range []string{"Request RFC", "Request TRD"} {
		f.status = status
		if err := f.run(t); err != nil {
			t.Fatal(err)
		}
	}
	f.pageBodies["NBS-12 SQL Migration Plan - Bulk withdrawal"] = confluence.MarkdownToStorage(testSQLPlan)
	return f
}

func TestReadyToImplementAppliesTheApprovedSQLPlanOnce(t *testing.T) {
	f := approvedSQLIssue(t)
	f.sql = &fakeSQLRunner{}
	f.status = "Ready To Implement"
	expectStatus(t, f, f.run(t), BankImplementing)

	want := []string{
		"cbsdb readOnly=true: SELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'dpb_x';",
		"cbsdb readOnly=false: CREATE TABLE IF NOT EXISTS dpb_x (a int);",
		"cbsdb readOnly=true: SELECT COUNT(*) FROM dpb_x;",
		"ctdb readOnly=false: INSERT INTO smp_sys_trans SELECT 'x' FROM dual WHERE NOT EXISTS (SELECT 1 FROM smp_sys_trans WHERE a = 'x' AND b > 1);",
	}
	if strings.Join(f.sql.ran, "\n") != strings.Join(want, "\n") {
		t.Fatalf("ran:\n%s\nwant:\n%s", strings.Join(f.sql.ran, "\n"), strings.Join(want, "\n"))
	}
	applied := ""
	for _, comment := range f.comments {
		if strings.Contains(comment.Body, "SQL migration plan on the development database") {
			applied = comment.Body
		}
	}
	if !strings.Contains(applied, "cbsdb - ddl") || !strings.Contains(applied, "rollback was not run") {
		t.Fatalf("SQL comment = %q", applied)
	}

	// The engineer gets the approved plan and read access.
	f.implementFn = func(input ImplementInput) (string, error) {
		if !input.ReadDatabase || !strings.Contains(input.Context, "already applied on the development database") || !strings.Contains(input.Context, "dpb_x") {
			return "", fmt.Errorf("engineer input = %+v", input)
		}
		return "done", nil
	}
	expectStatus(t, f, f.run(t), BankAIReview)

	// Starting again with the same approved plan does not run it twice.
	f.status = "Ready To Implement"
	expectStatus(t, f, f.run(t), BankImplementing)
	if len(f.sql.ran) != len(want) {
		t.Fatalf("ran %d scripts, want the unchanged plan skipped", len(f.sql.ran))
	}
}

func TestReadyToImplementStopsWhenTheSQLPlanFails(t *testing.T) {
	f := approvedSQLIssue(t)
	f.sql = &fakeSQLRunner{failOn: "INSERT INTO smp_sys_trans"}
	f.status = "Ready To Implement"
	err := f.run(t)
	if err == nil || !strings.Contains(err.Error(), "ctdb dml failed") || f.status != string(BankReadyToImplement) {
		t.Fatalf("err = %v, status %q; want the failure and no move", err, f.status)
	}
	if body := f.lastComment(); !strings.Contains(body, "Failed:") || !strings.Contains(body, "duplicate entry") {
		t.Fatalf("comment = %q, want the failure posted", body)
	}

	// Without a development database the approved plan cannot run.
	f = approvedSQLIssue(t)
	f.status = "Ready To Implement"
	if err := f.run(t); err == nil || !strings.Contains(err.Error(), "DEV_DB_ENV_FILE") {
		t.Fatalf("err = %v, want the missing database reported", err)
	}

	// A database the server does not have is refused before anything runs.
	f = approvedSQLIssue(t)
	f.sql = &fakeSQLRunner{}
	f.pageBodies["NBS-12 SQL Migration Plan - Bulk withdrawal"] = confluence.MarkdownToStorage(testSQLPlan + "\n## marketingdb\n\n### DML\n\n```sql\nINSERT INTO m VALUES (1);\n```\n")
	f.status = "Ready To Implement"
	if err := f.run(t); err == nil || !strings.Contains(err.Error(), "no database marketingdb") || len(f.sql.ran) != 0 {
		t.Fatalf("err = %v ran = %v; want nothing run", err, f.sql.ran)
	}
}

func TestParseSQLPlanReadsReviewerEditsAndSplitCDATA(t *testing.T) {
	storage := `<p>intro</p><h2><code>cbsdb</code></h2><h3>Pre-check</h3><ac:structured-macro ac:name="code" ac:schema-version="1"><ac:parameter ac:name="language">sql</ac:parameter><ac:plain-text-body><![CDATA[SELECT ']]]]><![CDATA[>' FROM t]]></ac:plain-text-body></ac:structured-macro>` +
		`<h3>Notes</h3><ac:structured-macro ac:name="code"><ac:plain-text-body><![CDATA[not run]]></ac:plain-text-body></ac:structured-macro>` +
		`<h3>DML</h3><ac:structured-macro ac:name="code"><ac:plain-text-body><![CDATA[UPDATE t SET a = 1 WHERE b = 2]]></ac:plain-text-body></ac:structured-macro>` +
		`<h2>Summary</h2><p>No code.</p>`
	plans := parseSQLPlan(storage)
	if len(plans) != 1 || plans[0].Database != "cbsdb" {
		t.Fatalf("plans = %+v", plans)
	}
	if plans[0].Parts[sqlPreCheck] != "SELECT ']]>' FROM t;" || plans[0].forward() != "UPDATE t SET a = 1 WHERE b = 2;" {
		t.Fatalf("parts = %+v", plans[0].Parts)
	}
}

func TestBankWorkflowSteps(t *testing.T) {
	for _, test := range []struct {
		name   string
		status Status
		ai     bool
	}{
		{"request rfc", BankRequestRFC, true},
		{"  Request TRD ", BankRequestTRD, true},
		{"FRD Review", BankFSDReview, false},
		{"PR Review", BankPRReview, false},
		{"Engineer Review", BankEngineerReview, false},
		{"ai error", BankAIError, false},
	} {
		step, ok := BankWorkflow{}.Step(test.name)
		if !ok || step.Status != test.status || step.AI != test.ai {
			t.Errorf("Step(%q) = (%q, ai=%t, %t), want (%q, ai=%t)", test.name, step.Status, step.AI, ok, test.status, test.ai)
		}
	}
	if _, ok := (BankWorkflow{}).Step("Planning"); ok {
		t.Error("Step(Planning) matched a bank status")
	}
	if (BankWorkflow{}).ErrorStatus() != "AI ERROR" {
		t.Error("ErrorStatus() is not AI ERROR")
	}
}

func TestTickedReposPrefersTheRepositoriesField(t *testing.T) {
	f := newBankFake(t, "bnc_busi") // the description ticks bnc_busi
	bank := f.deps().Bank
	bank.RepoFieldID = "customfield_10100"

	f.issue.Fields = map[string]json.RawMessage{"customfield_10100": json.RawMessage(`[{"value":"sump-vue"},{"value":"bnc_iobus"}]`)}
	selected, err := tickedRepos(f.issue, bank)
	if err != nil || strings.Join(repoNames(selected), ",") != "sump-vue,bnc_iobus" {
		t.Fatalf("tickedRepos() = %q, %v, want the field's options", repoNames(selected), err)
	}

	f.issue.Fields = map[string]json.RawMessage{"customfield_10100": json.RawMessage(`null`)}
	selected, err = tickedRepos(f.issue, bank)
	if err != nil || strings.Join(repoNames(selected), ",") != "bnc_busi" {
		t.Fatalf("tickedRepos() with an empty field = %q, %v, want the description checklist", repoNames(selected), err)
	}

	f.issue.Fields = map[string]json.RawMessage{"customfield_10100": json.RawMessage(`[{"value":"qris-api"}]`)}
	if _, err := tickedRepos(f.issue, bank); err == nil || !strings.Contains(err.Error(), "unknown repositories qris-api") {
		t.Fatalf("tickedRepos() error = %v, want the unknown option reported", err)
	}
}

func TestBankAIReviewKeepsBranchesLocalWithoutGitLab(t *testing.T) {
	f := newBankFake(t, "bnc_busi")
	f.plan = PlanResult{Overview: "#", Repos: []RepoTRD{{Repo: "bnc_busi", TRD: "#"}}}
	for _, status := range []string{"Request RFC", "Request TRD", "Ready To Implement"} {
		f.status = status
		if err := f.run(t); err != nil {
			t.Fatal(err)
		}
	}
	deps := f.deps()
	deps.Bank.MergeRequests = nil
	for _, want := range []Status{BankAIReview, BankEngineerReview} {
		step, _ := BankWorkflow{}.Step(f.status)
		if err := step.Run(context.Background(), f.issue.Key, deps); err != nil || f.status != string(want) {
			t.Fatalf("status = %q, %v, want %q", f.status, err, want)
		}
	}
	if len(f.pushed) != 0 || len(f.removed) != 1 {
		t.Fatalf("pushed %v removed %v, want nothing pushed and the worktree removed", f.pushed, f.removed)
	}
	if text := f.descriptionText(t); !strings.Contains(text, "branch ready (local)") {
		t.Fatalf("description = %q, want the branch shown as local", text)
	}
}

func TestBankDocumentsReadTheBaseBranch(t *testing.T) {
	f := newBankFake(t, "bnc_busi", "sump-vue")
	f.plan = PlanResult{Overview: "#", Repos: []RepoTRD{{Repo: "bnc_busi", TRD: "#"}, {Repo: "sump-vue", TRD: "#"}}}
	for _, status := range []string{"Request RFC", "Request TRD"} {
		f.status = status
		if err := f.run(t); err != nil {
			t.Fatal(err)
		}
	}
	for name, list := range map[string][]repos.Repo{"RFC": f.documents[0].Repos, "TRD": f.plans[0].Repos} {
		for _, repo := range list {
			if repo.Path != "/reference/"+repo.Name {
				t.Fatalf("%s writer reads %s at %q, want the base branch checkout", name, repo.Name, repo.Path)
			}
		}
	}
	// Context repositories stay at their main checkout.
	if all := f.documents[0].AllRepos; all[0].Path != "/codes/bnc_busi" {
		t.Fatalf("AllRepos[0] = %+v, want the main checkout", all[0])
	}
}

func TestBankBranchNamesTheTicketAndWhatChanges(t *testing.T) {
	planned := documentsState{TRD: &trdRecord{BranchName: "Bulk overpayment withdraw batch"}}
	tests := []struct {
		name      string
		issue     jira.Issue
		documents documentsState
		prefix    string
		want      string
	}{
		{"ticket line and planner name", jira.Issue{Key: "NBS-9", Summary: "Batch", Description: "Bulk withdraw.\nTicket: CORE-312\nRefs feature_core_201."}, planned, "tody/feature", "tody/feature/core-312-bulk-overpayment-withdraw-batch"},
		{"labelled ticket only", jira.Issue{Key: "NBS-9", Description: "See core-201 for the upload.\n  ticket :core-77"}, planned, "tody/feature/", "tody/feature/core-77-bulk-overpayment-withdraw-batch"},
		{"no ticket line falls back to the key", jira.Issue{Key: "NBS-9", Description: "Like CORE-201."}, planned, "tody/feature", "tody/feature/nbs-9-bulk-overpayment-withdraw-batch"},
		{"no planner name uses the summary", jira.Issue{Key: "NBS-9", Summary: "Batch Overpayment Withdrawat (BE Only, Job/Batch Backend Only, No FE Needed)"}, documentsState{}, "", "feature/nbs-9-batch-overpayment-withdrawat-be-only-job-batch"},
	}
	for _, test := range tests {
		if got := bankBranch(test.issue, test.documents, test.prefix); got != test.want {
			t.Errorf("%s: bankBranch() = %q, want %q", test.name, got, test.want)
		}
	}
}
