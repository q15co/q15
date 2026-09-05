package telegram

import (
	"html"
	"strings"
	"unicode/utf8"
)

func renderRichTableHTML(table parsedTable) string {
	var out strings.Builder
	out.WriteString("<table bordered striped>")
	renderRichTableRow(&out, table.header, true, table.aligns)
	for _, row := range table.rows {
		renderRichTableRow(&out, row, false, table.aligns)
	}
	out.WriteString("</table>")
	return out.String()
}

func renderRichTableRow(
	out *strings.Builder,
	row []string,
	header bool,
	aligns []string,
) {
	tag := "td"
	if header {
		tag = "th"
	}
	out.WriteString("<tr>")
	for column, cell := range row {
		out.WriteByte('<')
		out.WriteString(tag)
		if column < len(aligns) && aligns[column] != "" {
			out.WriteString(` align="`)
			out.WriteString(aligns[column])
			out.WriteByte('"')
		}
		out.WriteByte('>')
		out.WriteString(renderRichTableCell(cell, 0))
		out.WriteString("</")
		out.WriteString(tag)
		out.WriteByte('>')
	}
	out.WriteString("</tr>")
}

// renderRichTableCell emits only a small, explicit inline HTML allow-list.
// Telegram doesn't parse Markdown inside HTML table blocks.
func renderRichTableCell(source string, depth int) string {
	if source == "" {
		return ""
	}
	if depth >= telegramRichNestingLimit {
		return html.EscapeString(source)
	}

	var out strings.Builder
	for i := 0; i < len(source); {
		if source[i] == '\\' && i+1 < len(source) {
			out.WriteString(html.EscapeString(source[i+1 : i+2]))
			i += 2
			continue
		}
		if source[i] == '!' && i+1 < len(source) && source[i+1] == '[' {
			if link, ok := parseInlineMarkdownLink(source, i, true); ok {
				label := renderRichTableCell(link.label, depth+1)
				if label == "" {
					label = "image"
				}
				out.WriteString("Image: ")
				if isAllowedRichLink(link.destination) {
					out.WriteString(`<a href="`)
					out.WriteString(html.EscapeString(link.destination))
					out.WriteString(`">`)
					out.WriteString(label)
					out.WriteString("</a>")
				} else {
					out.WriteString(label)
				}
				i = link.end
				continue
			}
		}
		if source[i] == '[' {
			if link, ok := parseInlineMarkdownLink(source, i, false); ok {
				label := renderRichTableCell(link.label, depth+1)
				if isAllowedRichLink(link.destination) {
					out.WriteString(`<a href="`)
					out.WriteString(html.EscapeString(link.destination))
					out.WriteString(`">`)
					out.WriteString(label)
					out.WriteString("</a>")
				} else {
					out.WriteString(label)
				}
				i = link.end
				continue
			}
		}
		if source[i] == '`' {
			width := delimiterWidth(source[i:], '`')
			if end := findClosingDelimiter(source, i+width, '`', width); end >= 0 {
				out.WriteString("<code>")
				out.WriteString(html.EscapeString(source[i+width : end]))
				out.WriteString("</code>")
				i = end + width
				continue
			}
		}

		matched := false
		for _, marker := range []struct {
			delimiter string
			tag       string
		}{
			{"**", "b"},
			{"__", "b"},
			{"~~", "s"},
			{"==", "mark"},
			{"||", "tg-spoiler"},
			{"*", "i"},
			{"_", "i"},
		} {
			if !strings.HasPrefix(source[i:], marker.delimiter) {
				continue
			}
			start := i + len(marker.delimiter)
			end := strings.Index(source[start:], marker.delimiter)
			if end < 0 {
				continue
			}
			end += start
			out.WriteByte('<')
			out.WriteString(marker.tag)
			out.WriteByte('>')
			out.WriteString(renderRichTableCell(source[start:end], depth+1))
			out.WriteString("</")
			out.WriteString(marker.tag)
			out.WriteByte('>')
			i = end + len(marker.delimiter)
			matched = true
			break
		}
		if matched {
			continue
		}

		if source[i] == '$' && (i+1 >= len(source) || source[i+1] != '$') {
			if end := strings.IndexByte(source[i+1:], '$'); end >= 0 {
				end += i + 1
				out.WriteString("<tg-math>")
				out.WriteString(html.EscapeString(source[i+1 : end]))
				out.WriteString("</tg-math>")
				i = end + 1
				continue
			}
		}

		_, width := utf8.DecodeRuneInString(source[i:])
		out.WriteString(html.EscapeString(source[i : i+width]))
		i += width
	}
	return out.String()
}
