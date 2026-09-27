package jira

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestADFToPlainText(t *testing.T) {
	adf := json.RawMessage(`{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"First line"},{"type":"text","text":" continued"}]},{"type":"paragraph","content":[{"type":"text","text":"Second line"}]}]}`)

	got, err := adfToPlainText(adf)
	if err != nil {
		t.Fatalf("adfToPlainText() error = %v", err)
	}
	if want := "First line continued\nSecond line"; got != want {
		t.Fatalf("adfToPlainText() = %q, want %q", got, want)
	}
}

func TestADFToPlainTextTraversesHeadingsListsBlockquotesAndNestedNodes(t *testing.T) {
	adf := json.RawMessage(`{"type":"doc","version":1,"content":[{"type":"heading","content":[{"type":"text","text":"Heading"}]},{"type":"bulletList","content":[{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"First item"},{"type":"hardBreak"},{"type":"link","content":[{"type":"text","text":"continued"}]}]}]},{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"Second item"}]}]}]},{"type":"blockquote","content":[{"type":"paragraph","content":[{"type":"text","text":"Quoted"}]}]}]}`)

	got, err := adfToPlainText(adf)
	if err != nil {
		t.Fatalf("adfToPlainText() error = %v", err)
	}
	if want := "Heading\nFirst item\ncontinued\nSecond item\nQuoted"; got != want {
		t.Fatalf("adfToPlainText() = %q, want %q", got, want)
	}
}

func TestADFToPlainTextEmptyDescription(t *testing.T) {
	for _, adf := range []json.RawMessage{nil, []byte(`null`), []byte(`{"type":"doc","version":1,"content":[]}`)} {
		got, err := adfToPlainText(adf)
		if err != nil {
			t.Fatalf("adfToPlainText(%s) error = %v", adf, err)
		}
		if got != "" {
			t.Errorf("adfToPlainText(%s) = %q, want empty", adf, got)
		}
	}
}

func TestADFToPlainTextRejectsInvalidOrUnsupportedADF(t *testing.T) {
	for _, raw := range []json.RawMessage{
		[]byte(`{"type":"paragraph","version":1}`),
		[]byte(`{"type":"doc","version":1,"content":[{"type":"unsupported"}]}`),
		[]byte(`not json`),
	} {
		if _, err := adfToPlainText(raw); err == nil {
			t.Errorf("adfToPlainText(%s) error = nil, want validation error", raw)
		}
	}
}

func TestAppendPRDLinkToADFPreservesRichContentAndReplacesManagedBlock(t *testing.T) {
	original := json.RawMessage(`{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"Human "},{"type":"text","text":"description","marks":[{"type":"strong"}]}]},{"type":"table","attrs":{"layout":"default"},"content":[{"type":"tableRow","content":[]}]},{"type":"paragraph","content":[{"type":"text","text":"PRD"}]},{"type":"paragraph","content":[{"type":"text","text":"============="}]},{"type":"paragraph","content":[{"type":"text","text":"Old PRD"}]},{"type":"paragraph","content":[{"type":"text","text":"more old PRD"}]}]}`)

	updated, err := AppendPRDLinkToADF(original, "https://example.atlassian.net/wiki/spaces/ENG/pages/1")
	if err != nil {
		t.Fatalf("AppendPRDLinkToADF() error = %v", err)
	}
	var document struct {
		Content []json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(updated, &document); err != nil {
		t.Fatalf("decode updated ADF: %v", err)
	}
	if len(document.Content) != 5 {
		t.Fatalf("content nodes = %d, want 5: %s", len(document.Content), updated)
	}
	if !strings.Contains(string(document.Content[0]), `"marks":[{"type":"strong"}]`) || !strings.Contains(string(document.Content[1]), `"type":"table"`) {
		t.Fatalf("rich content was not preserved: %s", updated)
	}
	if strings.Contains(string(updated), "Old PRD") {
		t.Fatalf("managed block was not replaced: %s", updated)
	}
}

func TestAppendPRDLinkToADFWritesMarkerAndLink(t *testing.T) {
	pageURL := "https://example.atlassian.net/wiki/spaces/ENG/pages/1"
	updated, err := AppendPRDLinkToADF(nil, pageURL)
	if err != nil {
		t.Fatalf("AppendPRDLinkToADF() error = %v", err)
	}

	var document struct {
		Content []struct {
			Type    string `json:"type"`
			Content []struct {
				Type  string `json:"type"`
				Text  string `json:"text"`
				Marks []struct {
					Type  string `json:"type"`
					Attrs struct {
						Href string `json:"href"`
					} `json:"attrs"`
				} `json:"marks"`
			} `json:"content"`
		} `json:"content"`
	}
	if err := json.Unmarshal(updated, &document); err != nil {
		t.Fatalf("decode ADF: %v", err)
	}
	if len(document.Content) != 3 {
		t.Fatalf("content nodes = %d, want 3: %s", len(document.Content), updated)
	}
	for index, want := range []string{"PRD", "=============", pageURL} {
		paragraph := document.Content[index]
		if paragraph.Type != "paragraph" || len(paragraph.Content) != 1 || paragraph.Content[0].Text != want {
			t.Fatalf("paragraph %d = %+v, want text %q", index, paragraph, want)
		}
	}
	marks := document.Content[2].Content[0].Marks
	if len(marks) != 1 || marks[0].Type != "link" || marks[0].Attrs.Href != pageURL {
		t.Fatalf("link marks = %+v, want link to %q", marks, pageURL)
	}

	text, err := DescriptionText(updated)
	if err != nil || text != "PRD\n=============\n"+pageURL {
		t.Fatalf("DescriptionText() = (%q, %v), want marker and link", text, err)
	}
}

func TestAppendPRDLinkToADFRejectsUnsupportedADF(t *testing.T) {
	if _, err := AppendPRDLinkToADF(json.RawMessage(`{"type":"doc","version":1,"content":[{"type":"unsupported"}]}`), "https://example.com"); err == nil {
		t.Fatal("AppendPRDLinkToADF() error = nil, want validation error")
	}
}

func TestAppendTRDLinkToADFAddsTRDBelowPRD(t *testing.T) {
	prdURL := "https://example.atlassian.net/wiki/prd"
	trdURL := "https://example.atlassian.net/wiki/trd"
	withPRD, err := AppendPRDLinkToADF(json.RawMessage(`{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"Human description"}]}]}`), prdURL)
	if err != nil {
		t.Fatalf("AppendPRDLinkToADF() error = %v", err)
	}

	withTRD, err := AppendTRDLinkToADF(withPRD, trdURL)
	if err != nil {
		t.Fatalf("AppendTRDLinkToADF() error = %v", err)
	}
	text, _ := DescriptionText(withTRD)
	if want := "Human description\nPRD\n=============\n" + prdURL + "\nTRD\n=============\n" + trdURL; text != want {
		t.Fatalf("description = %q, want %q", text, want)
	}

	// Regenerating either document replaces only its own link.
	again, err := AppendTRDLinkToADF(withTRD, trdURL+"?v=2")
	if err != nil {
		t.Fatalf("AppendTRDLinkToADF() again error = %v", err)
	}
	revised, err := AppendPRDLinkToADF(again, prdURL+"?v=2")
	if err != nil {
		t.Fatalf("AppendPRDLinkToADF() after TRD error = %v", err)
	}
	text, _ = DescriptionText(revised)
	if want := "Human description\nPRD\n=============\n" + prdURL + "?v=2\nTRD\n=============\n" + trdURL + "?v=2"; text != want {
		t.Fatalf("description = %q, want %q", text, want)
	}
}

func TestAppendTRDLinkToADFWithoutPRDStillWritesTRD(t *testing.T) {
	updated, err := AppendTRDLinkToADF(nil, "https://example.com/trd")
	if err != nil {
		t.Fatalf("AppendTRDLinkToADF() error = %v", err)
	}
	if text, _ := DescriptionText(updated); text != "TRD\n=============\nhttps://example.com/trd" {
		t.Fatalf("description = %q, want TRD block only", text)
	}
}
