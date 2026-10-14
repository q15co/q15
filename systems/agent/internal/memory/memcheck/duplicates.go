package memcheck

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/q15co/q15/systems/agent/internal/memory/claims"
)

// DefaultDuplicateThreshold is the normalized token-overlap score at or above
// which a claim pair is a near-duplicate (DESIGN.md §7.3, "threshold to
// tune"). It is the default, not a law: callers tune it by passing their own
// DuplicateOptions.Threshold.
const DefaultDuplicateThreshold = 0.8

// DuplicateRateLimit is the corpus duplicate rate the design requires the
// conversion pass to stay under (DESIGN.md §7.3, "assert rate < 5 %"). A
// report at or above this rate is the failure the conversion job's golden
// tests and the post-migration corpus check are looking for.
const DuplicateRateLimit = 0.05

// semanticFileRelativePaths are the canonical semantic root files a memory
// root owns. Only files that exist are checked; like the store's
// LoadSemanticFileSizes, a partial root is measured for what it has.
var semanticFileRelativePaths = []string{
	"semantic/facts.md",
	"semantic/preferences.md",
	"semantic/projects.md",
}

const semanticTopicsRelativeDir = "semantic/topics"

// NormalizeClaimText folds a claim text the way the near-duplicate comparison
// requires before any token is counted: case is lowered, punctuation and
// symbols become separators, and whitespace runs collapse to one space. Two
// claims that differ only in case, punctuation, or layout normalize to the
// same text.
func NormalizeClaimText(text string) string {
	var builder strings.Builder
	builder.Grow(len(text))
	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			builder.WriteRune(r)
			continue
		}
		builder.WriteByte(' ')
	}
	return strings.Join(strings.Fields(builder.String()), " ")
}

// TokenOverlap scores two claim texts as the Jaccard overlap of their
// normalized token sets: shared tokens over all tokens, from 0 (nothing
// shared) to 1 (identical). Either side with no tokens scores 0, so blank or
// empty claims can never be marked duplicates of anything.
//
// The measure is a DuplicateOptions parameter, so callers can tune the
// threshold against a sharper or looser score without touching this one.
func TokenOverlap(a, b string) float64 {
	first := normalizedTokenSet(a)
	second := normalizedTokenSet(b)
	shared := intersectionCount(first, second)
	union := len(first) + len(second) - shared
	if union == 0 {
		return 0
	}
	return float64(shared) / float64(union)
}

// normalizedTokenSet folds a raw claim text and returns its set of tokens.
func normalizedTokenSet(text string) map[string]struct{} {
	fields := strings.Fields(NormalizeClaimText(text))
	set := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		set[field] = struct{}{}
	}
	return set
}

// intersectionCount returns how many tokens two normalized token sets share.
func intersectionCount(first, second map[string]struct{}) int {
	if len(first) > len(second) {
		first, second = second, first
	}
	shared := 0
	for token := range first {
		if _, ok := second[token]; ok {
			shared++
		}
	}
	return shared
}

// DuplicateOptions tunes the near-duplicate check. The zero value is usable
// and means the defaults below.
type DuplicateOptions struct {
	// Threshold is the normalized token-overlap score at or above which a
	// claim pair counts as a duplicate. A non-positive threshold selects
	// DefaultDuplicateThreshold.
	Threshold float64
	// Measure scores one claim pair. It receives both raw claim texts and must
	// return 0 for shares-nothing and 1 for identical. A nil measure selects
	// TokenOverlap.
	Measure func(a, b string) float64
}

// DefaultDuplicateOptions returns the design's §7.3 settings: the 0.8 overlap
// threshold with the token-overlap measure.
func DefaultDuplicateOptions() DuplicateOptions {
	return DuplicateOptions{
		Threshold: DefaultDuplicateThreshold,
		Measure:   TokenOverlap,
	}
}

// resolved fills in the zero-value defaults so a check never runs with a
// threshold that flags everything or a missing measure.
func (o DuplicateOptions) resolved() DuplicateOptions {
	if o.Threshold <= 0 {
		o.Threshold = DefaultDuplicateThreshold
	}
	if o.Measure == nil {
		o.Measure = TokenOverlap
	}
	return o
}

// DuplicatePair is one flagged near-duplicate pair, named so a failure says
// what it found rather than a bare rate.
type DuplicatePair struct {
	// File is the slash-relative path of the semantic file holding both
	// claims.
	File string
	// FirstID and SecondID are the two claim IDs, ordered as they appear in
	// the file.
	FirstID  string
	SecondID string
	// FirstText and SecondText are the raw claim texts the measure scored.
	FirstText  string
	SecondText string
	// Score is what Measure returned for the pair.
	Score float64
}

// FileDuplicateReport is one semantic file's duplicate measurement.
type FileDuplicateReport struct {
	// Path is the slash-relative path under the memory root.
	Path string
	// Claims is how many claims the file parsed, whether compared or not.
	Claims int
	// ComparedPairs is the number of pairwise comparisons run, the
	// denominator of Rate. Claims that parse to no text are counted in Claims
	// but not compared, so their weight never hides duplicates.
	ComparedPairs int
	// Pairs lists the flagged pairs in claim order.
	Pairs []DuplicatePair
	// Rate is the share of compared pairs that were flagged duplicates; 0 for
	// a file with fewer than two comparable claims.
	Rate float64
}

// DuplicateReport is the corpus-level duplicate measurement of one root.
type DuplicateReport struct {
	// Root is the memory root the report was measured against.
	Root string
	// Files holds one report per semantic file found, in path order.
	Files []FileDuplicateReport
	// Claims, ComparedPairs and Pairs aggregate every file.
	Claims        int
	ComparedPairs int
	Pairs         []DuplicatePair
	// Rate is the corpus duplicate rate: all flagged pairs over all compared
	// pairs. DESIGN.md §7.3 puts the line this must stay under at
	// DuplicateRateLimit.
	Rate float64
}

// CheckDuplicates measures the semantic files under a memory root for
// near-duplicate claim texts. Every claim of a file is compared pairwise with
// every other claim of the same file; the cross-file overlap that the embed
// scanner indexes anyway is deliberately out of scope, mirroring the per-file
// check the design specifies.
//
// The root is a memory root path, not a claim file path: the walk covers the
// canonical semantic roots and the direct children of semantic/topics, the
// same set the store's LoadSemanticFileSizes reports. A root with none of
// those files reports zero rather than failing, because an empty corpus has a
// duplicate rate of zero, not an unknown one; a path that is not a directory
// at all is an error worth naming.
func CheckDuplicates(root string, opts DuplicateOptions) (DuplicateReport, error) {
	opts = opts.resolved()
	files, err := semanticClaimFiles(root)
	if err != nil {
		return DuplicateReport{}, err
	}

	report := DuplicateReport{Root: root}
	for _, file := range files {
		fileReport := measureFileDuplicates(file, opts)
		report.Files = append(report.Files, fileReport)
		report.Claims += fileReport.Claims
		report.ComparedPairs += fileReport.ComparedPairs
		report.Pairs = append(report.Pairs, fileReport.Pairs...)
	}
	if report.ComparedPairs > 0 {
		report.Rate = float64(len(report.Pairs)) / float64(report.ComparedPairs)
	}
	return report, nil
}

// measureFileDuplicates compares one file's claims pairwise and returns the
// per-file report. The rate denominator is the raw pairwise count, so a
// failure names both the ratio and what its numerators were.
func measureFileDuplicates(file claimFile, opts DuplicateOptions) FileDuplicateReport {
	report := FileDuplicateReport{Path: file.relativePath, Claims: len(file.claims)}
	comparable := make([]claims.Claim, 0, len(file.claims))
	for _, claim := range file.claims {
		if NormalizeClaimText(claim.Text) != "" {
			comparable = append(comparable, claim)
		}
	}
	if len(comparable) < 2 {
		return report
	}
	report.ComparedPairs = len(comparable) * (len(comparable) - 1) / 2

	for i := 0; i < len(comparable); i++ {
		for j := i + 1; j < len(comparable); j++ {
			score := opts.Measure(comparable[i].Text, comparable[j].Text)
			if score < opts.Threshold {
				continue
			}
			report.Pairs = append(report.Pairs, DuplicatePair{
				File:       file.relativePath,
				FirstID:    comparable[i].ID,
				SecondID:   comparable[j].ID,
				FirstText:  comparable[i].Text,
				SecondText: comparable[j].Text,
				Score:      score,
			})
		}
	}
	if report.ComparedPairs > 0 {
		report.Rate = float64(len(report.Pairs)) / float64(report.ComparedPairs)
	}
	return report
}

// claimFile is one semantic file's claims as measurement sees them.
type claimFile struct {
	relativePath string
	claims       []claims.Claim
}

// semanticClaimFiles walks a memory root's semantic files in path order and
// parses each into claims. The set mirrors the store's LoadSemanticFileSizes:
// the three canonical roots and the direct children of semantic/topics.
func semanticClaimFiles(root string) ([]claimFile, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("stat memory root %q: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("memory root %q is not a directory", root)
	}

	relativePaths := make([]string, 0, len(semanticFileRelativePaths)+1)
	for _, relative := range semanticFileRelativePaths {
		_, err := os.Stat(filepath.Join(root, filepath.FromSlash(relative)))
		if err == nil {
			relativePaths = append(relativePaths, relative)
			continue
		}
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("stat semantic file %q: %w", relative, err)
		}
	}
	topicsDir := filepath.Join(root, filepath.FromSlash(semanticTopicsRelativeDir))
	topics, err := filepath.Glob(filepath.Join(topicsDir, "*.md"))
	if err != nil {
		return nil, fmt.Errorf("list semantic topics in %q: %w", topicsDir, err)
	}
	for _, topic := range topics {
		relativePaths = append(
			relativePaths,
			semanticTopicsRelativeDir+"/"+filepath.Base(topic),
		)
	}
	sort.Strings(relativePaths)

	files := make([]claimFile, 0, len(relativePaths))
	for _, relative := range relativePaths {
		path := filepath.Join(root, filepath.FromSlash(relative))
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read semantic file %q: %w", relative, err)
		}
		parsed, _, err := claims.ParseClaims(string(raw))
		if err != nil {
			return nil, fmt.Errorf("parse semantic file %q: %w", relative, err)
		}
		files = append(files, claimFile{relativePath: relative, claims: parsed})
	}
	return files, nil
}
