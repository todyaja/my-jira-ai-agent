package jira

import (
	"strings"
	"testing"
)

func TestMarkdownADFConvertsBlocks(t *testing.T) {
	adf := MarkdownADF("## Findings\n\n1. **Blocking** - missing test\n2. Rename `x`\n\n- a\n- b\n\nFirst line\nsecond line\n\n```ts\nconst x = 1\n```")

	text, err := DescriptionText(adf)
	if err != nil {
		t.Fatalf("MarkdownADF() is not valid ADF: %v", err)
	}
	if want := "Findings\n**Blocking** - missing test\nRename `x`\na\nb\nFirst line\nsecond line\nconst x = 1"; text != want {
		t.Fatalf("text = %q, want %q", text, want)
	}
	for _, want := range []string{`"type":"heading"`, `"level":2`, `"type":"orderedList"`, `"type":"bulletList"`, `"type":"hardBreak"`, `"type":"codeBlock"`} {
		if !strings.Contains(string(adf), want) {
			t.Fatalf("ADF = %s, want %s", adf, want)
		}
	}
}

func TestMarkdownADFTruncatesLongText(t *testing.T) {
	adf := MarkdownADF(strings.Repeat("a", maxCommentRunes+10))
	if !strings.Contains(string(adf), "(truncated)") {
		t.Fatal("MarkdownADF() did not mark truncated text")
	}
}
