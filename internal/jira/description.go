package jira

import (
	"encoding/json"
	"strings"
)

const prdMarker = "PRD\n=============\n"

type adfDocument struct {
	Type    string    `json:"type"`
	Version int       `json:"version"`
	Content []adfNode `json:"content,omitempty"`
}

type adfNode struct {
	Type    string    `json:"type"`
	Text    string    `json:"text,omitempty"`
	Content []adfNode `json:"content,omitempty"`
}

func adfToPlainText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}

	var document adfDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		return ""
	}

	var plainText strings.Builder
	appendADFText(&plainText, document.Content)
	return strings.TrimRight(plainText.String(), "\n")
}

func appendADFText(output *strings.Builder, nodes []adfNode) {
	for _, node := range nodes {
		switch node.Type {
		case "text":
			output.WriteString(node.Text)
		case "hardBreak":
			output.WriteByte('\n')
		default:
			appendADFText(output, node.Content)
		}
		if isADFBlock(node.Type) && output.Len() > 0 && !strings.HasSuffix(output.String(), "\n") {
			output.WriteByte('\n')
		}
	}
}

func plainTextToADF(description string) adfDocument {
	lines := strings.Split(description, "\n")
	document := adfDocument{Type: "doc", Version: 1, Content: make([]adfNode, 0, len(lines))}
	for _, line := range lines {
		document.Content = append(document.Content, adfNode{
			Type:    "paragraph",
			Content: []adfNode{{Type: "text", Text: line}},
		})
	}
	return document
}

func isADFBlock(nodeType string) bool {
	switch nodeType {
	case "paragraph", "heading", "listItem", "bulletList", "orderedList", "blockquote", "panel", "expand", "table", "tableRow", "tableCell", "mediaSingle":
		return true
	default:
		return false
	}
}

func AppendPRD(description, prd string) string {
	prefix := description
	if markerIndex := strings.Index(description, prdMarker); markerIndex >= 0 {
		prefix = description[:markerIndex]
	}
	if prefix != "" && !strings.HasSuffix(prefix, "\n") {
		prefix += "\n"
	}
	return prefix + prdMarker + prd
}
