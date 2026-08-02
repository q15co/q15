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

	tables := extractTables(text, inlineCodes.codes)
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

func extractTables(text string, inlineCodes []string) tableMatch {
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
				table := renderTable(lines[i:j], inlineCodes)
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

func renderTable(lines []string, inlineCodes []string) string {
	if len(lines) < 3 {
		return strings.Join(lines, "\n")
	}

	// Restore inline code placeholders so column widths reflect actual content.
	restored := make([]string, len(lines))
	for i, line := range lines {
		restored[i] = restoreInlineCodePlaceholders(line, inlineCodes)
	}
	lines = restored

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

	// Pad rows to uniform column count.
	for i, row := range rows {
		if len(row) < colCount {
			rows[i] = append(row, make([]string, colCount-len(row))...)
		}
	}

	// Calculate natural column widths (in runes).
	colWidths := make([]int, colCount)
	for _, row := range rows {
		for i, cell := range row {
			if w := len([]rune(cell)); w > colWidths[i] {
				colWidths[i] = w
			}
		}
	}

	// Telegram mobile wraps <pre> blocks at ~40 monospace characters.
	// Truncate columns if the table exceeds the mobile-friendly width.
	const maxTableWidth = 48
	colWidths = fitColumnWidths(colWidths, maxTableWidth)

	// Truncate and pad each cell to its column width.
	for i, row := range rows {
		for j, cell := range row {
			rows[i][j] = fitCell(cell, colWidths[j])
		}
	}

	// Render rows with a separator line after the header.
	var out strings.Builder
	for i, row := range rows {
		if i > 0 {
			out.WriteByte('\n')
		}
		line := strings.Join(row, " | ")
		out.WriteString(strings.TrimRight(line, " "))

		if i == 0 {
			out.WriteByte('\n')
			seps := make([]string, colCount)
			for j := range seps {
				seps[j] = strings.Repeat("-", colWidths[j])
			}
			sepLine := strings.Join(seps, " | ")
			out.WriteString(strings.TrimRight(sepLine, " "))
		}
	}
	return out.String()
}

// fitColumnWidths reduces column widths proportionally so the total table
// width fits within maxTotal characters. Columns that are already narrow
// enough keep their natural width; the savings are redistributed to wider
// columns.
func fitColumnWidths(widths []int, maxTotal int) []int {
	n := len(widths)
	sepWidth := 3 * (n - 1) // " | " between columns
	available := maxTotal - sepWidth
	if available < n {
		available = n
	}

	total := 0
	for _, w := range widths {
		total += w
	}
	if total <= available {
		return widths
	}

	const minCol = 3
	result := make([]int, n)
	for i := range result {
		result[i] = minCol
	}

	remaining := available - n*minCol
	if remaining > 0 {
		origExcess := total - n*minCol
		if origExcess > 0 {
			for i := range result {
				excess := widths[i] - minCol
				if excess > 0 {
					result[i] += remaining * excess / origExcess
				}
			}
			used := 0
			for _, w := range result {
				used += w
			}
			leftover := available - used
			for i := 0; i < n && leftover > 0; i++ {
				if widths[i] > result[i] {
					result[i]++
					leftover--
				}
			}
		}
	}

	return result
}

// fitCell truncates s to width runes, adding an ellipsis if truncated,
// then pads with spaces to exactly width runes.
func fitCell(s string, width int) string {
	runes := []rune(s)
	if len(runes) <= width {
		return s + strings.Repeat(" ", width-len(runes))
	}
	if width <= 3 {
		return string(runes[:width])
	}
	return string(runes[:width-1]) + "…"
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
