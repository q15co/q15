package cognition

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/q15co/q15/systems/agent/internal/memory/claims"
)

var (
	claimIDPattern           = regexp.MustCompile(`^c-[0-9]{4}-[0-9]{4,}$`)
	loopHeadPattern          = regexp.MustCompile(`^[-*+] \[(l-[0-9]{4}-[0-9]{4,})\]\s+(.+)`)
	sourceIDPattern          = regexp.MustCompile(`^t[1-9][0-9]*$`)
	historicalClaimIDPattern = regexp.MustCompile(`\bc-[0-9]{4}-[0-9]{4,}\b`)
	metadataKeyPattern       = regexp.MustCompile(`(?:^|,\s*)([a-z]+):\s*`)
)

// conversionBaseline captures raw evidence rather than prompt-normalized text.
func conversionBaselineFiles(ctx context.Context, loader ContextLoader) (map[string]string, error) {
	sizes, err := loader.LoadSemanticFileSizes(ctx)
	if err != nil {
		return nil, fmt.Errorf("list conversion baseline files: %w", err)
	}
	paths := append(
		semanticRuntimePaths(),
		workingMemoryRuntimePath,
		workingLedgerRuntimePath,
		openLoopsRuntimePath,
	)
	for relative := range sizes {
		paths = append(paths, memoryRuntimePrefix+relative)
	}
	baseline := make(map[string]string, len(paths))
	for _, runtimePath := range paths {
		raw, _, err := loader.ReadFileBytes(
			ctx,
			strings.TrimPrefix(runtimePath, memoryRuntimePrefix),
		)
		if err != nil {
			return nil, fmt.Errorf("read conversion baseline %s: %w", runtimePath, err)
		}
		baseline[runtimePath] = string(raw)
	}
	return baseline, nil
}

func validateExitTargets(
	ctx context.Context,
	loader ContextLoader,
	entries []claims.LedgerEntry,
) error {
	for _, entry := range entries {
		switch entry.Outcome {
		case claims.OutcomeDropped:
			if entry.TargetID != "" || entry.TargetPath != "" ||
				strings.TrimSpace(entry.Note) == "" {
				return fmt.Errorf("dropped ledger exit requires a reason and no target")
			}
		case claims.OutcomeConverted:
			runtimePath := memoryRuntimePrefix + entry.TargetPath
			if semanticCapFor(runtimePath) == 0 || !claimIDPattern.MatchString(entry.TargetID) {
				return fmt.Errorf(
					"converted ledger exit has invalid claim target %q (%s)",
					entry.TargetID,
					entry.TargetPath,
				)
			}
			raw, existed, err := loader.ReadFileBytes(ctx, entry.TargetPath)
			if err != nil {
				return fmt.Errorf("read converted ledger target: %w", err)
			}
			if !existed {
				return fmt.Errorf("converted ledger target %s is missing", entry.TargetPath)
			}
			found := false
			for _, item := range workingItems(string(raw)) {
				parsed, _, err := claims.ParseClaims(item)
				if err != nil {
					return err
				}
				if len(parsed) != 1 || parsed[0].ID != entry.TargetID {
					continue
				}
				if err := validateClaimItem(item); err != nil {
					return fmt.Errorf("invalid converted target %s: %w", entry.TargetID, err)
				}
				found = true
			}
			if !found {
				return fmt.Errorf(
					"converted ledger target %s does not exist in %s",
					entry.TargetID,
					entry.TargetPath,
				)
			}
		case claims.OutcomeMoved:
			if entry.TargetPath != "working/OPEN_LOOPS.md" {
				return fmt.Errorf("moved ledger exit must target working/OPEN_LOOPS.md")
			}
			raw, _, err := loader.ReadFileBytes(ctx, entry.TargetPath)
			if err != nil {
				return fmt.Errorf("read moved ledger target: %w", err)
			}
			loops, err := parseOpenLoops(string(raw))
			if err != nil {
				return fmt.Errorf("invalid moved target %s: %w", entry.TargetID, err)
			}
			found := false
			for _, loop := range loops {
				if loop.ID == entry.TargetID {
					found = true
				}
			}
			if !found {
				return fmt.Errorf("moved ledger target %s does not exist", entry.TargetID)
			}
		}
	}
	return nil
}

func validateClaimItem(item string) error {
	parsed, _, err := claims.ParseClaims(item)
	if err != nil {
		return err
	}
	if len(parsed) != 1 || !claimIDPattern.MatchString(parsed[0].ID) ||
		strings.TrimSpace(parsed[0].Text) == "" {
		return fmt.Errorf("claim requires a stable ID and statement")
	}
	fields, err := itemMetadata(item, "status", "kind", "confidence", "src", "rev")
	if err != nil {
		return err
	}
	if fields["status"] != "active" && fields["status"] != "conflicted" {
		return fmt.Errorf("invalid claim status %q", fields["status"])
	}
	switch fields["kind"] {
	case "fact", "inference", "preference":
	default:
		return fmt.Errorf("invalid claim kind %q", fields["kind"])
	}
	switch fields["confidence"] {
	case "confirmed", "likely", "uncertain":
	default:
		return fmt.Errorf("invalid claim confidence %q", fields["confidence"])
	}
	if !validSourceList(fields["src"]) {
		return fmt.Errorf("invalid claim src %q", fields["src"])
	}
	if _, err := time.Parse(time.DateOnly, fields["rev"]); err != nil {
		return fmt.Errorf("invalid claim rev %q", fields["rev"])
	}
	return nil
}

func validateLoopItem(item string) error {
	if loopHeadPattern.FindStringSubmatch(item) == nil {
		return fmt.Errorf("loop requires a stable ID and intention")
	}
	fields, err := itemMetadata(item, "trigger", "owner", "src")
	if err != nil {
		return err
	}
	if fields["owner"] != "agent" && fields["owner"] != "user" {
		return fmt.Errorf("invalid loop owner %q", fields["owner"])
	}
	if !sourceIDPattern.MatchString(fields["src"]) {
		return fmt.Errorf("invalid loop src %q", fields["src"])
	}
	trigger := fields["trigger"]
	if trigger == "next-session" {
		return nil
	}
	if date, ok := strings.CutPrefix(trigger, "date:"); ok {
		if _, err := time.Parse(time.DateOnly, date); err == nil {
			return nil
		}
	}
	if quoted, ok := strings.CutPrefix(trigger, "event:"); ok {
		if event, err := strconv.Unquote(quoted); err == nil && strings.TrimSpace(event) != "" {
			return nil
		}
	}
	return fmt.Errorf("invalid loop trigger %q", trigger)
}

func itemMetadata(item string, required ...string) (map[string]string, error) {
	open := strings.LastIndex(item, "{")
	endBrace := strings.LastIndex(item, "}")
	if open < 0 || endBrace < open || strings.TrimSpace(item[endBrace+1:]) != "" {
		return nil, fmt.Errorf("missing canonical metadata")
	}
	raw := strings.TrimSpace(item[open+1 : endBrace])
	matches := metadataKeyPattern.FindAllStringSubmatchIndex(raw, -1)
	outsideQuotes := make([]bool, len(raw))
	quoted, escaped := false, false
	for i, char := range []byte(raw) {
		outsideQuotes[i] = !quoted
		if escaped {
			escaped = false
			continue
		}
		if char == '\\' && quoted {
			escaped = true
			continue
		}
		if char == '"' {
			quoted = !quoted
		}
	}
	filtered := matches[:0]
	for _, match := range matches {
		if outsideQuotes[match[0]] {
			filtered = append(filtered, match)
		}
	}
	matches = filtered
	if len(matches) == 0 || matches[0][0] != 0 {
		return nil, fmt.Errorf("malformed metadata")
	}
	fields := make(map[string]string, len(matches))
	allowed := make(map[string]bool, len(required))
	for _, key := range required {
		allowed[key] = true
	}
	for i, match := range matches {
		key := raw[match[2]:match[3]]
		if !allowed[key] || fields[key] != "" {
			return nil, fmt.Errorf("unknown or duplicate metadata field %q", key)
		}
		end := len(raw)
		if i+1 < len(matches) {
			end = matches[i+1][0]
		}
		fields[key] = strings.TrimSpace(raw[match[1]:end])
	}
	for _, key := range required {
		if fields[key] == "" {
			return nil, fmt.Errorf("missing metadata field %s", key)
		}
	}
	return fields, nil
}

func validSourceList(raw string) bool {
	for _, source := range strings.Split(raw, ",") {
		if !sourceIDPattern.MatchString(strings.TrimSpace(source)) {
			return false
		}
	}
	return raw != ""
}

func checkSemanticClaims(
	ctx context.Context,
	loader ContextLoader,
	baseline map[string]string,
	written []string,
) error {
	sizes, err := loader.LoadSemanticFileSizes(ctx)
	if err != nil {
		return fmt.Errorf("list semantic claims: %w", err)
	}
	before := make(map[string]claims.Claim)
	for runtimePath, raw := range baseline {
		if semanticCapFor(runtimePath) == 0 {
			continue
		}
		parsed, _, err := claims.ParseClaims(raw)
		if err != nil {
			return err
		}
		for _, claim := range parsed {
			before[claim.ID] = claim
		}
	}
	history, err := loader.LoadConversionLedgerHistory(ctx)
	if err != nil {
		return fmt.Errorf("load historical claim IDs: %w", err)
	}
	historical := make(map[string]bool)
	for _, id := range historicalClaimIDPattern.FindAllString(history+"\n"+baseline[workingLedgerRuntimePath], -1) {
		historical[id] = true
	}
	changed := make(map[string]bool, len(written))
	for _, runtimePath := range written {
		changed[runtimePath] = true
	}
	seen := make(map[string]string)
	for relative := range sizes {
		runtimePath := memoryRuntimePrefix + relative
		raw, existed, err := loader.ReadFileBytes(ctx, relative)
		if err != nil {
			return fmt.Errorf("read semantic claims %s: %w", relative, err)
		}
		if !existed {
			return fmt.Errorf("semantic claim inventory file %s is missing", relative)
		}
		parsed, _, err := claims.ParseClaims(string(raw))
		if err != nil {
			return err
		}
		for _, claim := range parsed {
			if previous, ok := seen[claim.ID]; ok {
				return fmt.Errorf(
					"duplicate claim ID %s in %s and %s",
					claim.ID,
					previous,
					relative,
				)
			}
			seen[claim.ID] = relative
			old, existedBefore := before[claim.ID]
			if !existedBefore && historical[claim.ID] {
				return fmt.Errorf("claim ID %s was previously used and cannot be reused", claim.ID)
			}
			if existedBefore && changed[runtimePath] {
				for _, source := range old.Src {
					found := false
					for _, current := range claim.Src {
						if current == source {
							found = true
						}
					}
					if !found {
						return fmt.Errorf(
							"claim %s removed existing provenance %s",
							claim.ID,
							source,
						)
					}
				}
				if claim.Rev < old.Rev {
					return fmt.Errorf("claim %s moved revision backwards", claim.ID)
				}
			}
		}
		if !changed[runtimePath] {
			continue
		}
		for _, item := range workingItems(string(raw)) {
			if strings.Contains(item, "→ topics/") && relative == semanticProjectsRelativePath {
				continue
			}
			if err := validateClaimItem(item); err != nil {
				return fmt.Errorf("invalid written semantic claim in %s: %w", relative, err)
			}
		}
	}
	entries, err := appendedWorkingLedgerEntries(ctx, loader, baseline, "semantic-memory run")
	if err != nil {
		return err
	}
	recorded := make(map[string]bool)
	for _, entry := range entries {
		switch entry.Outcome {
		case claims.OutcomeSuperseded, claims.OutcomeRetracted, claims.OutcomeResolved:
			if !claimIDPattern.MatchString(entry.TargetID) || strings.TrimSpace(entry.Note) == "" {
				return fmt.Errorf("claim lifecycle entry requires the old claim ID and a reason")
			}
			recorded[entry.TargetID] = true
		}
	}
	for id := range before {
		if _, remains := seen[id]; !remains && !recorded[id] {
			return fmt.Errorf("removed durable claim %s has no lifecycle ledger record", id)
		}
	}
	return nil
}

type conversionOpenLoop struct {
	ID      string
	Text    string
	Trigger string
	Owner   string
	Src     string
	Item    string
}

func parseOpenLoops(content string) ([]conversionOpenLoop, error) {
	var loops []conversionOpenLoop
	seen := make(map[string]bool)
	for _, item := range workingItems(content) {
		if err := validateLoopItem(item); err != nil {
			return nil, err
		}
		match := loopHeadPattern.FindStringSubmatch(item)
		if seen[match[1]] {
			return nil, fmt.Errorf("duplicate open-loop ID %s", match[1])
		}
		seen[match[1]] = true
		fields, err := itemMetadata(item, "trigger", "owner", "src")
		if err != nil {
			return nil, err
		}
		text := match[2]
		if open := strings.Index(text, "{"); open >= 0 {
			text = text[:open]
		}
		loops = append(
			loops,
			conversionOpenLoop{
				ID:      match[1],
				Text:    strings.TrimSpace(text),
				Trigger: fields["trigger"],
				Owner:   fields["owner"],
				Src:     fields["src"],
				Item:    item,
			},
		)
	}
	return loops, nil
}

func conversionClaimIDSummary(
	ctx context.Context,
	loader ContextLoader,
	baseline map[string]string,
) (string, error) {
	history, err := loader.LoadConversionLedgerHistory(ctx)
	if err != nil {
		return "", fmt.Errorf("load historical claim IDs: %w", err)
	}
	var source strings.Builder
	source.WriteString(history)
	for _, content := range baseline {
		source.WriteByte('\n')
		source.WriteString(content)
	}
	highest := make(map[string]int)
	for _, id := range historicalClaimIDPattern.FindAllString(source.String(), -1) {
		fields := strings.Split(id, "-")
		number, err := strconv.Atoi(fields[2])
		if err != nil {
			return "", fmt.Errorf("invalid historical claim ID %s: %w", id, err)
		}
		if number > highest[fields[1]] {
			highest[fields[1]] = number
		}
	}
	years := make([]string, 0, len(highest))
	for year := range highest {
		years = append(years, year)
	}
	sort.Strings(years)
	var lines []string
	for _, year := range years {
		lines = append(
			lines,
			fmt.Sprintf("%s: allocate above c-%s-%04d", year, year, highest[year]),
		)
	}
	if len(lines) == 0 {
		return "No prior claim IDs are recorded. Start at c-YYYY-0001 for the current year.", nil
	}
	return strings.Join(lines, "\n"), nil
}
