package jira

import (
	"encoding/json"
	"regexp"
	"strings"
)

// maxCommentRunes keeps generated comments under Jira's comment size limit.
const maxCommentRunes = 30000

var (
	markdownHeading = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)
	markdownBullet  = regexp.MustCompile(`^\s*[-*+]\s+(.*)$`)
	markdownNumber  = regexp.MustCompile(`^\s*\d+[.)]\s+(.*)$`)
)

// MarkdownADF converts the Markdown agents write into an ADF comment. It
// keeps headings, flat bullet and numbered lists, fenced code blocks and
// paragraphs; inline formatting stays as literal text. Text beyond Jira's
// comment limit is cut off with a note.
func MarkdownADF(markdown string) json.RawMessage {
	markdown = strings.ReplaceAll(markdown, "\r\n", "\n")
	if runes := []rune(markdown); len(runes) > maxCommentRunes {
		markdown = string(runes[:maxCommentRunes]) + "\n\n(truncated)"
	}

	var blocks []json.RawMessage
	var paragraph []string
	var listType string
	var listItems []json.RawMessage
	var code []string
	inCode := false

	flushParagraph := func() {
		if len(paragraph) == 0 {
			return
		}
		var inline []json.RawMessage
		for index, line := range paragraph {
			if index > 0 {
				inline = append(inline, mustJSON(adfNode{Type: "hardBreak"}))
			}
			inline = append(inline, mustJSON(adfNode{Type: "text", Text: line}))
		}
		blocks = append(blocks, mustJSON(adfNode{Type: "paragraph", Content: inline}))
		paragraph = nil
	}
	flushList := func() {
		if len(listItems) == 0 {
			return
		}
		blocks = append(blocks, mustJSON(adfNode{Type: listType, Content: listItems}))
		listItems, listType = nil, ""
	}
	addListItem := func(kind, text string) {
		flushParagraph()
		if listType != kind {
			flushList()
			listType = kind
		}
		listItems = append(listItems, mustJSON(adfNode{Type: "listItem", Content: []json.RawMessage{textParagraph(text)}}))
	}

	for _, line := range strings.Split(markdown, "\n") {
		trimmed := strings.TrimSpace(line)
		if inCode {
			if strings.HasPrefix(trimmed, "```") {
				blocks = append(blocks, codeBlock(code))
				code, inCode = nil, false
				continue
			}
			code = append(code, line)
			continue
		}
		switch {
		case strings.HasPrefix(trimmed, "```"):
			flushParagraph()
			flushList()
			inCode = true
		case trimmed == "":
			flushParagraph()
			flushList()
		case markdownHeading.MatchString(trimmed):
			flushParagraph()
			flushList()
			match := markdownHeading.FindStringSubmatch(trimmed)
			blocks = append(blocks, mustJSON(map[string]any{
				"type":    "heading",
				"attrs":   map[string]int{"level": len(match[1])},
				"content": []json.RawMessage{mustJSON(adfNode{Type: "text", Text: match[2]})},
			}))
		case markdownBullet.MatchString(line):
			addListItem("bulletList", markdownBullet.FindStringSubmatch(line)[1])
		case markdownNumber.MatchString(line):
			addListItem("orderedList", markdownNumber.FindStringSubmatch(line)[1])
		default:
			flushList()
			paragraph = append(paragraph, trimmed)
		}
	}
	if inCode {
		blocks = append(blocks, codeBlock(code))
	}
	flushParagraph()
	flushList()
	if len(blocks) == 0 {
		blocks = []json.RawMessage{textParagraph("(empty)")}
	}
	return mustJSON(adfDocument{Type: "doc", Version: 1, Content: blocks})
}

func codeBlock(lines []string) json.RawMessage {
	text := strings.Join(lines, "\n")
	if text == "" {
		return mustJSON(adfNode{Type: "codeBlock"})
	}
	return mustJSON(adfNode{Type: "codeBlock", Content: []json.RawMessage{mustJSON(adfNode{Type: "text", Text: text})}})
}
