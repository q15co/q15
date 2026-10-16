package claims

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/q15co/q15/systems/agent/internal/atomicfile"
)

// ledgerTimestampLayout is the UTC timestamp prefix every ledger line carries.
const ledgerTimestampLayout = "2006-01-02T15:04Z"

// LedgerOutcome is the fate of one working-memory item that left the control
// surface during a conversion pass (DESIGN.md §3.2 and §3.3).
type LedgerOutcome string

const (
	// OutcomeConverted marks an item rewritten as a durable claim.
	OutcomeConverted LedgerOutcome = "converted"
	// OutcomeMoved marks an item moved to the prospective open-loops file.
	OutcomeMoved LedgerOutcome = "moved"
	// OutcomeDropped marks an item with no durable content left.
	OutcomeDropped LedgerOutcome = "dropped"
	// OutcomeSuperseded marks an old claim replaced by a newer one.
	OutcomeSuperseded LedgerOutcome = "superseded"
	// OutcomeRetracted marks a claim removed because its evidence was false.
	OutcomeRetracted LedgerOutcome = "retracted"
	// OutcomeResolved marks a conflict settled in favour of one claim.
	OutcomeResolved LedgerOutcome = "resolved"
	// OutcomeClosed marks an open loop that finished, with an outcome seq.
	OutcomeClosed LedgerOutcome = "closed"
)

// LedgerEntry is one append-only line of the conversion ledger.
type LedgerEntry struct {
	// Timestamp is the moment of the exit. A zero value means "now", resolved
	// at append time so a caller never has to guess the clock.
	Timestamp time.Time
	// Run names the cognition job or process that recorded the exit, for
	// example "semantic_memory.extract".
	Run string
	// SourceSeq is the transcript marker or range the exit covers ("t4391"
	// or "t4391..t4414"), or a memory citation naming the line a memory
	// repair run drained out of a memory file ("mem:semantic/facts.md:1234").
	SourceSeq string
	// Outcome is what became of the item.
	Outcome LedgerOutcome
	// Description names the item as it appeared in working memory.
	Description string
	// TargetID is the claim or loop ID the item became, or the losing claim
	// ID for resolved/retracted/superseded outcomes.
	TargetID string
	// TargetPath is the repository-relative file the target lives in.
	TargetPath string
	// Note carries any remaining detail, such as the reason for a drop.
	Note string
}

var (
	ledgerAppendMu sync.Mutex
	ledgerNow      = func() time.Time { return time.Now().UTC() }
)

// FormatLedgerEntry renders one ledger line, single-line and newline
// terminated, so the ledger stays greppable and several writers can append
// without interleaving within a line.
func FormatLedgerEntry(entry LedgerEntry) string {
	timestamp := entry.Timestamp.UTC()
	if entry.Timestamp.IsZero() {
		timestamp = ledgerNow()
	}

	var builder strings.Builder
	builder.WriteString(timestamp.Format(ledgerTimestampLayout))

	if run := strings.TrimSpace(entry.Run); run != "" {
		builder.WriteString(" run=")
		builder.WriteString(run)
	}
	if src := strings.TrimSpace(entry.SourceSeq); src != "" {
		builder.WriteString(" src=")
		builder.WriteString(src)
	}
	builder.WriteString(" ")
	builder.WriteString(string(entry.Outcome))

	description := normalizeLedgerField(entry.Description)
	if description != "" {
		builder.WriteString(" ")
		builder.WriteString(strconv.Quote(description))
	}
	if target := strings.TrimSpace(entry.TargetID); target != "" {
		if strings.Contains(target, " ") {
			builder.WriteString(" → ")
			builder.WriteString(strconv.Quote(target))
		} else {
			builder.WriteString(" → ")
			builder.WriteString(target)
		}
		if targetPath := strings.TrimSpace(entry.TargetPath); targetPath != "" {
			builder.WriteString(" (")
			builder.WriteString(targetPath)
			builder.WriteString(")")
		}
	}
	note := normalizeLedgerField(entry.Note)
	if strings.HasPrefix(note, "(") && strings.HasSuffix(note, ")") {
		note = strings.TrimSpace(note[1 : len(note)-1])
	}
	if note != "" {
		builder.WriteString(" (")
		builder.WriteString(note)
		builder.WriteString(")")
	}
	builder.WriteString("\n")
	return builder.String()
}

// normalizeLedgerField strips the newline and carriage return runs that would
// break the one-line-per-exit invariant, and collapses them to spaces.
func normalizeLedgerField(raw string) string {
	raw = strings.ReplaceAll(raw, "\r\n", " ")
	raw = strings.ReplaceAll(raw, "\n", " ")
	raw = strings.ReplaceAll(raw, "\r", " ")
	return strings.TrimSpace(strings.Join(strings.Fields(raw), " "))
}

// AppendLedger appends entries to the append-only conversion ledger at path,
// creating the file and its parent directories when missing.
//
// This is the one memory write that is deliberately *not* an atomic rename:
// the ledger is append-only evidence, so entries are added with a single
// O_APPEND write of the whole batch. Two properties carry the concurrency
// safety:
//
//   - The whole batch is one buffer, so a single write(2) emits every entry
//     with no torn or interleaved lines.
//   - The open and the write happen under a package mutex, because O_APPEND
//     only makes the kernel's write position atomic once the file *is* open.
//     Concurrent OpenFile(O_APPEND) followed by WriteFile races: a process
//     that truncates or recreates the path between them resets the shared
//     append offset, and entries land on top of one another. Serialising the
//     file mutation is what removes that window.
//
// The mutex only orders writes within this process. Cross-process safety is
// still provided by O_APPEND, which keeps each write position-atomic; the
// in-process serialisation is what stops two goroutines from racing the open.
func AppendLedger(path string, entries []LedgerEntry) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("ledger path is required")
	}
	if len(entries) == 0 {
		return nil
	}

	dir := filepath.Dir(path)
	if err := atomicfile.EnsureDirectory(dir, 0o755); err != nil {
		return fmt.Errorf("create ledger dir %q: %w", dir, err)
	}

	var batch strings.Builder
	for _, entry := range entries {
		line := FormatLedgerEntry(entry)
		if _, err := ParseLedgerEntry(strings.TrimSuffix(line, "\n")); err != nil {
			return fmt.Errorf("format ledger entry: %w", err)
		}
		batch.WriteString(line)
	}
	payload := []byte(batch.String())

	ledgerAppendMu.Lock()
	defer ledgerAppendMu.Unlock()

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open ledger %q: %w", path, err)
	}
	written, err := file.Write(payload)
	if err != nil {
		_ = file.Close()
		return fmt.Errorf("append ledger %q: %w", path, err)
	}
	if written != len(payload) {
		_ = file.Close()
		return fmt.Errorf(
			"append ledger %q: short write (%d of %d bytes)", path, written, len(payload),
		)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync ledger %q: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close ledger %q: %w", path, err)
	}
	return nil
}

// ParseLedgerEntry parses one complete single-line conversion record. Run and
// src may appear in either order. Converted and moved records use their first
// parenthesized suffix as a target path and an optional second suffix as a
// reason; other outcomes use one suffix as a reason. Outcome-specific target
// requirements are enforced when a conversion result is applied.
func ParseLedgerEntry(line string) (LedgerEntry, error) {
	var entry LedgerEntry
	if strings.ContainsAny(line, "\r\n") {
		return entry, fmt.Errorf("ledger record must occupy one line")
	}
	timestamp, rest, _ := strings.Cut(strings.TrimSpace(line), " ")
	parsedTime, err := time.Parse(ledgerTimestampLayout, timestamp)
	if err != nil {
		return entry, fmt.Errorf("invalid ledger timestamp: %w", err)
	}
	entry.Timestamp = parsedTime
	for range 2 {
		field, after, ok := strings.Cut(strings.TrimSpace(rest), " ")
		if !ok {
			return entry, fmt.Errorf("ledger record requires run and src")
		}
		rest = after
		key, value, ok := strings.Cut(field, "=")
		if !ok || value == "" {
			return entry, fmt.Errorf("invalid ledger field %q", field)
		}
		switch key {
		case "run":
			if entry.Run != "" || !ledgerRunPattern.MatchString(value) {
				return entry, fmt.Errorf("invalid or duplicate ledger run %q", value)
			}
			entry.Run = value
		case "src":
			if entry.SourceSeq != "" || !validLedgerSource(value) {
				return entry, fmt.Errorf("invalid or duplicate ledger src %q", value)
			}
			entry.SourceSeq = value
		default:
			return entry, fmt.Errorf("unknown ledger field %q", key)
		}
	}
	if entry.Run == "" || entry.SourceSeq == "" {
		return entry, fmt.Errorf("ledger record requires run and src")
	}
	outcome, rest, _ := strings.Cut(strings.TrimSpace(rest), " ")
	entry.Outcome = LedgerOutcome(outcome)
	switch entry.Outcome {
	case OutcomeConverted, OutcomeMoved, OutcomeDropped, OutcomeSuperseded,
		OutcomeRetracted, OutcomeResolved, OutcomeClosed:
	default:
		return entry, fmt.Errorf("invalid ledger outcome %q", outcome)
	}
	entry.Description, rest, err = consumeLedgerQuote(strings.TrimSpace(rest))
	if err != nil || strings.TrimSpace(entry.Description) == "" {
		return entry, fmt.Errorf("ledger record requires a quoted nonempty item")
	}
	if strings.ContainsAny(entry.Description, "\r\n") {
		return entry, fmt.Errorf("ledger item must occupy one line")
	}
	return parseLedgerSuffix(entry, rest)
}

var (
	ledgerRunPattern          = regexp.MustCompile(`^[a-zA-Z0-9_.:-]+$`)
	ledgerRecordPrefixPattern = regexp.MustCompile(`^[0-9]{4}-`)
	// ledgerMemoryPathPattern bounds the repository-relative path a memory
	// citation may name. It deliberately excludes ':' so the path and the
	// byte offset stay unambiguous when split at the first colon.
	ledgerMemoryPathPattern = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
)

// ledgerMemorySourcePrefix opens a memory citation in a src field:
// mem:<relative-path>:<byte-offset> names one line a memory repair run
// drained out of a memory file, where no transcript marker exists to cite.
const ledgerMemorySourcePrefix = "mem:"

func validLedgerSource(source string) bool {
	if strings.HasPrefix(source, ledgerMemorySourcePrefix) {
		_, _, ok := ParseMemorySource(source)
		return ok
	}
	first, last, hasRange := strings.Cut(source, "..")
	parseMarker := func(marker string) (uint64, error) {
		if !strings.HasPrefix(marker, "t") || len(marker) == 1 {
			return 0, fmt.Errorf("invalid transcript marker")
		}
		return strconv.ParseUint(marker[1:], 10, 64)
	}
	from, err := parseMarker(first)
	if err != nil {
		return false
	}
	if !hasRange {
		return true
	}
	to, err := parseMarker(last)
	return err == nil && to >= from
}

// ParseMemorySource splits a memory citation of the form
// mem:<relative-path>:<byte-offset> into its path and offset. The path must
// sit under one of the two memory content roots (semantic/ or working/) and
// match the portable path alphabet; the offset is decimal. Anything else -
// an empty path, a path outside the roots, a negative or non-numeric or
// empty offset, or a range suffix - is refused, because the citation is what
// authorises a repair run to delete the line it names.
func ParseMemorySource(source string) (relativePath string, offset uint64, ok bool) {
	rest, found := strings.CutPrefix(source, ledgerMemorySourcePrefix)
	if !found {
		return "", 0, false
	}
	relative, rawOffset, found := strings.Cut(rest, ":")
	if !found ||
		(!strings.HasPrefix(relative, "semantic/") &&
			!strings.HasPrefix(relative, "working/")) {
		return "", 0, false
	}
	if !ledgerMemoryPathPattern.MatchString(relative) || path.Clean(relative) != relative {
		return "", 0, false
	}
	parsed, err := strconv.ParseUint(rawOffset, 10, 64)
	if err != nil {
		return "", 0, false
	}
	return relative, parsed, true
}

// ValidMemorySource reports whether source is one well-formed memory
// citation. The claims metadata grammar accepts it alongside transcript
// markers, so a claim adopted out of a memory file can cite the line it
// drained from.
func ValidMemorySource(source string) bool {
	_, _, ok := ParseMemorySource(source)
	return ok
}

func consumeLedgerQuote(raw string) (string, string, error) {
	if !strings.HasPrefix(raw, `"`) {
		return "", "", fmt.Errorf("expected quoted value")
	}
	quoted, err := strconv.QuotedPrefix(raw)
	if err != nil {
		return "", "", err
	}
	value, err := strconv.Unquote(quoted)
	return value, raw[len(quoted):], err
}

func parseLedgerSuffix(entry LedgerEntry, rest string) (LedgerEntry, error) {
	rest = strings.TrimSpace(rest)
	if after, ok := strings.CutPrefix(rest, "→ "); ok {
		if strings.HasPrefix(after, `"`) {
			var err error
			entry.TargetID, rest, err = consumeLedgerQuote(after)
			if err != nil {
				return entry, fmt.Errorf("invalid ledger target: %w", err)
			}
		} else {
			entry.TargetID, rest, _ = strings.Cut(after, " ")
		}
		if strings.TrimSpace(entry.TargetID) == "" ||
			strings.ContainsAny(entry.TargetID, "\r\n()") {
			return entry, fmt.Errorf("invalid ledger target")
		}
	}
	var suffixes []string
	for strings.TrimSpace(rest) != "" {
		rest = strings.TrimSpace(rest)
		if !strings.HasPrefix(rest, "(") {
			return entry, fmt.Errorf("unexpected ledger suffix %q", rest)
		}
		end, depth := 0, 0
		for i, char := range rest {
			if char == '(' {
				depth++
			}
			if char == ')' {
				depth--
				if depth == 0 {
					end = i
					break
				}
			}
		}
		if end == 0 || strings.TrimSpace(rest[1:end]) == "" {
			return entry, fmt.Errorf("invalid ledger parenthesized suffix")
		}
		suffixes = append(suffixes, strings.TrimSpace(rest[1:end]))
		rest = rest[end+1:]
		if len(suffixes) > 2 {
			return entry, fmt.Errorf("too many ledger suffixes")
		}
	}
	if entry.TargetID != "" &&
		(entry.Outcome == OutcomeConverted || entry.Outcome == OutcomeMoved) {
		if len(suffixes) > 0 {
			entry.TargetPath = suffixes[0]
		}
		if len(suffixes) == 2 {
			entry.Note = suffixes[1]
		}
	} else if len(suffixes) == 1 {
		entry.Note = suffixes[0]
	} else if len(suffixes) > 1 {
		return entry, fmt.Errorf("ledger outcome %q accepts one reason suffix", entry.Outcome)
	}
	return entry, nil
}

// ParseLedger parses records while ignoring blank lines, markdown headings,
// and standalone HTML comments. Other content is rejected with its line number.
func ParseLedger(data []byte) ([]LedgerEntry, error) {
	lines, err := ledgerContentLines(data)
	if err != nil {
		return nil, err
	}
	var entries []LedgerEntry
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		entry, err := ParseLedgerEntry(line)
		if err != nil {
			return nil, fmt.Errorf("ledger line %d: %w", index+1, err)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func ledgerContentLines(data []byte) ([]string, error) {
	lines := strings.Split(string(data), "\n")
	inComment, commentLine := false, 0
	for index, line := range lines {
		rest := strings.TrimSpace(line)
		for {
			if inComment {
				_, after, closed := strings.Cut(rest, "-->")
				if !closed {
					rest = ""
					break
				}
				rest = strings.TrimSpace(after)
				inComment = false
			}
			after, opens := strings.CutPrefix(rest, "<!--")
			if !opens {
				break
			}
			inComment, commentLine = true, index+1
			rest = after
		}
		lines[index] = rest
	}
	if inComment {
		return nil, fmt.Errorf("ledger line %d: unterminated HTML comment", commentLine)
	}
	return lines, nil
}

// LedgerEntryCount returns the number of records in the file at path. Legacy
// header prose and standalone HTML comments are ignored; malformed date-prefixed
// records fail. A missing file counts as zero.
func LedgerEntryCount(path string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("read ledger %q: %w", path, err)
	}
	lines, err := ledgerContentLines(raw)
	if err != nil {
		return 0, fmt.Errorf("parse ledger %q: %w", path, err)
	}
	count := 0
	for index, line := range lines {
		if !ledgerRecordPrefixPattern.MatchString(strings.TrimSpace(line)) {
			continue
		}
		if _, err := ParseLedgerEntry(line); err != nil {
			return 0, fmt.Errorf("parse ledger %q line %d: %w", path, index+1, err)
		}
		count++
	}
	return count, nil
}
