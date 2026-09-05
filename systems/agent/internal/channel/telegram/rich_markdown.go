package telegram

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Telegram accepts up to 32,768 UTF-8 characters, 500 blocks, and 16 levels
// of nesting in one rich message. Keep a little headroom for the table HTML
// generated below and for conservative block estimation.
var (
	telegramRichTextRunes    = 32_000
	telegramRichBlockLimit   = 480
	telegramRichNestingLimit = 16
)

const telegramRichTableColumnLimit = 20

// richTextChunk is one independently sendable piece of a Markdown document.
// richMarkdown is safe to hand to Telegram's rich Markdown parser. fallback
// keeps Markdown tables intact for the legacy HTML/plain-text path.
type richTextChunk struct {
	richMarkdown string
	fallback     string
	rich         bool
}

type markdownBlock struct {
	rich         string
	fallback     string
	blockCount   int
	depth        int
	richEligible bool
}

// planRichText sanitizes untrusted model Markdown, renders tables using only
// q15-controlled rich HTML, and splits the document at Markdown block
// boundaries. A block that cannot fit Telegram's structural limits is marked
// for the legacy fallback instead of being submitted as invalid rich content.
func planRichText(text string) []richTextChunk {
	blocks := parseMarkdownBlocks(strings.TrimSpace(text))
	if len(blocks) == 0 {
		return nil
	}

	chunks := make([]richTextChunk, 0, 1)
	current := make([]markdownBlock, 0, len(blocks))
	currentRunes := 0
	currentBlocks := 0

	flush := func() {
		if len(current) == 0 {
			return
		}
		richParts := make([]string, 0, len(current))
		fallbackParts := make([]string, 0, len(current))
		for _, block := range current {
			richParts = append(richParts, block.rich)
			fallbackParts = append(fallbackParts, block.fallback)
		}
		chunks = append(chunks, richTextChunk{
			richMarkdown: strings.Join(richParts, "\n\n"),
			fallback:     strings.Join(fallbackParts, "\n\n"),
			rich:         true,
		})
		current = current[:0]
		currentRunes = 0
		currentBlocks = 0
	}

	for _, block := range blocks {
		blockRunes := utf8.RuneCountInString(block.rich)
		eligible := block.richEligible && blockRunes <= telegramRichTextRunes &&
			block.blockCount <= telegramRichBlockLimit &&
			block.depth <= telegramRichNestingLimit
		if !eligible {
			flush()
			chunks = append(chunks, richTextChunk{fallback: block.fallback})
			continue
		}

		separatorRunes := 0
		if len(current) > 0 {
			separatorRunes = 2
		}
		if len(current) > 0 &&
			(currentRunes+separatorRunes+blockRunes > telegramRichTextRunes ||
				currentBlocks+block.blockCount > telegramRichBlockLimit) {
			flush()
			separatorRunes = 0
		}

		current = append(current, block)
		currentRunes += separatorRunes + blockRunes
		currentBlocks += block.blockCount
	}
	flush()

	return chunks
}

// parseMarkdownBlocks recognizes the block boundaries needed for safe
// splitting without attempting to replace Telegram's rich Markdown parser.
// Fenced code, math blocks, headings, dividers, and tables remain indivisible;
// other blocks are separated at blank lines.
func parseMarkdownBlocks(text string) []markdownBlock {
	if strings.TrimSpace(text) == "" {
		return nil
	}

	lines := strings.Split(text, "\n")
	blocks := make([]markdownBlock, 0, len(lines)/2+1)
	paragraph := make([]string, 0, 4)

	appendBlock := func(raw string, table *parsedTable) {
		raw = strings.Trim(raw, "\r\n")
		if strings.TrimSpace(raw) == "" {
			return
		}
		fallback := raw
		rich := sanitizeRichMarkdown(raw)
		if table != nil {
			rich = renderRichTableHTML(*table)
		}
		count, depth := markdownBlockMetrics(raw, table)
		blocks = append(blocks, markdownBlock{
			rich:       rich,
			fallback:   fallback,
			blockCount: count,
			depth:      depth,
			richEligible: table == nil ||
				tableColumnCount(*table) <= telegramRichTableColumnLimit,
		})
	}
	flushParagraph := func() {
		if len(paragraph) == 0 {
			return
		}
		appendBlock(strings.Join(paragraph, "\n"), nil)
		paragraph = paragraph[:0]
	}

	for i := 0; i < len(lines); {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			flushParagraph()
			i++
			continue
		}

		if fence, ok := markdownFence(trimmed); ok {
			flushParagraph()
			j := i + 1
			for j < len(lines) {
				if closesMarkdownFence(strings.TrimSpace(lines[j]), fence) {
					j++
					break
				}
				j++
			}
			appendBlock(strings.Join(lines[i:j], "\n"), nil)
			i = j
			continue
		}

		if trimmed == "$$" {
			flushParagraph()
			j := i + 1
			for j < len(lines) {
				if strings.TrimSpace(lines[j]) == "$$" {
					j++
					break
				}
				j++
			}
			appendBlock(strings.Join(lines[i:j], "\n"), nil)
			i = j
			continue
		}

		if i+2 < len(lines) && isTableRowLine(line) && isTableSeparatorLine(lines[i+1]) {
			j := i + 2
			for j < len(lines) && isTableRowLine(lines[j]) {
				j++
			}
			if j > i+2 {
				flushParagraph()
				table := parseTable(lines[i:j])
				appendBlock(strings.Join(lines[i:j], "\n"), &table)
				i = j
				continue
			}
		}

		if isStandaloneMarkdownBlock(trimmed) {
			flushParagraph()
			appendBlock(line, nil)
			i++
			continue
		}

		paragraph = append(paragraph, line)
		i++
	}
	flushParagraph()

	return blocks
}

type markdownFenceMarker struct {
	char  byte
	width int
}

func markdownFence(line string) (markdownFenceMarker, bool) {
	line = strings.TrimLeftFunc(line, unicode.IsSpace)
	if len(line) < 3 || (line[0] != '`' && line[0] != '~') {
		return markdownFenceMarker{}, false
	}
	width := 0
	for width < len(line) && line[width] == line[0] {
		width++
	}
	if width < 3 {
		return markdownFenceMarker{}, false
	}
	return markdownFenceMarker{char: line[0], width: width}, true
}

func closesMarkdownFence(line string, fence markdownFenceMarker) bool {
	line = strings.TrimLeftFunc(line, unicode.IsSpace)
	width := 0
	for width < len(line) && line[width] == fence.char {
		width++
	}
	return width >= fence.width && strings.TrimSpace(line[width:]) == ""
}

func isStandaloneMarkdownBlock(line string) bool {
	if isHeadingLine(line) || isDividerLine(line) {
		return true
	}
	return strings.HasPrefix(line, "[^") && strings.Contains(line, "]: ")
}

func isHeadingLine(line string) bool {
	if line == "" || line[0] != '#' {
		return false
	}
	i := 0
	for i < len(line) && line[i] == '#' && i < 6 {
		i++
	}
	return i > 0 && i < len(line) && unicode.IsSpace(rune(line[i]))
}

func isDividerLine(line string) bool {
	compact := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, line)
	if len(compact) < 3 {
		return false
	}
	marker := compact[0]
	if marker != '-' && marker != '*' && marker != '_' {
		return false
	}
	for i := 1; i < len(compact); i++ {
		if compact[i] != marker {
			return false
		}
	}
	return true
}

func markdownBlockMetrics(raw string, table *parsedTable) (int, int) {
	if table != nil {
		return len(table.rows) + 1, 1
	}

	count := 1
	maxDepth := 1
	for _, line := range strings.Split(raw, "\n") {
		depth := markdownLineDepth(line)
		if depth > maxDepth {
			maxDepth = depth
		}
		trimmed := strings.TrimSpace(line)
		if isListItemLine(trimmed) {
			// A list item contributes both an item and its content block.
			count += 2
		}
		if strings.HasPrefix(trimmed, ">") {
			count++
		}
	}
	return count, maxDepth
}

func tableColumnCount(table parsedTable) int {
	columns := len(table.header)
	for _, row := range table.rows {
		if len(row) > columns {
			columns = len(row)
		}
	}
	return columns
}

func markdownLineDepth(line string) int {
	depth := 1
	trimmed := strings.TrimLeftFunc(line, unicode.IsSpace)
	for strings.HasPrefix(trimmed, ">") {
		depth++
		trimmed = strings.TrimLeftFunc(strings.TrimPrefix(trimmed, ">"), unicode.IsSpace)
	}

	indent := len(line) - len(strings.TrimLeft(line, " \t"))
	if isListItemLine(trimmed) {
		depth += 2 * (indent/2 + 1)
	}
	return depth
}

func isListItemLine(line string) bool {
	if len(line) >= 2 && (line[0] == '-' || line[0] == '*' || line[0] == '+') &&
		unicode.IsSpace(rune(line[1])) {
		return true
	}
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	return i > 0 && i+1 < len(line) && line[i] == '.' && unicode.IsSpace(rune(line[i+1]))
}
