package jira

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// The managed block at the end of the description links the documents the
// agents publish. Each document is a heading paragraph, an underline
// paragraph and a link paragraph, in managedHeadings order.
const (
	prdHeading       = "PRD"
	trdHeading       = "TRD"
	managedUnderline = "============="
)

var managedHeadings = []string{prdHeading, trdHeading}

type adfDocument struct {
	Type    string            `json:"type"`
	Version int               `json:"version"`
	Content []json.RawMessage `json:"content,omitempty"`
}

type adfNode struct {
	Type    string            `json:"type"`
	Text    string            `json:"text,omitempty"`
	Content []json.RawMessage `json:"content,omitempty"`
}

func adfToPlainText(raw json.RawMessage) (string, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", nil
	}

	var document adfDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		return "", fmt.Errorf("decode ADF: %w", err)
	}
	if document.Type != "doc" || document.Version != 1 {
		return "", fmt.Errorf("unsupported ADF document")
	}

	var plainText strings.Builder
	if err := appendADFText(&plainText, document.Content); err != nil {
		return "", err
	}
	return strings.TrimRight(plainText.String(), "\n"), nil
}

func DescriptionText(raw json.RawMessage) (string, error) {
	return adfToPlainText(raw)
}

func appendADFText(output *strings.Builder, nodes []json.RawMessage) error {
	for _, raw := range nodes {
		var node adfNode
		if err := json.Unmarshal(raw, &node); err != nil {
			return fmt.Errorf("decode ADF node: %w", err)
		}
		if !isSupportedADFNode(node.Type) {
			return fmt.Errorf("unsupported ADF node %q", node.Type)
		}
		switch node.Type {
		case "text":
			output.WriteString(node.Text)
		case "hardBreak":
			output.WriteByte('\n')
		default:
			if err := appendADFText(output, node.Content); err != nil {
				return err
			}
		}
		if isADFBlock(node.Type) && output.Len() > 0 && !strings.HasSuffix(output.String(), "\n") {
			output.WriteByte('\n')
		}
	}
	return nil
}

func mustJSON(value any) json.RawMessage {
	raw, _ := json.Marshal(value)
	return raw
}

func isSupportedADFNode(nodeType string) bool {
	switch nodeType {
	case "text", "hardBreak", "link", "inlineCard", "mention", "emoji", "date", "status", "inlineExtension", "paragraph", "heading", "listItem", "bulletList", "orderedList", "blockquote", "panel", "expand", "table", "tableRow", "tableCell", "tableHeader", "mediaSingle", "mediaGroup", "media", "codeBlock", "rule", "decisionList", "decisionItem", "taskList", "taskItem", "extension", "bodiedExtension", "blockCard":
		return true
	default:
		return false
	}
}

func isADFBlock(nodeType string) bool {
	switch nodeType {
	case "paragraph", "heading", "listItem", "bulletList", "orderedList", "blockquote", "panel", "expand", "table", "tableRow", "tableCell", "tableHeader", "mediaSingle", "mediaGroup", "media", "codeBlock", "rule", "decisionList", "decisionItem", "taskList", "taskItem", "extension", "bodiedExtension", "blockCard":
		return true
	default:
		return false
	}
}

// AppendPRDLinkToADF sets the PRD link in the managed block at the end of
// the description, preserving everything before the block and the other
// document links in it.
func AppendPRDLinkToADF(raw json.RawMessage, pageURL string) (json.RawMessage, error) {
	return setDocumentLink(raw, prdHeading, pageURL)
}

// AppendTRDLinkToADF sets the TRD link in the managed block, below the PRD
// link.
func AppendTRDLinkToADF(raw json.RawMessage, pageURL string) (json.RawMessage, error) {
	return setDocumentLink(raw, trdHeading, pageURL)
}

func setDocumentLink(raw json.RawMessage, heading, pageURL string) (json.RawMessage, error) {
	if _, err := adfToPlainText(raw); err != nil {
		return nil, err
	}

	document := adfDocument{Type: "doc", Version: 1, Content: []json.RawMessage{}}
	if len(bytes.TrimSpace(raw)) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if err := json.Unmarshal(raw, &document); err != nil {
			return nil, fmt.Errorf("decode ADF: %w", err)
		}
	}

	// Everything from the first managed heading on belongs to the block;
	// nodes are kept under the heading they follow.
	managedAt := -1
	sections := map[string][]json.RawMessage{}
	current := ""
	for index := 0; index < len(document.Content); index++ {
		if index+1 < len(document.Content) {
			name, err := managedHeadingAt(document.Content[index], document.Content[index+1])
			if err != nil {
				return nil, err
			}
			if name != "" {
				if managedAt < 0 {
					managedAt = index
				}
				current = name
				sections[current] = nil
				index++
				continue
			}
		}
		if managedAt >= 0 {
			sections[current] = append(sections[current], document.Content[index])
		}
	}
	if managedAt >= 0 {
		document.Content = document.Content[:managedAt]
	}

	sections[heading] = []json.RawMessage{linkParagraph(pageURL)}
	for _, name := range managedHeadings {
		body, ok := sections[name]
		if !ok {
			continue
		}
		document.Content = append(document.Content, textParagraph(name), textParagraph(managedUnderline))
		document.Content = append(document.Content, body...)
	}
	updated, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode ADF: %w", err)
	}
	return updated, nil
}

// managedHeadingAt returns the document heading that first and second
// start, or "" when they do not start a managed section.
func managedHeadingAt(first, second json.RawMessage) (string, error) {
	heading, err := adfNodePlainText(first)
	if err != nil {
		return "", err
	}
	underline, err := adfNodePlainText(second)
	if err != nil {
		return "", err
	}
	if underline != managedUnderline {
		return "", nil
	}
	for _, name := range managedHeadings {
		if heading == name {
			return name, nil
		}
	}
	return "", nil
}

func textParagraph(text string) json.RawMessage {
	return mustJSON(adfNode{Type: "paragraph", Content: []json.RawMessage{mustJSON(adfNode{Type: "text", Text: text})}})
}

func linkParagraph(href string) json.RawMessage {
	link := map[string]any{
		"type":  "text",
		"text":  href,
		"marks": []any{map[string]any{"type": "link", "attrs": map[string]string{"href": href}}},
	}
	return mustJSON(adfNode{Type: "paragraph", Content: []json.RawMessage{mustJSON(link)}})
}

func adfNodePlainText(raw json.RawMessage) (string, error) {
	var output strings.Builder
	if err := appendADFText(&output, []json.RawMessage{raw}); err != nil {
		return "", err
	}
	return strings.TrimRight(output.String(), "\n"), nil
}
