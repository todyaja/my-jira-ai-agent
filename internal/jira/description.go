package jira

import (
	"encoding/json"
	"strings"
)

const prdMarker = "PRD\n=============\n"

type adfDocument struct {
	Type    string     `json:"type"`
	Version int        `json:"version"`
	Content []adfBlock `json:"content,omitempty"`
}

type adfBlock struct {
	Type    string    `json:"type"`
	Content []adfText `json:"content,omitempty"`
}

type adfText struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

func adfToPlainText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}

	var document adfDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		return ""
	}

	paragraphs := make([]string, 0, len(document.Content))
	for _, block := range document.Content {
		texts := make([]string, 0, len(block.Content))
		for _, node := range block.Content {
			if node.Type == "text" {
				texts = append(texts, node.Text)
			}
		}
		if block.Type == "paragraph" {
			paragraphs = append(paragraphs, strings.Join(texts, ""))
		}
	}

	return strings.Join(paragraphs, "\n")
}

func plainTextToADF(description string) adfDocument {
	lines := strings.Split(description, "\n")
	document := adfDocument{Type: "doc", Version: 1, Content: make([]adfBlock, 0, len(lines))}
	for _, line := range lines {
		document.Content = append(document.Content, adfBlock{
			Type:    "paragraph",
			Content: []adfText{{Type: "text", Text: line}},
		})
	}
	return document
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
