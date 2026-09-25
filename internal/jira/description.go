package jira

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

const prdMarker = "PRD\n=============\n"

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

func plainTextToADF(description string) adfDocument {
	lines := strings.Split(description, "\n")
	document := adfDocument{Type: "doc", Version: 1, Content: make([]json.RawMessage, 0, len(lines))}
	for _, line := range lines {
		node, _ := json.Marshal(adfNode{Type: "paragraph", Content: []json.RawMessage{mustJSON(adfNode{Type: "text", Text: line})}})
		document.Content = append(document.Content, node)
	}
	return document
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

func AppendPRD(description, prd string) string {
	prefix := description
	if markerIndex := strings.Index(description, prdMarker); markerIndex >= 0 {
		prefix = description[:markerIndex]
	}
	if prefix != "" && !strings.HasSuffix(prefix, "\n") {
		prefix += "\n"
	}
	return prefix + prdMarker + sanitizePRD(prd)
}

func AppendPRDToADF(raw json.RawMessage, prd string) (json.RawMessage, error) {
	if _, err := adfToPlainText(raw); err != nil {
		return nil, err
	}

	document := adfDocument{Type: "doc", Version: 1, Content: []json.RawMessage{}}
	if len(bytes.TrimSpace(raw)) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if err := json.Unmarshal(raw, &document); err != nil {
			return nil, fmt.Errorf("decode ADF: %w", err)
		}
	}

	managedAt := -1
	for index := 0; index+1 < len(document.Content); index++ {
		first, err := adfNodePlainText(document.Content[index])
		if err != nil {
			return nil, err
		}
		second, err := adfNodePlainText(document.Content[index+1])
		if err != nil {
			return nil, err
		}
		if first == "PRD" && second == "=============" {
			managedAt = index
			break
		}
	}
	if managedAt >= 0 {
		document.Content = document.Content[:managedAt]
	}
	managed := plainTextToADF(prdMarker + sanitizePRD(prd))
	document.Content = append(document.Content, managed.Content...)
	updated, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("encode ADF: %w", err)
	}
	return updated, nil
}

func adfNodePlainText(raw json.RawMessage) (string, error) {
	var node adfNode
	if err := json.Unmarshal(raw, &node); err != nil {
		return "", fmt.Errorf("decode ADF node: %w", err)
	}
	var output strings.Builder
	if err := appendADFText(&output, []json.RawMessage{raw}); err != nil {
		return "", err
	}
	return strings.TrimRight(output.String(), "\n"), nil
}

func sanitizePRD(prd string) string {
	lines := strings.Split(prd, "\n")
	for index := 0; index+1 < len(lines); index++ {
		if lines[index] == "PRD" && lines[index+1] == "=============" {
			lines[index+1] = "-------------"
		}
	}
	return strings.Join(lines, "\n")
}
