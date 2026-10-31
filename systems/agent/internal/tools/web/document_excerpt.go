package web

import (
	"regexp"
	"strings"
	"unicode"
)

var (
	documentListMarker = regexp.MustCompile(`^(?:[-+*•●]|\p{N}+[.)])\s+`)
	summarySeparator   = regexp.MustCompile(`\s+[·›»|]\s+`)
)

// Provider summaries can contain only breadcrumbs or index links. Keep any
// explanatory sentences in a separated summary, and leave the source available
// without an excerpt when the provider supplied no prose.
func summaryExcerpt(title, text string, terms []string) string {
	text = cleanText(text)
	if parts := summarySeparator.Split(text, -1); len(parts) > 1 {
		var prose []string
		for _, part := range parts {
			if hasProseSentence(part) {
				prose = append(prose, part)
			}
		}
		text = strings.Join(prose, "\n\n")
	}
	return documentExcerpt(title, text, terms)
}

// documentExcerpt retains paragraph boundaries until navigation, tables and
// fenced code have been excluded. Flattening first makes a query-dense TOC
// indistinguishable from an explanatory passage.
func documentExcerpt(title, body string, terms []string) string {
	text := stripTitleEcho(title, documentProse(body))
	if titleKey(text) == titleKey(title) {
		return ""
	}
	return excerpt(text, terms, maxExcerptRunes)
}

func documentProse(body string) string {
	var paragraphs, sentences []string
	var paragraph strings.Builder
	flush := func() {
		text := cleanText(paragraph.String())
		paragraph.Reset()
		if text == "" {
			return
		}
		paragraphs = append(paragraphs, text)
		if hasProseSentence(text) {
			sentences = append(sentences, text)
		}
	}

	fence := ""
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if marker := documentFence(line); marker != "" {
			flush()
			if fence == "" {
				fence = marker
			} else if strings.HasPrefix(marker, fence) && strings.TrimSpace(line[len(marker):]) == "" {
				fence = ""
			}
			continue
		}
		if fence != "" {
			continue
		}
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "|") {
			flush()
			continue
		}
		if marker := documentListMarker.FindString(line); marker != "" {
			flush()
			line = strings.TrimSpace(strings.TrimPrefix(line, marker))
			if !hasProseSentence(line) {
				continue
			}
		}
		paragraph.WriteString(line)
		paragraph.WriteByte(' ')
	}
	flush()

	// Form labels and misdecoded PDF glyph runs often survive as valid Unicode.
	// Prefer complete prose when available without guessing replacement glyphs
	// or discarding characters based on their script.
	if len(sentences) > 0 {
		return strings.Join(sentences, " ")
	}
	return strings.Join(paragraphs, " ")
}

func documentFence(line string) string {
	if len(line) < 3 || (line[0] != '`' && line[0] != '~') {
		return ""
	}
	n := 1
	for n < len(line) && line[n] == line[0] {
		n++
	}
	if n < 3 {
		return ""
	}
	return line[:n]
}

func hasProseSentence(text string) bool {
	runes := []rune(text)
	hasLetter := false
	for i, r := range runes {
		if unicode.IsLetter(r) {
			hasLetter = true
		}
		if !hasLetter {
			continue
		}
		switch r {
		case '。', '！', '？':
			return true
		case '.', '!', '?':
			if i > 0 && (i+1 == len(runes) || unicode.IsSpace(runes[i+1])) {
				return true
			}
		}
	}
	return false
}
