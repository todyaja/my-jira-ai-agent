package jira

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBankSectionsKeepOrderAndUserContent(t *testing.T) {
	description := json.RawMessage(checklistDescription)

	withTRD, err := SetDocumentLinks(description, TRDHeading, []DocumentLink{
		{Label: "Overview", URL: "https://wiki/trd"},
		{Label: "bnc_busi", URL: "https://wiki/trd-busi"},
	})
	if err != nil {
		t.Fatalf("SetDocumentLinks(TRD) error = %v", err)
	}
	withStatus, err := SetRepositoryStatus(withTRD, [][]string{
		{"Repository", "Merge request"},
		{"bnc_busi", "https://git/mr/1"},
		{"sump-vue", ""},
	}, "Merge order: bnc_busi -> sump-vue")
	if err != nil {
		t.Fatalf("SetRepositoryStatus() error = %v", err)
	}
	withRFC, err := SetDocumentLinks(withStatus, RFCHeading, []DocumentLink{{Label: "RFC-2026-11", URL: "https://wiki/rfc"}})
	if err != nil {
		t.Fatalf("SetDocumentLinks(RFC) error = %v", err)
	}

	text, err := DescriptionText(withRFC)
	if err != nil {
		t.Fatalf("DescriptionText() error = %v", err)
	}
	// The user's checklist stays first and the sections follow in the managed
	// order, whatever order they were written in.
	order := []string{"Repositories:", "bnc_busi", "RFC\n" + managedUnderline, "RFC-2026-11: https://wiki/rfc", "TRD\n" + managedUnderline, "Overview: https://wiki/trd", "bnc_busi: https://wiki/trd-busi", "REPOSITORY STATUS\n" + managedUnderline, "https://git/mr/1", "Merge order"}
	position := 0
	for _, want := range order {
		index := strings.Index(text[position:], want)
		if index < 0 {
			t.Fatalf("description text = %q, want %q after position %d", text, want, position)
		}
		position += index + len(want)
	}
	checked, err := CheckedItems(withRFC)
	if err != nil || len(checked) != 2 {
		t.Fatalf("CheckedItems() after updates = %q, %v, want the checklist untouched", checked, err)
	}

	// Rewriting a section replaces it instead of adding another.
	again, err := SetRepositoryStatus(withRFC, [][]string{{"Repository"}, {"bnc_busi"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if text, _ := DescriptionText(again); strings.Count(text, "REPOSITORY STATUS") != 1 || strings.Contains(text, "Merge order") {
		t.Fatalf("description after rewrite = %q, want one status section without the old note", text)
	}
	if strings.Contains(string(again), `"text":""`) {
		t.Fatal("an empty table cell produced an empty text node, which Jira rejects")
	}
}
