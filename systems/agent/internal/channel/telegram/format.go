package telegram

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/mymmrac/telego"
)

var (
	reHeading    = regexp.MustCompile(`(?m)^#{1,6}\s+(.+)$`)
	reBlockquote = regexp.MustCompile(`(?m)^>\s*(.*)$`)
	reLink       = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	reBoldStar   = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reBoldUnder  = regexp.MustCompile(`__(.+?)__`)
	reItalic     = regexp.MustCompile(`_([^_]+)_`)
	reStrike     = regexp.MustCompile(`~~(.+?)~~`)
	reRule       = regexp.MustCompile(`(?m)^\s*-{3,}\s*$`)
	reTaskItem   = regexp.MustCompile(`(?m)^(\s*)[-*]\s+\[([ xX])\]\s+`)
	reListItem   = regexp.MustCompile(`(?m)^[-*]\s+`)
	reCodeBlock  = regexp.MustCompile("```[\\w]*\\n?([\\s\\S]*?)```")
	reInlineCode = regexp.MustCompile("`([^`]+)`")
)

const telegramDivider = "<b>──────────────</b>"

func markdownToTelegramHTML(text string) string {
	if text == "" {
		return ""
	}

	codeBlocks := extractCodeBlocks(text)
	text = codeBlocks.text

	inlineCodes := extractInlineCodes(text)
	text = inlineCodes.text

	tables := extractTables(text)
	text = tables.text

	text = reBlockquote.ReplaceAllString(text, "$1")

	text = escapeHTML(text)
	text = reHeading.ReplaceAllString(text, "<b><u>$1</u></b>")

	text = reLink.ReplaceAllString(text, `<a href="$2">$1</a>`)
	text = reBoldStar.ReplaceAllString(text, "<b>$1</b>")
	text = reBoldUnder.ReplaceAllString(text, "<b>$1</b>")
	text = reItalic.ReplaceAllStringFunc(text, func(s string) string {
		match := reItalic.FindStringSubmatch(s)
		if len(match) < 2 {
			return s
		}
		return "<i>" + match[1] + "</i>"
	})
	text = reStrike.ReplaceAllString(text, "<s>$1</s>")
	text = reRule.ReplaceAllString(text, telegramDivider)
	text = reTaskItem.ReplaceAllStringFunc(text, renderTaskItem)
	text = reListItem.ReplaceAllString(text, "• ")

	for i, table := range tables.tables {
		rendered := restoreInlineCodePlaceholders(renderTable(table.lines), inlineCodes.codes)
		escaped := escapeHTML(rendered)
		text = strings.ReplaceAll(
			text,
			fmt.Sprintf("\x00TB%d\x00", i),
			fmt.Sprintf("<pre>%s</pre>", escaped),
		)
	}

	for i, code := range inlineCodes.codes {
		escaped := escapeHTML(code)
		text = strings.ReplaceAll(
			text,
			fmt.Sprintf("\x00IC%d\x00", i),
			fmt.Sprintf("<code>%s</code>", escaped),
		)
	}

	for i, code := range codeBlocks.codes {
		escaped := escapeHTML(code)
		text = strings.ReplaceAll(
			text,
			fmt.Sprintf("\x00CB%d\x00", i),
			fmt.Sprintf("<pre><code>%s</code></pre>", escaped),
		)
	}

	return text
}

type codeBlockMatch struct {
	text  string
	codes []string
}

func extractCodeBlocks(text string) codeBlockMatch {
	matches := reCodeBlock.FindAllStringSubmatch(text, -1)

	codes := make([]string, 0, len(matches))
	for _, match := range matches {
		codes = append(codes, match[1])
	}

	i := 0
	text = reCodeBlock.ReplaceAllStringFunc(text, func(string) string {
		placeholder := fmt.Sprintf("\x00CB%d\x00", i)
		i++
		return placeholder
	})

	return codeBlockMatch{text: text, codes: codes}
}

type inlineCodeMatch struct {
	text  string
	codes []string
}

func extractInlineCodes(text string) inlineCodeMatch {
	matches := reInlineCode.FindAllStringSubmatch(text, -1)

	codes := make([]string, 0, len(matches))
	for _, match := range matches {
		codes = append(codes, match[1])
	}

	i := 0
	text = reInlineCode.ReplaceAllStringFunc(text, func(string) string {
		placeholder := fmt.Sprintf("\x00IC%d\x00", i)
		i++
		return placeholder
	})

	return inlineCodeMatch{text: text, codes: codes}
}

// parsedTable is a markdown table with its header, body rows, and per-column
// alignment pre-split into cells.
type parsedTable struct {
	lines  []string // raw markdown lines, including the separator row
	header []string
	rows   [][]string
	aligns []string // "" (default), "left", "center", or "right" per column
}

type tableMatch struct {
	text   string
	tables []parsedTable
}

func extractTables(text string) tableMatch {
	lines := strings.Split(text, "\n")

	tables := make([]parsedTable, 0)
	var out strings.Builder

	for i := 0; i < len(lines); {
		if i+2 < len(lines) &&
			isTableRowLine(lines[i]) &&
			isTableSeparatorLine(lines[i+1]) {
			j := i + 2
			for j < len(lines) && isTableRowLine(lines[j]) {
				j++
			}
			if j > i+2 {
				table := parseTable(lines[i:j])
				placeholder := fmt.Sprintf("\x00TB%d\x00", len(tables))
				tables = append(tables, table)
				out.WriteString(placeholder)
				if j < len(lines) {
					out.WriteByte('\n')
				}
				i = j
				continue
			}
		}

		out.WriteString(lines[i])
		if i < len(lines)-1 {
			out.WriteByte('\n')
		}
		i++
	}

	return tableMatch{text: out.String(), tables: tables}
}

// parseTable converts raw markdown table lines into a header row, body rows,
// and per-column alignment parsed from the separator row.
func parseTable(lines []string) parsedTable {
	header := splitTableCells(lines[0])
	aligns := parseTableAligns(splitTableCells(lines[1]))

	rows := make([][]string, 0, len(lines)-2)
	for _, line := range lines[2:] {
		rows = append(rows, splitTableCells(line))
	}

	cols := len(header)
	for _, row := range rows {
		if len(row) > cols {
			cols = len(row)
		}
	}
	for len(aligns) < cols {
		aligns = append(aligns, "")
	}

	return parsedTable{lines: lines, header: header, rows: rows, aligns: aligns}
}

// parseTableAligns maps GFM separator cells ("---", ":---", ":---:", "---:")
// to "" (default), "left", "center", and "right".
func parseTableAligns(cells []string) []string {
	aligns := make([]string, 0, len(cells))
	for _, cell := range cells {
		cell = strings.TrimSpace(cell)
		left := strings.HasPrefix(cell, ":")
		right := strings.HasSuffix(cell, ":")
		switch {
		case left && right:
			aligns = append(aligns, telego.CellAlignCenter)
		case right:
			aligns = append(aligns, telego.CellAlignRight)
		case left:
			aligns = append(aligns, telego.CellAlignLeft)
		default:
			aligns = append(aligns, "")
		}
	}
	return aligns
}

func isTableRowLine(line string) bool {
	return len(splitTableCells(line)) >= 2
}

func isTableSeparatorLine(line string) bool {
	cells := splitTableCells(line)
	if len(cells) < 2 {
		return false
	}
	for _, cell := range cells {
		cell = strings.TrimSpace(cell)
		if cell == "" {
			return false
		}
		if strings.Count(cell, "-") < 3 {
			return false
		}
		if strings.Trim(cell, "-: ") != "" {
			return false
		}
	}
	return true
}

func renderTable(lines []string) string {
	if len(lines) < 3 {
		return strings.Join(lines, "\n")
	}

	rows := make([][]string, 0, len(lines)-1)
	rows = append(rows, splitTableCells(lines[0]))
	for _, line := range lines[2:] {
		rows = append(rows, splitTableCells(line))
	}

	colCount := 0
	for _, row := range rows {
		if len(row) > colCount {
			colCount = len(row)
		}
	}

	var out strings.Builder
	for i, row := range rows {
		if len(row) < colCount {
			padding := make([]string, colCount-len(row))
			row = append(row, padding...)
		}
		out.WriteString(strings.Join(row, " | "))
		if i < len(rows)-1 {
			out.WriteByte('\n')
		}
	}
	return out.String()
}

func splitTableCells(line string) []string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return nil
	}

	parts := splitUnescapedPipes(trimmed)
	if strings.HasPrefix(trimmed, "|") && len(parts) > 0 {
		parts = parts[1:]
	}
	if strings.HasSuffix(trimmed, "|") && len(parts) > 0 {
		parts = parts[:len(parts)-1]
	}

	if len(parts) < 2 {
		return nil
	}

	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func splitUnescapedPipes(s string) []string {
	parts := make([]string, 0, 4)
	var current strings.Builder
	escaped := false

	for _, r := range s {
		if escaped {
			if r == '|' {
				current.WriteRune('|')
			} else {
				current.WriteRune('\\')
				current.WriteRune(r)
			}
			escaped = false
			continue
		}

		if r == '\\' {
			escaped = true
			continue
		}
		if r == '|' {
			parts = append(parts, current.String())
			current.Reset()
			continue
		}
		current.WriteRune(r)
	}
	if escaped {
		current.WriteRune('\\')
	}
	parts = append(parts, current.String())
	return parts
}

func escapeHTML(text string) string {
	text = strings.ReplaceAll(text, "&", "&amp;")
	text = strings.ReplaceAll(text, "<", "&lt;")
	text = strings.ReplaceAll(text, ">", "&gt;")
	return text
}

func restoreInlineCodePlaceholders(text string, codes []string) string {
	for i, code := range codes {
		text = strings.ReplaceAll(
			text,
			fmt.Sprintf("\x00IC%d\x00", i),
			code,
		)
	}
	return text
}

func renderTaskItem(taskLine string) string {
	match := reTaskItem.FindStringSubmatch(taskLine)
	if len(match) < 3 {
		return taskLine
	}

	prefix := match[1]
	marker := "⬜"
	if match[2] == "x" || match[2] == "X" {
		marker = "✅"
	}

	return reTaskItem.ReplaceAllString(taskLine, prefix+marker+" ")
}

// contentSegmentKind distinguishes prose segments (regular HTML message
// path) from table segments (native rich-message table rendering).
type contentSegmentKind int

const (
	segmentHTML contentSegmentKind = iota
	segmentTable
)

// contentSegment is one top-level piece of a markdown document: either the
// raw markdown of a prose chunk (tables already carved out) or a parsed
// table with the inline-code contents its cells contain.
type contentSegment struct {
	kind  contentSegmentKind
	raw   string      // segmentHTML: raw markdown chunk
	table parsedTable // segmentTable
	codes []string    // segmentTable: inline-code contents in cells
}

// markdownToSegments splits markdown into ordered prose and table segments.
// Tables are carved out at their exact positions so prose chunks can be
// formatted, chunked, and sent independently while tables render natively.
func markdownToSegments(text string) []contentSegment {
	if strings.TrimSpace(text) == "" {
		return nil
	}

	codeBlocks := extractCodeBlocks(text)
	inlineCodes := extractInlineCodes(codeBlocks.text)
	tables := extractTables(inlineCodes.text)

	restore := func(chunk string) string {
		chunk = restoreInlineCodePlaceholders(chunk, inlineCodes.codes)
		return restoreCodeBlockPlaceholders(chunk, codeBlocks.codes)
	}

	segments := make([]contentSegment, 0, 2*len(tables.tables)+1)
	rest := tables.text
	for i, table := range tables.tables {
		placeholder := fmt.Sprintf("\x00TB%d\x00", i)
		idx := strings.Index(rest, placeholder)
		if idx < 0 {
			// Defensive: the placeholder should always be present. Drop the
			// marker so it never leaks into sent text.
			rest = strings.ReplaceAll(rest, placeholder, "")
			continue
		}
		if chunk := strings.TrimSpace(rest[:idx]); chunk != "" {
			segments = append(segments, contentSegment{kind: segmentHTML, raw: restore(chunk)})
		}
		segments = append(
			segments,
			contentSegment{kind: segmentTable, table: table, codes: inlineCodes.codes},
		)
		rest = rest[idx+len(placeholder):]
	}
	if chunk := strings.TrimSpace(rest); chunk != "" {
		segments = append(segments, contentSegment{kind: segmentHTML, raw: restore(chunk)})
	}

	return segments
}

func hasMarkdownTable(text string) bool {
	for _, segment := range markdownToSegments(text) {
		if segment.kind == segmentTable {
			return true
		}
	}
	return false
}

// restoreCodeBlockPlaceholders turns fenced-code placeholders back into
// fenced blocks so restored prose can be re-parsed and re-chunked safely.
func restoreCodeBlockPlaceholders(text string, codes []string) string {
	for i, code := range codes {
		text = strings.ReplaceAll(
			text,
			fmt.Sprintf("\x00CB%d\x00", i),
			"```"+code+"```",
		)
	}
	return text
}

// tableCellRichText converts one raw markdown table cell into Telegram rich
// text. Supported inline markdown: `code`, **bold**, and [text](url) links;
// everything else stays plain text. Returns nil for empty cells.
func tableCellRichText(cell string, codes []string) telego.RichText {
	for i, code := range codes {
		// Inline-code placeholders carry the bare code content, so restore
		// them with their backtick markers for the rich text scanner.
		cell = strings.ReplaceAll(
			cell,
			fmt.Sprintf("\x00IC%d\x00", i),
			"`"+code+"`",
		)
	}
	parts := inlineMarkdownToRichText(cell)
	switch len(parts) {
	case 0:
		return nil
	case 1:
		return parts[0]
	default:
		list := telego.RichTextList(parts)
		return &list
	}
}

// inlineMarkdownToRichText scans inline markdown into rich text nodes,
// keeping plain runs as bare strings the way Telegram expects.
func inlineMarkdownToRichText(text string) []telego.RichText {
	parts := make([]telego.RichText, 0, 4)
	var plain strings.Builder
	flush := func() {
		if plain.Len() > 0 {
			parts = append(parts, richPlain(plain.String()))
			plain.Reset()
		}
	}

	for text != "" {
		span, node, ok := nextInlineRichNode(text)
		if !ok {
			plain.WriteString(text)
			break
		}
		plain.WriteString(text[:span[0]])
		flush()
		parts = append(parts, node)
		text = text[span[1]:]
	}
	flush()

	return parts
}

// nextInlineRichNode finds the earliest inline code, bold, or link marker
// and returns its span plus the corresponding rich text node.
func nextInlineRichNode(text string) ([]int, telego.RichText, bool) {
	best := -1
	span := []int{0, 0}
	var node telego.RichText

	if m := reInlineCode.FindStringSubmatchIndex(text); m != nil {
		best = m[0]
		span = []int{m[0], m[1]}
		node = &telego.RichTextCode{Type: telego.TextTypeCode, Text: richPlain(text[m[2]:m[3]])}
	}
	if m := reBoldStar.FindStringSubmatchIndex(text); m != nil && (best < 0 || m[0] < best) {
		best = m[0]
		span = []int{m[0], m[1]}
		node = &telego.RichTextBold{Type: telego.TextTypeBold, Text: richPlain(text[m[2]:m[3]])}
	}
	if m := reLink.FindStringSubmatchIndex(text); m != nil && (best < 0 || m[0] < best) {
		best = m[0]
		span = []int{m[0], m[1]}
		node = &telego.RichTextURL{
			Type: telego.TextTypeURL,
			Text: linkTextRichText(text[m[2]:m[3]]),
			URL:  text[m[4]:m[5]],
		}
	}
	if best < 0 {
		return nil, nil, false
	}

	return span, node, true
}

// linkTextRichText converts the inner text of a markdown link into rich text
// so links can contain code or bold markers.
func linkTextRichText(text string) telego.RichText {
	parts := inlineMarkdownToRichText(text)
	switch len(parts) {
	case 0:
		return richPlain("")
	case 1:
		return parts[0]
	default:
		list := telego.RichTextList(parts)
		return &list
	}
}

// richPlain returns a plain rich text node as a pointer, matching the
// RichText interface's pointer-receiver methods.
func richPlain(text string) telego.RichText {
	plain := telego.RichTextPlain(text)
	return &plain
}
