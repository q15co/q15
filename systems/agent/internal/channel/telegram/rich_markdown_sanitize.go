package telegram

import (
	"net/url"
	"strings"
	"unicode"
)

// sanitizeRichMarkdown preserves Telegram's supported Markdown surface while
// neutralizing the capabilities that untrusted model output must not create:
// raw rich HTML, embedded media, and links outside the explicit scheme allow-list.
func sanitizeRichMarkdown(source string) string {
	if source == "" {
		return ""
	}
	if fence, ok := markdownFence(strings.TrimSpace(strings.SplitN(source, "\n", 2)[0])); ok {
		_ = fence
		return source
	}

	var out strings.Builder
	for i := 0; i < len(source); {
		if end := unsafeReferenceDefinitionEnd(source, i); end > i {
			out.WriteString("\\[unsafe link definition omitted]")
			i = end
			continue
		}
		if source[i] == '\\' && i+1 < len(source) {
			out.WriteByte(source[i])
			out.WriteByte(source[i+1])
			i += 2
			continue
		}

		if source[i] == '`' {
			width := delimiterWidth(source[i:], '`')
			if end := findClosingDelimiter(source, i+width, '`', width); end >= 0 {
				out.WriteString(source[i : end+width])
				i = end + width
				continue
			}
		}
		if source[i] == '$' {
			width := delimiterWidth(source[i:], '$')
			if width <= 2 {
				if end := findClosingDelimiter(source, i+width, '$', width); end >= 0 {
					out.WriteString(source[i : end+width])
					i = end + width
					continue
				}
			}
		}

		if source[i] == '!' && i+1 < len(source) && source[i+1] == '[' {
			if link, ok := parseInlineMarkdownLink(source, i, true); ok {
				label := sanitizeRichMarkdown(link.label)
				if strings.TrimSpace(label) == "" {
					label = "image"
				}
				out.WriteString("Image: ")
				if isAllowedRichLink(link.destination) {
					out.WriteByte('[')
					out.WriteString(label)
					out.WriteString("](")
					out.WriteString(link.destination)
					out.WriteByte(')')
				} else {
					out.WriteString(label)
				}
				i = link.end
				continue
			}
			// Reference-style and malformed images must not reach Telegram as media.
			out.WriteString("\\!")
			i++
			continue
		}

		if source[i] == '[' {
			if link, ok := parseInlineMarkdownLink(source, i, false); ok {
				label := sanitizeRichMarkdown(link.label)
				if isAllowedRichLink(link.destination) {
					out.WriteByte('[')
					out.WriteString(label)
					out.WriteString("](")
					out.WriteString(link.destination)
					out.WriteByte(')')
				} else {
					out.WriteString(label)
				}
				i = link.end
				continue
			}
		}

		switch source[i] {
		case '<':
			out.WriteString("&lt;")
		default:
			out.WriteByte(source[i])
		}
		i++
	}
	return out.String()
}

type inlineMarkdownLink struct {
	label       string
	destination string
	end         int
}

func unsafeReferenceDefinitionEnd(source string, start int) int {
	if start >= len(source) || source[start] != '[' ||
		(start+1 < len(source) && source[start+1] == '^') {
		return -1
	}
	lineStart := strings.LastIndexByte(source[:start], '\n') + 1
	indent := source[lineStart:start]
	if len(indent) > 3 || strings.Trim(indent, " ") != "" {
		return -1
	}
	lineEnd := strings.IndexByte(source[start:], '\n')
	if lineEnd < 0 {
		lineEnd = len(source)
	} else {
		lineEnd += start
	}
	closeLabel := strings.Index(source[start:lineEnd], "]:")
	if closeLabel < 0 {
		return -1
	}
	closeLabel += start
	target := strings.TrimSpace(source[closeLabel+2 : lineEnd])
	if target == "" || isAllowedRichLink(markdownLinkDestination(target)) {
		return -1
	}
	return lineEnd
}

func parseInlineMarkdownLink(source string, start int, image bool) (inlineMarkdownLink, bool) {
	open := start
	if image {
		open++
	}
	if open >= len(source) || source[open] != '[' {
		return inlineMarkdownLink{}, false
	}
	closeLabel := matchingDelimiter(source, open, '[', ']')
	if closeLabel < 0 || closeLabel+1 >= len(source) || source[closeLabel+1] != '(' {
		return inlineMarkdownLink{}, false
	}
	closeTarget := matchingDelimiter(source, closeLabel+1, '(', ')')
	if closeTarget < 0 {
		return inlineMarkdownLink{}, false
	}
	target := source[closeLabel+2 : closeTarget]
	return inlineMarkdownLink{
		label:       source[open+1 : closeLabel],
		destination: markdownLinkDestination(target),
		end:         closeTarget + 1,
	}, true
}

func matchingDelimiter(source string, open int, left, right byte) int {
	depth := 0
	for i := open; i < len(source); i++ {
		if source[i] == '\\' {
			i++
			continue
		}
		switch source[i] {
		case left:
			depth++
		case right:
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func markdownLinkDestination(target string) string {
	target = strings.TrimSpace(target)
	if strings.HasPrefix(target, "<") {
		if end := strings.IndexByte(target, '>'); end > 0 {
			return target[1:end]
		}
	}
	for i, r := range target {
		if unicode.IsSpace(r) {
			return target[:i]
		}
	}
	return target
}

func isAllowedRichLink(destination string) bool {
	destination = strings.TrimSpace(destination)
	if strings.HasPrefix(destination, "#") {
		return len(destination) > 1 && !strings.ContainsAny(destination, "\r\n<>")
	}
	if destination == "" || strings.ContainsAny(destination, "\r\n<>") {
		return false
	}

	parsed, err := url.Parse(destination)
	if err != nil {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		return parsed.Host != ""
	case "mailto", "tel":
		return parsed.Opaque != "" || parsed.Path != ""
	default:
		return false
	}
}

func delimiterWidth(source string, delimiter byte) int {
	width := 0
	for width < len(source) && source[width] == delimiter {
		width++
	}
	return width
}

func findClosingDelimiter(source string, start int, delimiter byte, width int) int {
	needle := strings.Repeat(string(delimiter), width)
	if idx := strings.Index(source[start:], needle); idx >= 0 {
		return start + idx
	}
	return -1
}
