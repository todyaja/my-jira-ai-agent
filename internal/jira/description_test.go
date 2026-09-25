package jira

import (
	"encoding/json"
	"testing"
)

func TestADFToPlainText(t *testing.T) {
	adf := json.RawMessage(`{"type":"doc","version":1,"content":[{"type":"paragraph","content":[{"type":"text","text":"First line"},{"type":"text","text":" continued"}]},{"type":"paragraph","content":[{"type":"text","text":"Second line"}]}]}`)

	if got, want := adfToPlainText(adf), "First line continued\nSecond line"; got != want {
		t.Fatalf("adfToPlainText() = %q, want %q", got, want)
	}
}

func TestADFToPlainTextEmptyDescription(t *testing.T) {
	for _, adf := range []json.RawMessage{nil, []byte(`null`), []byte(`{"type":"doc","version":1,"content":[]}`)} {
		if got := adfToPlainText(adf); got != "" {
			t.Errorf("adfToPlainText(%s) = %q, want empty", adf, got)
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AppendPRD(tt.description, tt.prd); got != tt.want {
				t.Fatalf("AppendPRD() = %q, want %q", got, tt.want)
			}
		})
	}
}
