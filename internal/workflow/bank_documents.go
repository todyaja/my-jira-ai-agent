package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/tody-aja/jira-ai-agent/internal/jira"
	"github.com/tody-aja/jira-ai-agent/internal/repos"
)

// DocumentsProperty is the issue property recording the bank documents
// published for the issue, so revisions update the same pages and files.
const DocumentsProperty = "jira-ai-agent.documents"

// DocumentKind is a document written from the bank's Word templates.
type DocumentKind string

const (
	RFCKind     DocumentKind = "rfc"
	FSDKind     DocumentKind = "fsd"
	RunbookKind DocumentKind = "runbook"
)

// SourceDocument is an approved document a writer builds on.
type SourceDocument struct {
	Title   string
	Content string
}

// DocumentInput asks for a new document, or a revision of the current one
// when Feedback is set.
type DocumentInput struct {
	Kind        DocumentKind
	IssueKey    string
	Summary     string
	Description string
	// Repos are the repositories the issue changes; AllRepos may be read for
	// context.
	Repos    []repos.Repo
	AllRepos []repos.Repo
	Sources  []SourceDocument
	// CurrentTitle, CurrentDocx and CurrentContent describe the published
	// document being revised; empty for a new one.
	CurrentTitle   string
	CurrentDocx    string
	CurrentContent string
	Feedback       []Feedback
}

// DocumentResult is a written document: a Word file built from the bank's
// template and a Markdown summary for its Confluence page.
type DocumentResult struct {
	Title   string
	Docx    string
	Summary string
}

// DocumentWriter writes RFCs, FSDs and runbooks.
type DocumentWriter interface {
	WriteDocument(context.Context, DocumentInput) (DocumentResult, error)
}

type documentRecord struct {
	Title   string   `json:"title"`
	URL     string   `json:"url"`
	PageID  string   `json:"pageId"`
	Parents []string `json:"parents"`
	Docx    string   `json:"docx,omitempty"`
}

type repoTRDRecord struct {
	Repo string `json:"repo"`
	documentRecord
}

type trdRecord struct {
	Overview   documentRecord  `json:"overview"`
	Repos      []repoTRDRecord `json:"repos"`
	MergeOrder []string        `json:"mergeOrder"`
	// BranchName is the planner's name for what changes, used in the
	// implementation branch.
	BranchName string `json:"branchName,omitempty"`
}

type documentsState struct {
	RFC     *documentRecord `json:"rfc,omitempty"`
	TRD     *trdRecord      `json:"trd,omitempty"`
	SQL     *documentRecord `json:"sql,omitempty"`
	// SQLApplied identifies the SQL plan last applied on the development
	// database.
	SQLApplied string `json:"sqlApplied,omitempty"`
	FSD     *documentRecord `json:"fsd,omitempty"`
	Runbook *documentRecord `json:"runbook,omitempty"`
}

func (s *documentsState) record(kind DocumentKind) **documentRecord {
	switch kind {
	case FSDKind:
		return &s.FSD
	case RunbookKind:
		return &s.Runbook
	default:
		return &s.RFC
	}
}

type documentKind struct {
	kind    DocumentKind
	name    string
	folder  string
	heading string
	review  pageReview
	// sources lists the approved documents the writer needs.
	sources func(ctx context.Context, key string, deps Dependencies, state documentsState) ([]SourceDocument, error)
}

var (
	rfcDocument = documentKind{kind: RFCKind, name: "RFC", folder: "RFCs", heading: jira.RFCHeading, review: rfcReview,
		sources: func(context.Context, string, Dependencies, documentsState) ([]SourceDocument, error) { return nil, nil }}
	fsdDocument = documentKind{kind: FSDKind, name: "FSD", folder: "FSDs", heading: jira.FSDHeading, review: fsdReview,
		sources: func(ctx context.Context, key string, deps Dependencies, state documentsState) ([]SourceDocument, error) {
			return collectSources(ctx, key, deps, state, true, false)
		}}
	runbookDocument = documentKind{kind: RunbookKind, name: "Runbook", folder: "Runbooks", heading: jira.RunbookHeading, review: runbookReview,
		sources: func(ctx context.Context, key string, deps Dependencies, state documentsState) ([]SourceDocument, error) {
			return collectSources(ctx, key, deps, state, true, true)
		}}
)

// executeDocument writes the issue's RFC, FSD or runbook, files it in
// Confluence with the Word file attached, links it from the issue and moves
// the issue to the document's review. An existing document is only revised
// for the open comments on its Confluence page.
func executeDocument(ctx context.Context, key string, kind documentKind, deps Dependencies) error {
	logger := deps.Logger
	fail := func(operation string, err error) error {
		logOperationFailure(logger, kind.name, key, operation, err, 0)
		return fmt.Errorf("%s: %w", operation, err)
	}
	if deps.Bank.Documents == nil || deps.Bank.Publisher == nil {
		return fail("configure", fmt.Errorf("no %s writer or Confluence publisher is configured", kind.name))
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
	current := *state.record(kind.kind)
	feedback := []Feedback{}
	if current != nil {
		if feedback, err = pageFeedback(ctx, deps.Bank.Publisher, []documentRecord{*current}); err != nil {
			return fail("get comments", err)
		}
		if len(feedback) == 0 {
			return kind.review.skipRevision(ctx, key, deps.Writer, logger)
		}
	}
	sources, err := kind.sources(ctx, key, deps, state)
	if err != nil {
		return fail("get source documents", err)
	}
	if ticked, err = baseCheckouts(ctx, deps.Bank, ticked); err != nil {
		return fail("check out base branches", err)
	}

	input := DocumentInput{
		Kind:        kind.kind,
		IssueKey:    issue.Key,
		Summary:     issue.Summary,
		Description: issue.Description,
		Repos:       ticked,
		AllRepos:    deps.Bank.Repos.All(),
		Sources:     sources,
		Feedback:    feedback,
	}
	if current != nil {
		input.CurrentTitle, input.CurrentDocx = current.Title, current.Docx
		if input.CurrentContent, _, err = deps.Bank.Publisher.PageContent(ctx, current.Title); err != nil {
			return fail("get current "+kind.name, err)
		}
	}

	startedAt := time.Now()
	if logger != nil {
		logger.Printf("%s generation started: issue=%s repos=%s feedback=%d", kind.name, key, strings.Join(repoNames(ticked), ","), len(feedback))
	}
	result, err := deps.Bank.Documents.WriteDocument(ctx, input)
	if err != nil {
		logOperationFailure(logger, kind.name, key, "write", err, time.Since(startedAt))
		return fmt.Errorf("write %s: %w", kind.name, err)
	}
	if logger != nil {
		logger.Printf("%s generation completed: issue=%s duration=%s", kind.name, key, time.Since(startedAt))
	}
	if strings.TrimSpace(result.Docx) == "" {
		return fail("check Word file", fmt.Errorf("the %s writer produced no Word file", kind.name))
	}
	if _, err := os.Stat(result.Docx); err != nil {
		return fail("check Word file", fmt.Errorf("the %s writer named %s, which cannot be read: %w", kind.name, result.Docx, err))
	}

	record := documentRecord{Title: documentTitle(result.Title, kind, issue), Parents: []string{kind.folder, kind.folder + " " + strconv.Itoa(deps.Bank.now().Year())}}
	if current != nil {
		// The page keeps its title and place, so links to it stay valid.
		record.Title, record.Parents = current.Title, current.Parents
	}
	record.Docx = result.Docx
	if err := kind.review.closeAll(ctx, key, deps.Bank.Publisher, feedback, logger); err != nil {
		return err
	}
	page, err := publishLabelled(ctx, deps.Bank.Publisher, record.Parents, record.Title, result.Summary, key)
	if err != nil {
		return fail("publish "+kind.name, err)
	}
	record.PageID, record.URL = page.ID, page.URL
	if err := deps.Bank.Publisher.AttachFile(ctx, page.ID, result.Docx); err != nil {
		return fail("attach Word file", err)
	}
	if logger != nil {
		logger.Printf("%s published: issue=%s url=%s", kind.name, key, page.URL)
	}

	*state.record(kind.kind) = &record
	if err := saveDocumentsState(ctx, deps.Writer, key, state); err != nil {
		return fail("save documents", err)
	}
	description, err := jira.SetDocumentLinks(issue.DescriptionADF, kind.heading, []jira.DocumentLink{{Label: record.Title, URL: record.URL}})
	if err != nil {
		return fail("build description", err)
	}
	if err := deps.Writer.UpdateDescription(ctx, key, description); err != nil {
		return fail("update description", err)
	}
	if err := deps.Writer.TransitionTo(ctx, key, string(kind.review.reviewStatus)); err != nil {
		return fail("transition", err)
	}
	return nil
}

// documentTitle uses the writer's title, which carries the document number,
// and makes sure the Jira key is in it so titles stay unique in the space.
func documentTitle(title string, kind documentKind, issue jira.Issue) string {
	title = strings.TrimSpace(title)
	if title == "" {
		title = kind.name + " " + issue.Summary
	}
	if !strings.Contains(title, issue.Key) {
		title += " [" + issue.Key + "]"
	}
	return title
}

func publishLabelled(ctx context.Context, publisher DocumentPublisher, parents []string, title, markdown, key string) (PublishedPage, error) {
	page, err := publisher.PublishPageUnder(ctx, parents, title, markdown)
	if err != nil {
		return PublishedPage{}, err
	}
	if err := publisher.AddLabel(ctx, page.ID, key); err != nil {
		return PublishedPage{}, fmt.Errorf("label page: %w", err)
	}
	return page, nil
}

// collectSources reads the approved RFC and TRDs, the SQL migration plan,
// plus the FSD for a runbook, and describes the implementation, for the FSD and runbook
// writers.
func collectSources(ctx context.Context, key string, deps Dependencies, state documentsState, needTRD, needFSD bool) ([]SourceDocument, error) {
	required := []*documentRecord{state.RFC}
	names := []string{"RFC"}
	if needFSD {
		required, names = append(required, state.FSD), append(names, "FSD")
	}
	sources := []SourceDocument{}
	for index, record := range required {
		if record == nil {
			return nil, fmt.Errorf("the issue has no published %s yet", names[index])
		}
		content, err := pageContent(ctx, deps.Bank.Publisher, record.Title)
		if err != nil {
			return nil, err
		}
		sources = append(sources, SourceDocument{Title: record.Title, Content: content})
	}
	if needTRD {
		if state.TRD == nil {
			return nil, fmt.Errorf("the issue has no published TRD yet")
		}
		titles := []string{state.TRD.Overview.Title}
		for _, repo := range state.TRD.Repos {
			titles = append(titles, repo.Title)
		}
		for _, title := range titles {
			content, err := pageContent(ctx, deps.Bank.Publisher, title)
			if err != nil {
				return nil, err
			}
			sources = append(sources, SourceDocument{Title: title, Content: content})
		}
	}
	if state.SQL != nil {
		content, err := pageContent(ctx, deps.Bank.Publisher, state.SQL.Title)
		if err != nil {
			return nil, err
		}
		sources = append(sources, SourceDocument{Title: state.SQL.Title, Content: content})
	}
	implementation, err := loadBankImplementation(ctx, deps.Reader, key)
	if err != nil {
		return nil, err
	}
	if len(implementation.Repos) > 0 {
		sources = append(sources, SourceDocument{Title: "Implementation", Content: implementationReport(implementation, state)})
	}
	return sources, nil
}

func pageContent(ctx context.Context, publisher DocumentPublisher, title string) (string, error) {
	content, found, err := publisher.PageContent(ctx, title)
	if err != nil {
		return "", fmt.Errorf("get page %q: %w", title, err)
	}
	if !found {
		return "", fmt.Errorf("no page titled %q", title)
	}
	return content, nil
}

func loadDocumentsState(ctx context.Context, reader IssueReader, key string) (documentsState, error) {
	var state documentsState
	raw, found, err := reader.GetIssueProperty(ctx, key, DocumentsProperty)
	if err != nil || !found {
		return state, err
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return state, fmt.Errorf("decode documents: %w", err)
	}
	return state, nil
}

func saveDocumentsState(ctx context.Context, writer IssueWriter, key string, state documentsState) error {
	value, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode documents: %w", err)
	}
	return writer.SetIssueProperty(ctx, key, DocumentsProperty, value)
}
