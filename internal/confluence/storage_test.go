package confluence

import (
	"encoding/xml"
	"io"
	"strings"
	"testing"
)

func TestMarkdownToStorage(t *testing.T) {
	tests := []struct {
		name     string
		markdown string
		want     string
	}{
		{
			name:     "headings and paragraphs",
			markdown: "## Problem\n\nUsers cannot\nexport data.\n\n### Detail ##",
			want:     "<h2>Problem</h2><p>Users cannot export data.</p><h3>Detail</h3>",
		},
		{
			name:     "escapes markup",
			markdown: `Use <script> & "quotes"`,
			want:     "<p>Use &lt;script&gt; &amp; &quot;quotes&quot;</p>",
		},
		{
			name:     "inline formatting",
			markdown: "**Must** call `run(a<b)` via [docs](https://example.com/a?b=1&c=2) not [x](javascript:alert(1))",
			want:     `<p><strong>Must</strong> call <code>run(a&lt;b)</code> via <a href="https://example.com/a?b=1&amp;c=2">docs</a> not [x](javascript:alert(1))</p>`,
		},
		{
			name:     "nested and mixed lists",
			markdown: "- one\n  - nested\n  continued\n- two\n1. first\n2. second",
			want:     "<ul><li>one<ul><li>nested continued</li></ul></li><li>two</li></ul><ol><li>first</li><li>second</li></ol>",
		},
		{
			name:     "table",
			markdown: "| Name | Value |\n|---|:---:|\n| a | **b** |",
			want:     "<table><tbody><tr><th>Name</th><th>Value</th></tr><tr><td>a</td><td><strong>b</strong></td></tr></tbody></table>",
		},
		{
			name:     "code block",
			markdown: "```go\nif a < b {}\n]]>\n```",
			want:     `<ac:structured-macro ac:name="code"><ac:plain-text-body><![CDATA[if a < b {}` + "\n" + `]]]]><![CDATA[>]]></ac:plain-text-body></ac:structured-macro>`,
		},
		{
			name:     "quote and rule",
			markdown: "> Assumed\n\n---",
			want:     "<blockquote><p>Assumed</p></blockquote><hr />",
		},
		{
			name:     "keeps unicode and drops control characters",
			markdown: "Café – ok\x01",
			want:     "<p>Café – ok</p>",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MarkdownToStorage(tt.markdown)
			if got != tt.want {
				t.Fatalf("MarkdownToStorage() =\n%s\nwant\n%s", got, tt.want)
			}
			assertWellFormed(t, got)
		})
	}
}

func TestMarkdownToStorageIsWellFormedForTypicalPRD(t *testing.T) {
	prd := "# PRD\n\n## Problem\nText with 5 > 3.\n\n## Goals\n- **Goal**: ship\n  1. step\n  2. step\n\n| A | B |\n|---|---|\n| x |\n\n```\nunclosed code"
	assertWellFormed(t, MarkdownToStorage(prd))
}

// assertWellFormed parses the storage value as XML, with a namespace
// declaration standing in for the one Confluence supplies for ac: elements.
func assertWellFormed(t *testing.T, storage string) {
	t.Helper()
	decoder := xml.NewDecoder(strings.NewReader(`<root xmlns:ac="urn:ac">` + storage + `</root>`))
	for {
		_, err := decoder.Token()
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatalf("storage is not well-formed XML: %v\n%s", err, storage)
		}
	}
}
