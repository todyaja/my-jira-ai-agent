package confluence

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	headingPattern   = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)
	listItemPattern  = regexp.MustCompile(`^(\s*)([-*+]|\d+[.)])\s+(.*)$`)
	rulePattern      = regexp.MustCompile(`^(-{3,}|\*{3,}|_{3,})$`)
	tableRulePattern = regexp.MustCompile(`^:?-+:?$`)
)

type listFrame struct {
	tag    string
	indent int
}

type storageWriter struct {
	output    strings.Builder
	paragraph []string
	lists     []listFrame
	table     [][]string
	code      []string
	inCode    bool
}

// MarkdownToStorage converts the Markdown a PRD is written in to Confluence
// storage format. It covers headings, paragraphs, nested lists, tables, code
// blocks, block quotes, rules, bold, inline code and links; anything else is
// kept as escaped text.
func MarkdownToStorage(markdown string) string {
	writer := &storageWriter{}
	for _, line := range strings.Split(strings.ReplaceAll(markdown, "\r\n", "\n"), "\n") {
		writer.line(strings.TrimRight(line, "\r"))
	}
	writer.flush()
	if writer.inCode {
		writer.flushCode()
	}
	return writer.output.String()
}

func (w *storageWriter) line(line string) {
	trimmed := strings.TrimSpace(line)
	if w.inCode {
		if strings.HasPrefix(trimmed, "```") {
			w.flushCode()
			return
		}
		w.code = append(w.code, line)
		return
	}

	switch {
	case strings.HasPrefix(trimmed, "```"):
		w.flush()
		w.inCode = true
	case trimmed == "":
		w.flush()
	case headingPattern.MatchString(trimmed):
		w.flush()
		match := headingPattern.FindStringSubmatch(trimmed)
		level := len(match[1])
		fmt.Fprintf(&w.output, "<h%d>%s</h%d>", level, inline(match[2]), level)
	case rulePattern.MatchString(trimmed):
		w.flush()
		w.output.WriteString("<hr />")
	case strings.HasPrefix(trimmed, "|"):
		w.flushParagraph()
		w.flushLists()
		w.table = append(w.table, tableCells(trimmed))
	case listItemPattern.MatchString(line):
		w.flushParagraph()
		w.flushTable()
		match := listItemPattern.FindStringSubmatch(line)
		tag := "ul"
		if match[2][0] >= '0' && match[2][0] <= '9' {
			tag = "ol"
		}
		w.listItem(tag, len(strings.ReplaceAll(match[1], "\t", "    ")), match[3])
	case strings.HasPrefix(trimmed, ">"):
		w.flush()
		quoted := strings.TrimSpace(strings.TrimPrefix(trimmed, ">"))
		fmt.Fprintf(&w.output, "<blockquote><p>%s</p></blockquote>", inline(quoted))
	case len(w.lists) > 0:
		// A plain line inside a list continues the open item.
		w.output.WriteString(" " + inline(trimmed))
	default:
		w.flushTable()
		w.paragraph = append(w.paragraph, trimmed)
	}
}

func (w *storageWriter) listItem(tag string, indent int, text string) {
	for len(w.lists) > 0 && w.lists[len(w.lists)-1].indent > indent {
		w.closeList()
	}
	if len(w.lists) > 0 {
		top := w.lists[len(w.lists)-1]
		if indent > top.indent {
			w.openList(tag, indent)
		} else if top.tag != tag {
			w.closeList()
			w.openList(tag, indent)
		} else {
			w.output.WriteString("</li>")
		}
	} else {
		w.openList(tag, indent)
	}
	w.output.WriteString("<li>" + inline(text))
}

func (w *storageWriter) openList(tag string, indent int) {
	w.output.WriteString("<" + tag + ">")
	w.lists = append(w.lists, listFrame{tag: tag, indent: indent})
}

func (w *storageWriter) closeList() {
	top := w.lists[len(w.lists)-1]
	w.lists = w.lists[:len(w.lists)-1]
	w.output.WriteString("</li></" + top.tag + ">")
}

func (w *storageWriter) flush() {
	w.flushParagraph()
	w.flushLists()
	w.flushTable()
}

func (w *storageWriter) flushParagraph() {
	if len(w.paragraph) == 0 {
		return
	}
	w.output.WriteString("<p>" + inline(strings.Join(w.paragraph, " ")) + "</p>")
	w.paragraph = nil
}

func (w *storageWriter) flushLists() {
	for len(w.lists) > 0 {
		w.closeList()
	}
}

func (w *storageWriter) flushTable() {
	if len(w.table) == 0 {
		return
	}
	w.output.WriteString("<table><tbody>")
	for index, row := range w.table {
		if index == 1 && isTableRule(row) {
			continue
		}
		cellTag := "td"
		if index == 0 {
			cellTag = "th"
		}
		w.output.WriteString("<tr>")
		for _, cell := range row {
			w.output.WriteString("<" + cellTag + ">" + inline(cell) + "</" + cellTag + ">")
		}
		w.output.WriteString("</tr>")
	}
	w.output.WriteString("</tbody></table>")
	w.table = nil
}

func (w *storageWriter) flushCode() {
	// "]]>" cannot appear inside CDATA, so it is split across two sections.
	body := strings.ReplaceAll(xmlSafe(strings.Join(w.code, "\n")), "]]>", "]]]]><![CDATA[>")
	w.output.WriteString(`<ac:structured-macro ac:name="code"><ac:plain-text-body><![CDATA[` + body + `]]></ac:plain-text-body></ac:structured-macro>`)
	w.code = nil
	w.inCode = false
}

func tableCells(row string) []string {
	row = strings.TrimSuffix(strings.TrimPrefix(row, "|"), "|")
	cells := strings.Split(row, "|")
	for index := range cells {
		cells[index] = strings.TrimSpace(cells[index])
	}
	return cells
}

func isTableRule(row []string) bool {
	for _, cell := range row {
		if !tableRulePattern.MatchString(cell) {
			return false
		}
	}
	return true
}

// inline escapes text and converts **bold**, `code` and [text](url) links.
func inline(text string) string {
	var output strings.Builder
	for len(text) > 0 {
		switch {
		case text[0] == '`':
			if end := strings.IndexByte(text[1:], '`'); end >= 0 {
				output.WriteString("<code>" + escape(text[1:1+end]) + "</code>")
				text = text[end+2:]
				continue
			}
		case strings.HasPrefix(text, "**"):
			if end := strings.Index(text[2:], "**"); end > 0 {
				output.WriteString("<strong>" + inline(text[2:2+end]) + "</strong>")
				text = text[end+4:]
				continue
			}
		case text[0] == '[':
			if label, href, rest, ok := markdownLink(text); ok {
				output.WriteString(`<a href="` + escape(href) + `">` + inline(label) + "</a>")
				text = rest
				continue
			}
		}
		_, size := utf8.DecodeRuneInString(text)
		output.WriteString(escape(text[:size]))
		text = text[size:]
	}
	return output.String()
}

func markdownLink(text string) (label, href, rest string, ok bool) {
	closeLabel := strings.Index(text, "](")
	if closeLabel < 0 {
		return "", "", "", false
	}
	closeHref := strings.IndexByte(text[closeLabel+2:], ')')
	if closeHref < 0 {
		return "", "", "", false
	}
	href = text[closeLabel+2 : closeLabel+2+closeHref]
	if !strings.HasPrefix(href, "https://") && !strings.HasPrefix(href, "http://") && !strings.HasPrefix(href, "mailto:") {
		return "", "", "", false
	}
	return text[1:closeLabel], href, text[closeLabel+3+closeHref:], true
}

func escape(text string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(xmlSafe(text))
}

// xmlSafe drops control characters that XML does not allow.
func xmlSafe(text string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' && r != '\n' {
			return -1
		}
		return r
	}, text)
}
