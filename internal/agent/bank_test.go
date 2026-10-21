package agent

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tody-aja/jira-ai-agent/internal/repos"
	"github.com/tody-aja/jira-ai-agent/internal/workflow"
)

func TestParsePlan(t *testing.T) {
	answer := "Here are the TRDs.\n<<<OVERVIEW>>>\n## Summary\nCross-repo.\n<<< MERGE ORDER >>>\n`bnc_iobus`, bnc_busi ,\n<<<REPO bnc_busi | Batch & transaksi bulk>>>\n## Summary\nBusi.\n\n<<<REPO sump-vue|Halaman upload>>>\n## Summary\r\nVue.\n"
	plan, err := parsePlan(answer)
	if err != nil {
		t.Fatalf("parsePlan() error = %v", err)
	}
	want := workflow.PlanResult{
		Overview:   "## Summary\nCross-repo.",
		MergeOrder: []string{"bnc_iobus", "bnc_busi"},
		Repos: []workflow.RepoTRD{
			{Repo: "bnc_busi", Title: "Batch & transaksi bulk", TRD: "## Summary\nBusi."},
			{Repo: "sump-vue", Title: "Halaman upload", TRD: "## Summary\nVue."},
		},
	}
	if !reflect.DeepEqual(plan, want) {
		t.Fatalf("parsePlan() = %+v, want %+v", plan, want)
	}
	if _, err := parsePlan("## Summary only, no markers"); err == nil {
		t.Fatal("parsePlan() accepted an answer without sections")
	}
}

func TestParseDocument(t *testing.T) {
	answer := "Done.\n<<<TITLE>>>\nRFC-2026-11-Bulk Overpayment Withdrawal\n<<<DOCX>>>\n`C:\\Codes\\RFCs\\RFC-2026-11-Bulk Overpayment Withdrawal.docx`\n<<<SUMMARY>>>\n## Latar Belakang\nIsi."
	got, err := parseDocument(answer)
	if err != nil {
		t.Fatalf("parseDocument() error = %v", err)
	}
	want := workflow.DocumentResult{Title: "RFC-2026-11-Bulk Overpayment Withdrawal", Docx: `C:\Codes\RFCs\RFC-2026-11-Bulk Overpayment Withdrawal.docx`, Summary: "## Latar Belakang\nIsi."}
	if got != want {
		t.Fatalf("parseDocument() = %+v, want %+v", got, want)
	}
	if _, err := parseDocument("<<<TITLE>>>\nx"); err == nil {
		t.Fatal("parseDocument() accepted an answer without a Word file")
	}
}

func TestBankPrompts(t *testing.T) {
	changed := []repos.Repo{{Name: "bnc_busi", Path: `C:\Codes\bnc_busi_parent\bnc_busi`, BaseBranch: "master"}}
	all := append([]repos.Repo{{Name: "bnc_iobus", Path: `C:\Codes\bnc_iobus`, BaseBranch: "master"}}, changed...)

	plan := repoPlanPrompt(workflow.PlanInput{IssueKey: "NBS-12", Summary: "Bulk", RFC: "<p>RFC</p>", Repos: changed, AllRepos: all})
	for _, want := range []string{"changes:\n- bnc_busi: C:\\Codes\\bnc_busi_parent\\bnc_busi (base branch master)", "must not plan changes in:\n- bnc_iobus", "<p>RFC</p>", "<<<REPO name | what changes"} {
		if !strings.Contains(plan, want) {
			t.Fatalf("repoPlanPrompt() = %q, want %q", plan, want)
		}
	}
	if strings.Contains(plan, "Current overview") {
		t.Fatal("a first plan asked for a revision")
	}
	revision := repoPlanPrompt(workflow.PlanInput{CurrentOverview: "<p>old</p>", Current: []workflow.RepoTRD{{Repo: "bnc_busi", Title: "Batch", TRD: "<p>busi</p>"}}, Feedback: []workflow.Feedback{{Text: "use the batch framework"}}})
	if !strings.Contains(revision, "[bnc_busi - Batch]\n<p>busi</p>") || !strings.Contains(revision, "use the batch framework") {
		t.Fatalf("revision prompt = %q", revision)
	}

	document := documentPrompt(workflow.DocumentInput{Kind: workflow.FSDKind, IssueKey: "NBS-12", Repos: changed, AllRepos: all, Sources: []workflow.SourceDocument{{Title: "RFC-2026-11", Content: "<p>rfc</p>"}}})
	for _, want := range []string{"Write the FSD", "Bahasa Indonesia", "Read the current code", "Approved source - RFC-2026-11:\n<p>rfc</p>", "<<<DOCX>>>"} {
		if !strings.Contains(document, want) {
			t.Fatalf("documentPrompt() = %q, want %q", document, want)
		}
	}
	revised := documentPrompt(workflow.DocumentInput{Kind: workflow.RFCKind, CurrentTitle: "RFC-2026-11 Bulk [NBS-12]", CurrentDocx: `C:\Codes\RFCs\x.docx`, Feedback: []workflow.Feedback{{Text: "tambahkan alur lama"}}})
	if !strings.Contains(revised, `revision of the published RFC titled "RFC-2026-11 Bulk [NBS-12]", built as C:\Codes\RFCs\x.docx`) || !strings.Contains(revised, "tambahkan alur lama") {
		t.Fatalf("revision prompt = %q", revised)
	}
}

func TestRepoChangesCatchesEdits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	if err := exec.Command("git", "init", "-q", dir).Run(); err != nil {
		t.Fatal(err)
	}
	list := []repos.Repo{{Name: "bnc_busi", Path: dir}}
	before := repoChanges(context.Background(), list)
	if changed := changedRepos(before, repoChanges(context.Background(), list)); len(changed) != 0 {
		t.Fatalf("changedRepos() = %q with no edits", changed)
	}
	if err := os.WriteFile(filepath.Join(dir, "Edited.java"), []byte("class Edited {}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if changed := changedRepos(before, repoChanges(context.Background(), list)); !reflect.DeepEqual(changed, []string{"bnc_busi"}) {
		t.Fatalf("changedRepos() = %q, want the edited repository", changed)
	}
}

func TestDocumentWriterArguments(t *testing.T) {
	writer, err := NewClaudeDocumentWriter(ClaudeConfig{Executable: os.Args[0]}, `C:\Codes`, []string{`C:\Users\me\.claude\skills`})
	if err != nil {
		t.Fatal(err)
	}
	args := writer.cli.args(claudeRun{agent: RFCWriterAgent, tools: documentWriterTools, permissionMode: "acceptEdits", allowedTools: bashRules(documentWriterCommands), addDirs: writer.addDirs})
	for _, pair := range [][]string{
		{"--agent", "jira-ai-agent:rfc-writer"},
		{"--allowedTools", "Bash(node:*),Bash(java:*),Bash(git log:*),Bash(git show:*),Bash(git diff:*)"},
		{"--add-dir", `C:\Users\me\.claude\skills`},
	} {
		if !containsPair(args, pair) {
			t.Fatalf("args = %q, want %q", args, pair)
		}
	}
}

func TestParsePlanJoinsSplitTRDsAndIgnoresFencedMarkers(t *testing.T) {
	answer := "<<<OVERVIEW>>>\nOverview.\n```text\n<<<REPO name | example of the format>>>\n```\n<<<REPO bnc_busi | Remark hapus buku>>>\n## Summary\nPart one.\n<<<REPO BNC_BUSI | ignored title>>>\n## Testing Strategy\nPart two.\n<<<MERGE ORDER>>>\nbnc_busi"
	plan, err := parsePlan(answer)
	if err != nil {
		t.Fatalf("parsePlan() error = %v", err)
	}
	if len(plan.Repos) != 1 || plan.Repos[0].Title != "Remark hapus buku" || plan.Repos[0].TRD != "## Summary\nPart one.\n\n## Testing Strategy\nPart two." {
		t.Fatalf("repos = %+v, want one bnc_busi TRD joined from both parts", plan.Repos)
	}
	if !strings.Contains(plan.Overview, "<<<REPO name | example of the format>>>") {
		t.Fatalf("overview = %q, want the fenced example kept as text", plan.Overview)
	}
}
