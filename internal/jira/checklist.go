package jira

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// ChecklistItem is one checkbox of an action-item list ("[]" in the Jira
// editor) in a description.
type ChecklistItem struct {
	Text    string
	Checked bool
}

// Checklist returns every checkbox in the description, in order, wherever it
// is nested.
func Checklist(raw json.RawMessage) ([]ChecklistItem, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return []ChecklistItem{}, nil
	}
	var document adfDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		return nil, fmt.Errorf("decode ADF: %w", err)
	}
	items := []ChecklistItem{}
	if err := collectTaskItems(document.Content, &items); err != nil {
		return nil, err
	}
	return items, nil
}

// CheckedItems returns the text of every checked checkbox in the
// description, in order.
func CheckedItems(raw json.RawMessage) ([]string, error) {
	items, err := Checklist(raw)
	if err != nil {
		return nil, err
	}
	checked := []string{}
	for _, item := range items {
		if item.Checked && item.Text != "" {
			checked = append(checked, item.Text)
		}
	}
	return checked, nil
}

type taskNode struct {
	Type  string `json:"type"`
	Attrs struct {
		State string `json:"state"`
	} `json:"attrs"`
	Content []json.RawMessage `json:"content"`
}

func collectTaskItems(nodes []json.RawMessage, items *[]ChecklistItem) error {
	for _, raw := range nodes {
		var node taskNode
		if err := json.Unmarshal(raw, &node); err != nil {
			return fmt.Errorf("decode ADF node: %w", err)
		}
		if node.Type == "taskItem" {
			var text strings.Builder
			if err := appendADFText(&text, node.Content); err != nil {
				return err
			}
			*items = append(*items, ChecklistItem{Text: strings.TrimSpace(text.String()), Checked: node.Attrs.State == "DONE"})
			continue
		}
		if err := collectTaskItems(node.Content, items); err != nil {
			return err
		}
	}
	return nil
}
