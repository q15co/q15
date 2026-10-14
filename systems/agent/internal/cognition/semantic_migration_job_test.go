package cognition

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
)

// migrationTopicRelativePath is the single topic file the scripted model
// writes converted claims into.
const migrationTopicRelativePath = "semantic/topics/legacy.md"

var migrationTopicRuntimePath = semanticTopicsRuntimeDir + "/legacy.md"

var migrationChunkBodyPattern = regexp.MustCompile(
	`^(\S+):(\d+) sha=([0-9a-f]{64}) section=(.*) \| ?(.*)$`,
)

// migrationJobLoader is the in-memory ContextLoader fixture for the
// migration job tests. Its chunk and drain accessors mirror the store's
// semantics on plain strings so the job logic is exercised against the same
// contract. Only the accessors the migration touches return meaningful
// data; the rest return zero values.
type migrationJobLoader struct {
	files              map[string]string
	topics             map[string]string
	ledger             string
	artifacts          map[string]string
	headSeq            int64
	semanticCheckpoint SemanticExtractionCheckpoint
	checkpointErr      error
}

func (l *migrationJobLoader) LoadCoreMemory(
	context.Context,
) (agent.CoreMemory, error) {
	return agent.CoreMemory{}, nil
}

func (l *migrationJobLoader) LoadWorkingMemory(
	context.Context,
) (agent.WorkingMemory, error) {
	return agent.WorkingMemory{}, nil
}

func (l *migrationJobLoader) LoadOpenLoops(context.Context) (string, error) {
	return "", nil
}

func (l *migrationJobLoader) LoadSkillCatalog(
	context.Context,
) (agent.SkillCatalog, error) {
	return agent.SkillCatalog{}, nil
}

func (l *migrationJobLoader) LoadRecentMessages(
	context.Context,
	int,
) ([]conversation.Message, error) {
	return nil, nil
}

func (l *migrationJobLoader) LoadLatestMessages(
	context.Context,
	int,
) ([]conversation.Message, error) {
	return nil, nil
}

func (l *migrationJobLoader) LoadMessagesSinceSeq(
	context.Context,
	int64,
) ([]conversation.Message, error) {
	return nil, nil
}

func (l *migrationJobLoader) LoadMessagesSinceSeqWindow(
	context.Context,
	int64,
	int,
) (TurnWindow, error) {
	return TurnWindow{}, nil
}

func (l *migrationJobLoader) LoadHead(context.Context) (int64, time.Time, error) {
	return l.headSeq, time.Time{}, nil
}

func (l *migrationJobLoader) LoadConsolidationCheckpoint(
	context.Context,
) (ConsolidationCheckpoint, error) {
	return ConsolidationCheckpoint{}, nil
}

func (l *migrationJobLoader) LoadSemanticExtractionCheckpoint(
	context.Context,
) (SemanticExtractionCheckpoint, error) {
	return SemanticExtractionCheckpoint{}, nil
}

func (l *migrationJobLoader) LoadJobState(
	context.Context,
	string,
) (JobState, error) {
	return JobState{}, nil
}

func (l *migrationJobLoader) LoadSemanticMemory(
	context.Context,
) (agent.SemanticMemory, error) {
	out := make([]agent.SemanticMemoryFile, 0, len(semanticMigrationSources))
	for _, relativePath := range semanticMigrationSources {
		out = append(out, agent.SemanticMemoryFile{
			RelativePath: relativePath,
			Content:      strings.TrimSpace(l.files[relativePath]),
		})
	}
	return agent.SemanticMemory{Files: out}, nil
}

func (l *migrationJobLoader) LoadSemanticFileSizes(
	context.Context,
) (map[string]int64, error) {
	sizes := make(map[string]int64, len(l.files)+len(l.topics))
	for relativePath, content := range l.files {
		sizes[relativePath] = int64(len(content))
	}
	for relativePath, content := range l.topics {
		sizes[relativePath] = int64(len(content))
	}
	return sizes, nil
}

func (l *migrationJobLoader) LoadSemanticTopics(
	context.Context,
) (map[string]string, error) {
	topics := make(map[string]string, len(l.topics))
	for relativePath, content := range l.topics {
		topics[relativePath] = strings.TrimSpace(content)
	}
	return topics, nil
}

func (l *migrationJobLoader) LoadWorkingLedger(context.Context) (string, error) {
	return strings.TrimSpace(l.ledger), nil
}

// AppendWorkingLedger appends the runtime way: the line joins the end of the
// ledger, and the bytes above it stay byte-for-byte.
func (l *migrationJobLoader) AppendWorkingLedger(_ context.Context, line string) error {
	line = strings.TrimRight(line, "\n")
	if l.ledger == "" {
		l.ledger = line + "\n"
		return nil
	}
	l.ledger = strings.TrimRight(l.ledger, "\n") + "\n" + line + "\n"
	return nil
}

func (l *migrationJobLoader) ReadFileBytes(
	_ context.Context,
	relativePath string,
) ([]byte, bool, error) {
	if data, ok := l.files[relativePath]; ok {
		return []byte(data), true, nil
	}
	if data, ok := l.topics[relativePath]; ok {
		return []byte(data), true, nil
	}
	return nil, false, nil
}

func (l *migrationJobLoader) WriteFileBytes(
	_ context.Context,
	relativePath string,
	data []byte,
) error {
	l.files[relativePath] = string(data)
	return nil
}

func (l *migrationJobLoader) DeleteFile(_ context.Context, relativePath string) error {
	delete(l.files, relativePath)
	delete(l.topics, relativePath)
	return nil
}

// StoreSemanticExtractionCheckpoint records the checkpoint the job set, or
// fails with the configured error so the unresolved branch is testable.
func (l *migrationJobLoader) StoreSemanticExtractionCheckpoint(
	_ context.Context,
	checkpoint SemanticExtractionCheckpoint,
) (SemanticExtractionCheckpoint, error) {
	if l.checkpointErr != nil {
		return SemanticExtractionCheckpoint{}, l.checkpointErr
	}
	l.semanticCheckpoint = checkpoint
	return checkpoint, nil
}

func (l *migrationJobLoader) LoadCognitionArtifact(
	_ context.Context,
	relativePath string,
) (Artifact, error) {
	content, ok := l.artifacts[relativePath]
	if !ok {
		return Artifact{}, nil
	}
	return Artifact{RelativePath: relativePath, Content: content}, nil
}

func (l *migrationJobLoader) StoreCognitionArtifact(
	_ context.Context,
	artifact Artifact,
) error {
	if l.artifacts == nil {
		l.artifacts = make(map[string]string)
	}
	l.artifacts[artifact.RelativePath] = artifact.Content
	return nil
}

func (l *migrationJobLoader) DeleteCognitionArtifact(
	_ context.Context,
	relativePath string,
) error {
	delete(l.artifacts, relativePath)
	return nil
}

func (l *migrationJobLoader) LoadSemanticChunk(
	_ context.Context,
	relativePath string,
	offset int64,
	length int64,
) (SemanticChunk, error) {
	if length <= 0 {
		return SemanticChunk{}, fmt.Errorf("chunk length must be positive")
	}
	if offset < 0 {
		return SemanticChunk{}, fmt.Errorf("chunk offset must not be negative")
	}
	if _, ok := l.files[relativePath]; !ok && !isMigrationSourceRelative(relativePath) {
		return SemanticChunk{}, fmt.Errorf(
			"path %q is not a semantic source file",
			relativePath,
		)
	}
	data := l.files[relativePath]
	size := int64(len(data))
	if offset > size {
		offset = size
	}
	end := offset + length
	if end > size {
		end = size
	}
	window := data[offset:end]
	content := ""
	switch {
	case len(window) == 0:
	case end == size:
		content = window
	default:
		cut := strings.LastIndexByte(window, '\n')
		if cut >= 0 {
			content = window[:cut+1]
		}
	}
	return SemanticChunk{
		RelativePath: relativePath,
		Offset:       offset,
		Content:      content,
		Size:         size,
		More:         offset+int64(len(content)) < size,
	}, nil
}

func (l *migrationJobLoader) RemoveSemanticLines(
	_ context.Context,
	relativePath string,
	start int64,
	end int64,
	hashes []string,
) (int64, error) {
	if end < start {
		return 0, fmt.Errorf("drain range end before start")
	}
	if len(hashes) == 0 {
		return 0, fmt.Errorf("drain requires line hashes")
	}
	if !isMigrationSourceRelative(relativePath) {
		return 0, fmt.Errorf("path %q is not a semantic source file", relativePath)
	}
	data := l.files[relativePath]
	wanted := make(map[string]struct{}, len(hashes))
	for _, hash := range hashes {
		wanted[hash] = struct{}{}
	}
	unmatched := make(map[string]struct{}, len(wanted))
	for hash := range wanted {
		unmatched[hash] = struct{}{}
	}
	var remaining strings.Builder
	position := int64(0)
	for position < int64(len(data)) {
		next := strings.IndexByte(data[position:], '\n')
		lineEnd := position
		if next >= 0 {
			lineEnd += int64(next) + 1
		} else {
			lineEnd = int64(len(data))
		}
		line := data[position:lineEnd]
		if position >= start && position < end {
			hash := SemanticLineHash(line)
			if _, ok := wanted[hash]; ok {
				delete(unmatched, hash)
				position = lineEnd
				continue
			}
		}
		remaining.WriteString(line)
		position = lineEnd
	}
	if len(unmatched) > 0 {
		return 0, fmt.Errorf("drain found no line matching a listed hash")
	}
	l.files[relativePath] = remaining.String()
	return int64(remaining.Len()), nil
}

func isMigrationSourceRelative(relativePath string) bool {
	for _, source := range semanticMigrationSources {
		if source == relativePath {
			return true
		}
	}
	return false
}

func cloneMigrationLoader(l *migrationJobLoader) *migrationJobLoader {
	copied := &migrationJobLoader{
		files:     make(map[string]string, len(l.files)),
		topics:    make(map[string]string, len(l.topics)),
		ledger:    l.ledger,
		artifacts: make(map[string]string, len(l.artifacts)),
	}
	for key, value := range l.files {
		copied.files[key] = value
	}
	for key, value := range l.topics {
		copied.topics[key] = value
	}
	for key, value := range l.artifacts {
		copied.artifacts[key] = value
	}
	return copied
}

// migrationFixture builds the three-source memory the migration tests run
// over: facts.md carries bulletCount legacy bullets plus a unique tail
// marker bullet, the other roots are structure only.
func migrationFixture(bulletCount int) map[string]string {
	var builder strings.Builder
	builder.WriteString("# Semantic Facts\n\n## Confirmed Facts\n\n")
	for i := 0; i < bulletCount; i++ {
		if i == bulletCount-1 {
			builder.WriteString(
				"- LEGACY TAIL MARKER bullet that must never leak into a bounded chunk\n",
			)
			continue
		}
		fmt.Fprintf(
			&builder,
			"- legacy bullet %03d about the deploy target of project X\n",
			i,
		)
	}
	builder.WriteString("\n## Grounded Inferences\n\n- None\n")
	return map[string]string{
		semanticFactsRelativePath: builder.String(),
		semanticPreferencesRelativePath: "# Semantic Preferences\n" +
			"\n## User Preferences\n\n- None\n",
		semanticProjectsRelativePath: "# Semantic Projects\n\n## Active Projects\n\n- None\n",
	}
}

// migrationCursorFor stores an operator-created cursor tracking the given
// source files from byte zero.
func migrationCursorFor(
	t *testing.T,
	loader *migrationJobLoader,
	tracked ...string,
) {
	t.Helper()
	files := make(map[string]SemanticMigrationFileCursor, len(tracked))
	for _, relativePath := range tracked {
		files[relativePath] = SemanticMigrationFileCursor{}
	}
	if err := SaveSemanticMigrationCursor(
		context.Background(),
		loader,
		SemanticMigrationCursor{Files: files},
	); err != nil {
		t.Fatalf("SaveSemanticMigrationCursor() error = %v", err)
	}
}

// migrationChunkBullets returns the annotated bullet lines of one built
// chunk: source-relative byte offsets, hashes, and raw text.
func migrationChunkBullets(
	t *testing.T,
	spec Spec,
) []migrationSourceLine {
	t.Helper()
	for _, section := range spec.PromptSections {
		if section.Name != "migration_chunk" {
			continue
		}
		var bullets []migrationSourceLine
		for _, line := range strings.Split(section.Body, "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			match := migrationChunkBodyPattern.FindStringSubmatch(line)
			if match == nil {
				t.Fatalf("unparseable chunk annotation %q", line)
			}
			if !isLegacyBulletLine(match[5]) {
				continue
			}
			byteOffset, err := strconv.ParseInt(match[2], 10, 64)
			if err != nil {
				t.Fatalf("chunk annotation offset %q is not a number", match[2])
			}
			bullets = append(bullets, migrationSourceLine{
				ByteOffset: byteOffset,
				Raw:        match[5] + "\n",
				Hash:       match[3],
				Section:    match[4],
			})
		}
		return bullets
	}
	t.Fatalf("prompt section migration_chunk missing from spec")
	return nil
}

// migrationFakeModelOutput is the scripted model for one migration run: it
// converts every annotated bullet of the chunk into one migrated ledger line
// naming a replacement claim in the legacy topic, appends the ledger lines
// to the fixture ledger, and returns the JobOutput ApplyResult expects.
func migrationFakeModelOutput(
	t *testing.T,
	loader *migrationJobLoader,
	spec Spec,
) JobOutput {
	t.Helper()
	bullets := migrationChunkBullets(t, spec)
	appended := make([]string, 0, len(bullets))
	for i, bullet := range bullets {
		appended = append(appended, fmt.Sprintf(
			"2026-10-12T09:00Z run=semantic_memory.migrate migrated \"legacy bullet\""+
				" → c-2026-%04d (%s) (source=%s:%d sha=%s)",
			i+1,
			migrationTopicRelativePath,
			semanticFactsRelativePath,
			bullet.ByteOffset,
			bullet.Hash,
		))
	}
	if len(appended) > 0 {
		trimmed := strings.TrimSpace(loader.ledger)
		if trimmed != "" {
			trimmed += "\n"
		}
		loader.ledger = trimmed + strings.Join(appended, "\n")
	}
	return JobOutput{
		Type:      SemanticMigrationJobType,
		Spec:      spec,
		FinalText: "Drained the annotated chunk.",
		Messages:  migrationModelWriteMessages(),
	}
}

// migrationModelWriteMessages fabricates the successful tool calls the
// scripted model made: one topic edit and one ledger edit.
func migrationModelWriteMessages() []conversation.Message {
	return []conversation.Message{
		conversation.AssistantMessage(conversation.ToolCall(
			"w-topic",
			"edit_file",
			fmt.Sprintf(
				`{"path":%q,"old_string":"x","new_string":"y"}`,
				migrationTopicRuntimePath,
			),
		)),
		conversation.ToolResultMessage("w-topic", "ok", false),
		conversation.AssistantMessage(conversation.ToolCall(
			"w-ledger",
			"edit_file",
			fmt.Sprintf(
				`{"path":%q,"old_string":"x","new_string":"y"}`,
				workingLedgerRuntimePath,
			),
		)),
		conversation.ToolResultMessage("w-ledger", "ok", false),
	}
}

// migrationLedgerLineFor renders one valid migration ledger line citing the
// given copyable annotation tail.
func migrationLedgerLineFor(reference string) string {
	return fmt.Sprintf(
		"2026-10-12T09:00Z run=semantic_memory.migrate migrated \"legacy bullet\" → c-2026-0001 (%s) %s",
		migrationTopicRelativePath,
		reference,
	)
}

// migrateOneChunk drives Build, the scripted model, and ApplyResult once and
// returns the built spec and the parse result.
func migrateOneChunk(
	t *testing.T,
	loader *migrationJobLoader,
	job JobDefinition,
) (Spec, ParsedResult) {
	t.Helper()
	spec, err := job.Build(context.Background(), loader)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	output := migrationFakeModelOutput(t, loader, spec)
	result, err := job.ApplyResult(context.Background(), loader, output)
	if err != nil {
		t.Fatalf("ApplyResult() error = %v", err)
	}
	return spec, result
}

func TestSemanticMigrationPromptGolden(t *testing.T) {
	t.Parallel()
	loader := &migrationJobLoader{
		files:  migrationFixture(4),
		topics: map[string]string{},
	}
	migrationCursorFor(t, loader, semanticFactsRelativePath)
	spec, err := NewSemanticMigrationRegistration().NewJob().Build(
		context.Background(),
		loader,
	)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	prompt, err := renderPrompt(SemanticMigrationJobType, spec)
	if err != nil {
		t.Fatalf("renderPrompt() error = %v", err)
	}
	golden := "testdata/semantic_migration_prompt.golden"
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("MkdirAll(testdata) error = %v", err)
		}
		if err := os.WriteFile(golden, []byte(prompt+"\n"), 0o644); err != nil {
			t.Fatalf("WriteFile(golden) error = %v", err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("ReadFile(golden) error = %v (run with -update to create)", err)
	}
	if string(want) != prompt+"\n" {
		t.Fatalf(
			"prompt drifted from %s; rerun with -update and review the diff\n--- got ---\n%s",
			golden,
			prompt,
		)
	}
}

func TestSemanticMigrationBuildIsBoundedAndWholeLine(t *testing.T) {
	t.Parallel()
	loader := &migrationJobLoader{files: migrationFixture(250)}
	migrationCursorFor(t, loader, semanticFactsRelativePath)
	spec, err := NewSemanticMigrationRegistration().NewJob().Build(
		context.Background(),
		loader,
	)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	chunkSection := migrationPromptSection(t, spec, "migration_chunk")
	if got, want := chunkSection.Attributes["path"], semanticFactsRelativePath; got != want {
		t.Fatalf("chunk path = %q, want %q", got, want)
	}
	if chunkSection.Attributes["more"] != "true" {
		t.Fatalf(
			"chunk more = %q, want true for a multi-chunk fixture",
			chunkSection.Attributes["more"],
		)
	}
	if strings.Contains(chunkSection.Body, "LEGACY TAIL MARKER") {
		t.Fatalf("chunk body leaked the tail of an oversized file into the prompt")
	}
	// Every annotated line is a whole fixture line: the last one must appear
	// verbatim in the source, never as a cut-off fragment.
	lastIndex := strings.LastIndex(chunkSection.Body, "\n")
	secondLast := strings.LastIndex(chunkSection.Body[:lastIndex], "\n")
	lastAnnotation := strings.TrimSpace(chunkSection.Body[secondLast+1 : lastIndex])
	match := migrationChunkBodyPattern.FindStringSubmatch(lastAnnotation)
	if match == nil {
		t.Fatalf("last chunk annotation %q is not parseable", lastAnnotation)
	}
	fixture := loader.files[semanticFactsRelativePath]
	if !strings.Contains(fixture, match[5]+"\n") {
		t.Fatalf("last annotated line %q is not a whole fixture line", match[5])
	}
	// The cursor is not written by Build: no run happened yet.
	if len(loader.artifacts) != 1 {
		t.Fatalf(
			"Build wrote artifacts %v, want only the seeded cursor",
			loader.artifacts,
		)
	}
}

func TestSemanticMigrationRunConsumesOneChunkAndAdvances(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	loader := &migrationJobLoader{files: migrationFixture(250)}
	migrationCursorFor(
		t,
		loader,
		semanticFactsRelativePath,
		semanticPreferencesRelativePath,
	)
	job := NewSemanticMigrationRegistration().NewJob()

	preChunk, err := loader.LoadSemanticChunk(
		ctx,
		semanticFactsRelativePath,
		0,
		semanticMigrationChunkBytes,
	)
	if err != nil {
		t.Fatalf("LoadSemanticChunk() error = %v", err)
	}
	spec, result := migrateOneChunk(t, loader, job)

	removed, err := strconv.ParseInt(
		result.Metadata["source_bytes_drained"],
		10,
		64,
	)
	if err != nil || removed <= 0 {
		t.Fatalf(
			"metadata source_bytes_drained = %q (%v), want a positive count",
			result.Metadata["source_bytes_drained"],
			err,
		)
	}
	wantOffset := preChunk.Offset + int64(len(preChunk.Content)) - removed
	cursor, err := LoadSemanticMigrationCursor(ctx, loader)
	if err != nil {
		t.Fatalf("LoadSemanticMigrationCursor() error = %v", err)
	}
	entry, ok := SemanticMigrationFileCursorFor(cursor, semanticFactsRelativePath)
	if !ok {
		t.Fatalf("cursor lost the facts entry: %+v", cursor)
	}
	if got, want := entry.Offset, wantOffset; got != want {
		t.Fatalf(
			"cursor offset = %d, want %d (bytes consumed minus bytes drained)",
			got,
			want,
		)
	}
	if entry.Finished || entry.Hash == "" {
		t.Fatalf(
			"cursor entry = %+v, want unfinished with a recorded hash",
			entry,
		)
	}
	// One run consumed exactly one chunk: the tail marker bullet and the
	// still-undrained bullets behind it are untouched.
	facts := loader.files[semanticFactsRelativePath]
	if !strings.Contains(facts, "LEGACY TAIL MARKER") {
		t.Fatalf("facts tail marker vanished after one chunk run")
	}
	if !strings.Contains(facts, "- legacy bullet 248") {
		t.Fatalf("undrained bullets behind the chunk window vanished")
	}
	if !strings.Contains(facts, "## Confirmed Facts") {
		t.Fatalf("structure heading left the source file: %q", facts[:64])
	}
	// The run ledgered every drained line with its copied source reference.
	for _, bullet := range migrationChunkBullets(t, spec) {
		reference := fmt.Sprintf(
			"(source=%s:%d sha=%s)",
			semanticFactsRelativePath,
			bullet.ByteOffset,
			bullet.Hash,
		)
		if !strings.Contains(loader.ledger, reference) {
			t.Fatalf(
				"drained line %s:%d is missing its ledger line",
				semanticFactsRelativePath,
				bullet.ByteOffset,
			)
		}
	}
	if got, want := result.Metadata["source_finished"], "false"; got != want {
		t.Fatalf("source_finished = %q, want %q", got, want)
	}
	// The cursor still tracks an unfinished file, so the trigger fires again.
	if !SemanticMigrationCursorHasOpenFile(cursor) {
		t.Fatalf("cursor after one chunk reports no open file: %+v", cursor)
	}
}

func TestSemanticMigrationResumesToTheSameEndState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	job := NewSemanticMigrationRegistration().NewJob()

	// Uninterrupted pass: runs until the cursor is finished and deleted.
	direct := &migrationJobLoader{files: migrationFixture(250)}
	direct.topics = map[string]string{}
	migrationCursorFor(
		t,
		direct,
		semanticFactsRelativePath,
		semanticPreferencesRelativePath,
	)
	for runs := 0; runs < 8; runs++ {
		cursor, err := LoadSemanticMigrationCursor(ctx, direct)
		if err != nil {
			t.Fatalf("LoadSemanticMigrationCursor() error = %v", err)
		}
		if !SemanticMigrationCursorHasOpenFile(cursor) {
			break
		}
		migrateOneChunk(t, direct, job)
	}
	if _, ok := direct.artifacts[SemanticMigrationCursorArtifactPath]; ok {
		t.Fatalf(
			"completed migration left the cursor behind: finishing it deletes it",
		)
	}
	if len(direct.artifacts) != 0 {
		t.Fatalf("completed migration left artifacts behind: %v", direct.artifacts)
	}
	facts := direct.files[semanticFactsRelativePath]
	if strings.Contains(facts, "legacy bullet ") {
		t.Fatalf("drained file still holds legacy bullets: %q", facts)
	}
	if !strings.Contains(facts, "## Confirmed Facts") ||
		!strings.Contains(facts, "## Grounded Inferences") {
		t.Fatalf("drained file lost its section structure: %q", facts)
	}
	// The run ledgered one line per drained bullet. The migration's own
	// finish adds one more line for the checkpoint jump, which the count
	// below deliberately ignores: it counts the per-bullet source citations.
	if got, want := strings.Count(direct.ledger, "(source="), 250; got != want {
		t.Fatalf(
			"ledger citations = %d, want one per drained bullet (%d)",
			got,
			want,
		)
	}

	// Interrupted pass: one chunk, a restart carrying the same persisted
	// state, then completion. The end state must match the uninterrupted one.
	resumed := &migrationJobLoader{files: migrationFixture(250)}
	resumed.topics = map[string]string{}
	migrationCursorFor(
		t,
		resumed,
		semanticFactsRelativePath,
		semanticPreferencesRelativePath,
	)
	migrateOneChunk(t, resumed, job)
	afterFirstChunk := cloneMigrationLoader(resumed)
	for runs := 0; runs < 8; runs++ {
		cursor, err := LoadSemanticMigrationCursor(ctx, afterFirstChunk)
		if err != nil {
			t.Fatalf("LoadSemanticMigrationCursor() error = %v", err)
		}
		if !SemanticMigrationCursorHasOpenFile(cursor) {
			break
		}
		migrateOneChunk(t, afterFirstChunk, job)
	}
	if afterFirstChunk.files[semanticFactsRelativePath] !=
		direct.files[semanticFactsRelativePath] {
		t.Fatalf(
			"resumed facts.md differs from the uninterrupted pass:\n%q\n%q",
			afterFirstChunk.files[semanticFactsRelativePath],
			direct.files[semanticFactsRelativePath],
		)
	}
	if afterFirstChunk.files[semanticPreferencesRelativePath] !=
		direct.files[semanticPreferencesRelativePath] {
		t.Fatalf("resumed preferences.md differs from the uninterrupted pass")
	}
	if afterFirstChunk.ledger != direct.ledger {
		t.Fatalf("resumed ledger differs from the uninterrupted pass")
	}
}

func TestSemanticMigrationReportsSourceMovedUnderTheMigration(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	job := NewSemanticMigrationRegistration().NewJob()

	// A file that changed between runs is reported, not converted over.
	loader := &migrationJobLoader{files: migrationFixture(250)}
	migrationCursorFor(t, loader, semanticFactsRelativePath)
	migrateOneChunk(t, loader, job)
	loader.files[semanticFactsRelativePath] =
		loader.files[semanticFactsRelativePath] + "- sneaky external edit\n"
	_, err := job.Build(ctx, loader)
	if err == nil || !strings.Contains(err.Error(), "changed under the migration") {
		t.Fatalf("Build(moved source) error = %v, want the movement report", err)
	}

	// A file that moved between Build and ApplyResult is reported too.
	pinned := &migrationJobLoader{files: migrationFixture(250)}
	migrationCursorFor(t, pinned, semanticFactsRelativePath)
	spec, err := job.Build(ctx, pinned)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	pinned.files[semanticFactsRelativePath] = strings.Replace(
		pinned.files[semanticFactsRelativePath],
		"legacy bullet 000",
		"rewritten bullet",
		1,
	)
	output := migrationFakeModelOutput(t, pinned, spec)
	if _, err := job.ApplyResult(ctx, pinned, output); err == nil ||
		!strings.Contains(err.Error(), "moved between Build and ApplyResult") {
		t.Fatalf(
			"ApplyResult(moved source) error = %v, want the movement report",
			err,
		)
	}
}

func TestSemanticMigrationEnforcesTheLedgerContract(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	job := NewSemanticMigrationRegistration().NewJob()

	buildSpec := func(loader *migrationJobLoader) (Spec, []migrationSourceLine) {
		spec, err := job.Build(ctx, loader)
		if err != nil {
			t.Fatalf("Build() error = %v", err)
		}
		return spec, migrationChunkBullets(t, spec)
	}
	applyWithLedger := func(loader *migrationJobLoader, spec Spec, ledger string) error {
		t.Helper()
		loader.ledger = ledger
		output := JobOutput{
			Type:      SemanticMigrationJobType,
			Spec:      spec,
			FinalText: "x",
			Messages:  migrationModelWriteMessages(),
		}
		_, err := job.ApplyResult(ctx, loader, output)
		return err
	}

	// Appended ledger lines must parse as migration lines.
	unparseable := &migrationJobLoader{files: migrationFixture(250)}
	migrationCursorFor(t, unparseable, semanticFactsRelativePath)
	spec, _ := buildSpec(unparseable)
	if err := applyWithLedger(unparseable, spec, "this is not a ledger line"); err == nil ||
		!strings.Contains(err.Error(), "does not match the migration ledger format") {
		t.Fatalf("ApplyResult(unparseable ledger) error = %v", err)
	}

	// The cited source must be inside this run's chunk.
	outside := &migrationJobLoader{files: migrationFixture(250)}
	migrationCursorFor(t, outside, semanticFactsRelativePath)
	spec, bullets := buildSpec(outside)
	err := applyWithLedger(outside, spec, migrationLedgerLineFor(fmt.Sprintf(
		"(source=%s:%d sha=%s)",
		semanticFactsRelativePath,
		999999,
		bullets[0].Hash,
	)))
	if err == nil || !strings.Contains(err.Error(), "outside this run's chunk") {
		t.Fatalf("ApplyResult(source outside chunk) error = %v", err)
	}

	// The cited hash must match the cited line.
	mismatched := &migrationJobLoader{files: migrationFixture(250)}
	migrationCursorFor(t, mismatched, semanticFactsRelativePath)
	spec, bullets = buildSpec(mismatched)
	err = applyWithLedger(mismatched, spec, migrationLedgerLineFor(fmt.Sprintf(
		"(source=%s:%d sha=%s)",
		semanticFactsRelativePath,
		bullets[0].ByteOffset,
		strings.Repeat("ab", 32),
	)))
	if err == nil || !strings.Contains(err.Error(), "but the line hashes to") {
		t.Fatalf("ApplyResult(hash mismatch) error = %v", err)
	}

	// A bullet chunk with no ledger lines fails the run.
	silent := &migrationJobLoader{files: migrationFixture(250)}
	migrationCursorFor(t, silent, semanticFactsRelativePath)
	spec, _ = buildSpec(silent)
	if err := applyWithLedger(silent, spec, ""); err == nil ||
		!strings.Contains(err.Error(), "ledgered nothing") {
		t.Fatalf("ApplyResult(empty ledger) error = %v", err)
	}

	// A declined bullet is not stranded: the cursor rewinds to it.
	declined := &migrationJobLoader{files: migrationFixture(250)}
	migrationCursorFor(t, declined, semanticFactsRelativePath)
	spec, bullets = buildSpec(declined)
	declined.ledger = fmt.Sprintf(
		"2026-10-12T09:00Z run=semantic_memory.migrate migrated \"one\""+
			" → c-2026-0001 (%s) (source=%s:%d sha=%s)",
		migrationTopicRelativePath,
		semanticFactsRelativePath,
		bullets[0].ByteOffset,
		bullets[0].Hash,
	)
	result, err := job.ApplyResult(ctx, declined, JobOutput{
		Type:      SemanticMigrationJobType,
		Spec:      spec,
		FinalText: "partial",
		Messages:  migrationModelWriteMessages(),
	})
	if err != nil {
		t.Fatalf("ApplyResult(partial ledger) error = %v", err)
	}
	cursor, err := LoadSemanticMigrationCursor(ctx, declined)
	if err != nil {
		t.Fatalf("LoadSemanticMigrationCursor() error = %v", err)
	}
	entry, ok := SemanticMigrationFileCursorFor(cursor, semanticFactsRelativePath)
	if !ok || entry.Offset != bullets[1].ByteOffset-int64(len(bullets[0].Raw)) {
		t.Fatalf(
			"cursor entry = %+v, want rewind to the first declined bullet at %d",
			entry,
			bullets[1].ByteOffset-int64(len(bullets[0].Raw)),
		)
	}
	if got, want := result.Metadata["source_finished"], "false"; got != want {
		t.Fatalf("source_finished = %q, want %q", got, want)
	}

	// A model write to a semantic source is a contract failure even though
	// the policy already denies it.
	writeRoot := &migrationJobLoader{files: migrationFixture(250)}
	migrationCursorFor(t, writeRoot, semanticFactsRelativePath)
	spec, bullets = buildSpec(writeRoot)
	writeRoot.ledger = migrationLedgerLineFor(fmt.Sprintf(
		"(source=%s:%d sha=%s)",
		semanticFactsRelativePath,
		bullets[0].ByteOffset,
		bullets[0].Hash,
	))
	messages := append(
		[]conversation.Message{
			conversation.AssistantMessage(conversation.ToolCall(
				"w-root",
				"write_file",
				fmt.Sprintf(`{"path":%q,"content":"x"}`, semanticFactsRuntimePath),
			)),
			conversation.ToolResultMessage("w-root", "ok", false),
		},
		migrationModelWriteMessages()...,
	)
	if _, err := job.ApplyResult(ctx, writeRoot, JobOutput{
		Type:      SemanticMigrationJobType,
		Spec:      spec,
		FinalText: "x",
		Messages:  messages,
	}); err == nil || !strings.Contains(err.Error(), "wrote semantic source") {
		t.Fatalf(
			"ApplyResult(root write) error = %v, want the contract failure",
			err,
		)
	}
}

func TestSemanticMigrationCapsNewDestinations(t *testing.T) {
	t.Parallel()
	loader := &migrationJobLoader{
		files: migrationFixture(250),
		topics: map[string]string{
			migrationTopicRelativePath: strings.Repeat("x", 33*1024),
		},
	}
	migrationCursorFor(t, loader, semanticFactsRelativePath)
	job := NewSemanticMigrationRegistration().NewJob()
	spec, err := job.Build(context.Background(), loader)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	// The over-cap topic is only judged once the run wrote it.
	output := migrationFakeModelOutput(t, loader, spec)
	if _, err := job.ApplyResult(
		context.Background(),
		loader,
		output,
	); err == nil || !strings.Contains(err.Error(), "over cap") {
		t.Fatalf(
			"ApplyResult(over-cap topic) error = %v, want the cap failure",
			err,
		)
	}
}

// TestSemanticMigrationCompletionJumpsTheCheckpointToHead drives a fixture to
// its last chunk, which drains every tracked file. The run must end honestly:
// the semantic extraction checkpoint lands on the transcript head (the backlog
// reached it through this cursor, not through that checkpoint), one ledger
// line names the jump, and the cursor is gone so the trigger never fires
// again.
func TestSemanticMigrationCompletionJumpsTheCheckpointToHead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	job := NewSemanticMigrationRegistration().NewJob()
	loader := &migrationJobLoader{
		files:   migrationFixture(250),
		topics:  map[string]string{},
		headSeq: 77,
	}
	migrationCursorFor(
		t,
		loader,
		semanticFactsRelativePath,
		semanticPreferencesRelativePath,
	)

	var finalResult ParsedResult
	for runs := 0; runs < 8; runs++ {
		cursor, err := LoadSemanticMigrationCursor(ctx, loader)
		if err != nil {
			t.Fatalf("LoadSemanticMigrationCursor() error = %v", err)
		}
		if !SemanticMigrationCursorHasOpenFile(cursor) {
			break
		}
		spec, err := job.Build(ctx, loader)
		if err != nil {
			t.Fatalf("Build() error = %v", err)
		}
		result, err := job.ApplyResult(
			ctx,
			loader,
			migrationFakeModelOutput(t, loader, spec),
		)
		if err != nil {
			t.Fatalf("ApplyResult() error = %v", err)
		}
		finalResult = result
	}
	if loader.semanticCheckpoint.LastExtractedSeq != 77 {
		t.Fatalf(
			"semantic checkpoint seq = %d, want the transcript head 77",
			loader.semanticCheckpoint.LastExtractedSeq,
		)
	}
	if !strings.Contains(
		loader.ledger,
		"migration finished: semantic extraction checkpoint set to seq 77",
	) {
		t.Fatalf(
			"ledger = %q, want the jump recorded for seq 77",
			loader.ledger,
		)
	}
	if !strings.Contains(
		loader.ledger,
		"the legacy backlog was drained by this migration and not by that checkpoint",
	) {
		t.Fatalf("ledger = %q, want the jump line to say why", loader.ledger)
	}
	if got, want := strings.Count(loader.ledger, "migration finished"), 1; got != want {
		t.Fatalf("jump ledger lines = %d, want exactly one", got)
	}
	if _, ok := loader.artifacts[SemanticMigrationCursorArtifactPath]; ok {
		t.Fatalf("completed migration left the cursor behind")
	}
	if got, want := finalResult.Metadata["checkpoint_jump_seq"], "77"; got != want {
		t.Fatalf("checkpoint_jump_seq = %q, want %q", got, want)
	}
	if got, want := finalResult.Metadata["checkpoint_jump_recorded"], "true"; got != want {
		t.Fatalf("checkpoint_jump_recorded = %q, want %q", got, want)
	}
}

// TestSemanticMigrationCompletionSurvivesAnUnrecordableHead finishes a
// migration whose head the checkpoint setter rejects. The run does not fail:
// the checkpoint stays where it was, the ledger line names that fact, and the
// run's summary reports it as unresolved.
func TestSemanticMigrationCompletionSurvivesAnUnrecordableHead(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	job := NewSemanticMigrationRegistration().NewJob()
	loader := &migrationJobLoader{
		files:   migrationFixture(250),
		topics:  map[string]string{},
		headSeq: 77,
		checkpointErr: fmt.Errorf(
			"transcript head seq 77 not found in turn history",
		),
	}
	migrationCursorFor(
		t,
		loader,
		semanticFactsRelativePath,
		semanticPreferencesRelativePath,
	)

	var summary string
	for runs := 0; runs < 8; runs++ {
		cursor, err := LoadSemanticMigrationCursor(ctx, loader)
		if err != nil {
			t.Fatalf("LoadSemanticMigrationCursor() error = %v", err)
		}
		if !SemanticMigrationCursorHasOpenFile(cursor) {
			break
		}
		spec, err := job.Build(ctx, loader)
		if err != nil {
			t.Fatalf("Build() error = %v", err)
		}
		output := migrationFakeModelOutput(t, loader, spec)
		result, err := job.ApplyResult(ctx, loader, output)
		if err != nil {
			t.Fatalf("ApplyResult() error = %v, want the run to finish", err)
		}
		summary = result.Summary
	}
	if loader.semanticCheckpoint.LastExtractedSeq != 0 {
		t.Fatalf(
			"semantic checkpoint seq = %d, want the checkpoint left where it was",
			loader.semanticCheckpoint.LastExtractedSeq,
		)
	}
	if !strings.Contains(
		loader.ledger,
		"migration finished: semantic extraction checkpoint left unrecorded",
	) {
		t.Fatalf("ledger = %q, want the unrecorded jump named", loader.ledger)
	}
	if !strings.Contains(summary, "unresolved") {
		t.Fatalf(
			"run summary = %q, want the unresolved item reported",
			summary,
		)
	}
	if _, ok := loader.artifacts[SemanticMigrationCursorArtifactPath]; ok {
		t.Fatalf("completed migration left the cursor behind")
	}
}
