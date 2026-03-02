package telegram

import (
	"fmt"
	"regexp"
	"strings"
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
		table = restoreInlineCodePlaceholders(table, inlineCodes.codes)
		escaped := escapeHTML(table)
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

type tableMatch struct {
	text   string
	tables []string
}

func extractTables(text string) tableMatch {
	lines := strings.Split(text, "\n")

	tables := make([]string, 0)
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
				table := renderTable(lines[i:j])
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
