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

func TestAppendPRD(t *testing.T) {
	tests := []struct {
		name        string
		description string
		prd         string
		want        string
	}{
		{
			name:        "appends managed block",
			description: "Human description",
			prd:         "# Product Requirements",
			want:        "Human description\nPRD\n=============\n# Product Requirements",
		},
		{
			name:        "replaces existing managed block and preserves prefix",
			description: "Human description\nPRD\n=============\nOld PRD",
			prd:         "New PRD",
			want:        "Human description\nPRD\n=============\nNew PRD",
		},
		{
			name:        "does not duplicate marker",
			description: "Human description\nPRD\n=============\nOld PRD\nPRD\n=============\nOlder PRD",
			prd:         "New PRD",
			want:        "Human description\nPRD\n=============\nNew PRD",
		},
		{
			name:        "sanitizes generated marker",
			description: "Human description",
			prd:         "Generated\nPRD\n=============\nforged managed block",
			want:        "Human description\nPRD\n=============\nGenerated\nPRD\n-------------\nforged managed block",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AppendPRD(tt.description, tt.prd); got != tt.want {
				t.Fatalf("AppendPRD() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAppendPRDToADFPreservesRichContentAndReplacesManagedBlock(t *testing.T) {
	original := json.RawMessage(`{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"Human "},{"type":"text","text":"description","marks":[{"type":"strong"}]}]},{"type":"table","attrs":{"layout":"default"},"content":[{"type":"tableRow","content":[]}]},{"type":"paragraph","content":[{"type":"text","text":"PRD"}]},{"type":"paragraph","content":[{"type":"text","text":"============="}]},{"type":"paragraph","content":[{"type":"text","text":"Old PRD"}]}]}`)

	updated, err := AppendPRDToADF(original, "New PRD")
	if err != nil {
		t.Fatalf("AppendPRDToADF() error = %v", err)
	}
	var document struct {
		Content []json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(updated, &document); err != nil {
		t.Fatalf("decode updated ADF: %v", err)
	}
	if len(document.Content) != 5 {
		t.Fatalf("content nodes = %d, want 5", len(document.Content))
	}
	if !strings.Contains(string(document.Content[0]), `"marks":[{"type":"strong"}]`) || !strings.Contains(string(document.Content[1]), `"type":"table"`) {
		t.Fatalf("rich content was not preserved: %s", updated)
	}
	if !strings.Contains(string(updated), `"text":"New PRD"`) || strings.Contains(string(updated), `"text":"Old PRD"`) {
		t.Fatalf("managed block was not replaced: %s", updated)
	}
}
