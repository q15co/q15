package history

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
)

// ContextSettings separates tunable working limits from catalog context ceilings.
type ContextSettings struct {
	WorkingBudgetTokens   int
	ModelBudgetTokens     map[string]int
	RecentTurns           int
	ContextWindowFraction float64
	RecentHistoryFraction float64
	MaxRequestBytes       int
	RetrievalLimit        int
	MinSimilarity         float32
	Tools                 []agent.ToolDefinition
}

// ContextService prepares one immutable retrieval snapshot for each agent run.
type ContextService struct {
	indexer  *Indexer
	settings ContextSettings
}

// NewContextService configures a bounded prompt source over Postgres history.
func NewContextService(indexer *Indexer, settings ContextSettings) (*ContextService, error) {
	if indexer == nil {
		return nil, errors.New("history context requires an indexer")
	}
	if settings.WorkingBudgetTokens == 0 {
		settings.WorkingBudgetTokens = 64000
	}
	if settings.ContextWindowFraction == 0 {
		settings.ContextWindowFraction = 0.25
	}
	if settings.RecentHistoryFraction == 0 {
		settings.RecentHistoryFraction = 1.0 / 3
	}
	if settings.MaxRequestBytes == 0 {
		settings.MaxRequestBytes = 4 * 1024 * 1024
	}
	if settings.RecentTurns == 0 {
		settings.RecentTurns = 4
	}
	if settings.RetrievalLimit == 0 {
		settings.RetrievalLimit = 6
	}
	if settings.WorkingBudgetTokens < 1024 || settings.RecentTurns < 1 ||
		settings.RecentTurns > 32 ||
		settings.RetrievalLimit < 1 ||
		settings.RetrievalLimit > 32 ||
		settings.MaxRequestBytes < 1024 {
		return nil, errors.New("invalid history context budget or tier limits")
	}
	for _, fraction := range []float64{settings.ContextWindowFraction, settings.RecentHistoryFraction} {
		if math.IsNaN(fraction) || math.IsInf(fraction, 0) || fraction <= 0 || fraction > 1 {
			return nil, errors.New(
				"history context fractions must be greater than zero and at most one",
			)
		}
	}
	settings.ModelBudgetTokens = cloneBudgets(settings.ModelBudgetTokens)
	settings.Tools = slices.Clone(settings.Tools)
	return &ContextService{indexer: indexer, settings: settings}, nil
}

type contextGroup struct {
	id        int64
	createdAt time.Time
	kind      string
	messages  []conversation.Message
	digest    string
	excerpt   string
	score     *float32
	protected bool
	dropped   bool
}

type contextSnapshot struct {
	groups          []contextGroup
	input           []conversation.Message
	systemCount     int
	settings        ContextSettings
	lexicalFallback bool
}

// PrepareContext loads source history once; later tool steps reuse that snapshot.
func (s *ContextService) PrepareContext(
	ctx context.Context,
	system, input []conversation.Message,
) (agent.PreparedContext, error) {
	turns, byTurn, err := s.indexer.store.contextWindow(
		ctx,
		s.settings.RecentTurns,
	)
	if err != nil {
		return agent.PreparedContext{}, err
	}
	snapshot := &contextSnapshot{
		systemCount: len(system),
		input:       conversation.CloneMessages(input),
		settings:    s.settings,
	}
	var protectedAssistant int64
	for _, turn := range turns {
		if slices.ContainsFunc(
			turn.Messages,
			func(message conversation.Message) bool { return message.Role == conversation.AssistantRole },
		) {
			protectedAssistant = turn.Seq
			break
		}
	}
	var recentIDs []int64
	for n := len(turns) - 1; n >= 0; n-- {
		turn := turns[n]
		group := contextGroup{
			id:        turn.Seq,
			createdAt: turn.CreatedAt,
			kind:      "gap",
			digest:    byTurn[turn.Seq],
		}
		if n < s.settings.RecentTurns || turn.Seq == protectedAssistant {
			group.kind, group.messages = "recent", turn.Messages
			group.protected = n == 0 || turn.Seq == protectedAssistant
			recentIDs = append(recentIDs, turn.Seq)
		}
		if group.digest == "" {
			group.excerpt = boundedText(renderSource(turn, 1024), 2048)
		}
		snapshot.groups = append(snapshot.groups, group)
	}
	query := retrievalQuery(input, turns, byTurn)
	if query != "" {
		result, err := s.indexer.Search(
			ctx,
			query,
			SearchOptions{
				Limit:          s.settings.RetrievalLimit,
				ExcludeTurnIDs: recentIDs,
				MinSimilarity:  s.settings.MinSimilarity,
				SummariesOnly:  true,
			},
		)
		if err != nil {
			return agent.PreparedContext{}, err
		}
		snapshot.lexicalFallback = result.LexicalFallback
		for _, hit := range slices.Backward(result.Hits) {
			snapshot.groups = append(
				snapshot.groups,
				contextGroup{
					id:        hit.TurnID,
					createdAt: hit.CreatedAt,
					kind:      "retrieved_" + hit.Kind,
					excerpt:   hit.Text,
					score:     hit.Similarity,
				},
			)
		}
	}
	initial := append(conversation.CloneMessages(system), conversation.CloneMessages(input)...)
	messages, err := snapshot.fit(
		ctx,
		agent.ModelContext{},
		initial,
		s.settings.Tools,
		len(initial),
		false,
	)
	var budgetError *agent.RequestBudgetError
	var tokenError *agent.TokenBudgetError
	if errors.As(err, &budgetError) || errors.As(err, &tokenError) {
		// The selected model may have a larger override. Fit each actual attempt before dispatch.
		messages, err = initial, nil
	}
	if err != nil {
		return agent.PreparedContext{}, err
	}
	return agent.PreparedContext{Messages: messages, FitRequest: snapshot.fitRequest}, nil
}

func retrievalQuery(
	input []conversation.Message,
	recent []conversation.Turn,
	digests map[int64]string,
) string {
	var text strings.Builder
	for _, message := range input {
		if body := strings.TrimSpace(conversation.TextValue(message)); body != "" {
			text.WriteString(body)
			text.WriteByte('\n')
		}
	}
	query := strings.TrimSpace(text.String())
	if query == "" {
		return ""
	}
	query = boundedText(query, 4096)
	if len(recent) == 0 || !needsPreviousContext(query) {
		return query
	}
	previous := recent[0]
	anchor := digests[previous.Seq]
	if anchor == "" {
		anchor = renderSource(previous, 1024)
	}
	return query + fmt.Sprintf("\nPrevious turn %d: ", previous.Seq) + boundedText(anchor, 2048)
}

func needsPreviousContext(query string) bool {
	query = strings.Trim(strings.ToLower(query), " .!?\n\t")
	switch query {
	case "ok", "okay", "yes", "no", "ja", "nee", "go ahead", "do it", "continue", "proceed", "why":
		return true
	}
	words := strings.Fields(query)
	if len(words) > 6 {
		return false
	}
	for _, word := range words {
		if word == "that" || word == "this" || word == "it" || word == "same" {
			return true
		}
	}
	return false
}

func (s *contextSnapshot) fitRequest(
	ctx context.Context,
	model agent.ModelContext,
	messages []conversation.Message,
	tools []agent.ToolDefinition,
	generatedStart int,
) ([]conversation.Message, error) {
	return s.fit(ctx, model, messages, tools, generatedStart, true)
}

func (s *contextSnapshot) fit(
	ctx context.Context,
	model agent.ModelContext,
	messages []conversation.Message,
	tools []agent.ToolDefinition,
	generatedStart int,
	emit bool,
) ([]conversation.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if generatedStart < s.systemCount || generatedStart > len(messages) {
		return nil, errors.New("invalid active-run context boundary")
	}
	groups := slices.Clone(s.groups)
	system := conversation.CloneMessages(messages[:s.systemCount])
	active := append(
		conversation.CloneMessages(s.input),
		conversation.CloneMessages(messages[generatedStart:])...)
	limit := s.workingLimit(model)
	maxBytes := s.settings.MaxRequestBytes
	if maxBytes == 0 {
		maxBytes = 4 * 1024 * 1024
	}
	toolCap, reasoningCap := 8192, 2048
	compact := false
	materialize := func() ([]conversation.Message, int, int, error) {
		result := append(
			conversation.CloneMessages(system),
			renderGroups(groups, toolCap, reasoningCap)...)
		current := capParts(active, toolCap, reasoningCap)
		if compact {
			compactActiveEvidence(current)
		}
		result = append(result, current...)
		size, err := agent.RequestEncodedBytes(result, tools)
		if err != nil {
			return nil, 0, 0, err
		}
		if size > maxBytes {
			return result, size, limit + 1, nil
		}
		tokens, err := agent.RequestEstimatedTokens(result, tools)
		return result, size, tokens, err
	}
	result, size, tokens, err := materialize()
	if err != nil {
		return nil, err
	}
	over := func() bool { return tokens > limit || size > maxBytes }
	recentAllowance, err := s.recentAllowance(system, active, groups, tools, limit)
	if err != nil {
		return nil, err
	}
	recentSize := func() (int, error) {
		var recent []contextGroup
		for _, group := range groups {
			if group.kind == "recent" || group.kind == "gap" {
				recent = append(recent, group)
			}
		}
		return historyTokens(renderGroups(recent, toolCap, reasoningCap))
	}
	for n := range groups {
		recentTokens, err := recentSize()
		if err != nil {
			return nil, err
		}
		if recentTokens <= recentAllowance {
			break
		}
		if groups[n].kind != "recent" || groups[n].protected {
			continue
		}
		groups[n].kind = "gap"
		result, size, tokens, err = materialize()
		if err != nil {
			return nil, err
		}
	}
	for over() && toolCap > 512 {
		toolCap /= 2
		reasoningCap = max(256, reasoningCap/2)
		result, size, tokens, err = materialize()
		if err != nil {
			return nil, err
		}
	}
	// Keyword-only matches have no comparable semantic confidence. They leave
	// first, followed by weaker semantic matches and optional continuity summaries.
	for _, tier := range []int{0, 1, 2} {
		for n := range groups {
			if !over() {
				break
			}
			group := &groups[n]
			priority := groupPriority(group.kind) - 1
			if strings.HasPrefix(group.kind, "retrieved") && group.score != nil {
				priority = 1
			}
			if group.kind == "gap" {
				priority = 2
			}
			if group.protected || priority != tier {
				continue
			}
			group.dropped = true
			result, size, tokens, err = materialize()
			if err != nil {
				return nil, err
			}
		}
	}
	if over() {
		compact = true
		result, size, tokens, err = materialize()
		if err != nil {
			return nil, err
		}
	}
	if size > maxBytes {
		return nil, &agent.RequestBudgetError{Bytes: size, Limit: maxBytes}
	}
	if tokens > limit {
		return nil, &agent.TokenBudgetError{EstimatedTokens: tokens, LimitTokens: limit}
	}
	if emit {
		log.Printf(
			"q15: history context ref=%q canonical_bytes=%d max_bytes=%d estimated_tokens=%d limit_tokens=%d estimator=o200k_base_margin25 lexical_fallback=%t selected=%v",
			model.Ref,
			size,
			maxBytes,
			tokens,
			limit,
			s.lexicalFallback,
			contextLabels(groups),
		)
	}
	return result, nil
}

func historyTokens(messages []conversation.Message) (int, error) {
	tokens, err := agent.RequestEstimatedTokens(messages, nil)
	return max(0, tokens-160), err
}

func (s *contextSnapshot) recentAllowance(
	system, active []conversation.Message,
	groups []contextGroup,
	tools []agent.ToolDefinition,
	limit int,
) (int, error) {
	base := append(conversation.CloneMessages(system), capParts(active, 8192, 2048)...)
	maxBytes := s.settings.MaxRequestBytes
	if maxBytes == 0 {
		maxBytes = 4 * 1024 * 1024
	}
	size, err := agent.RequestEncodedBytes(base, tools)
	if err != nil {
		return 0, err
	}
	if size > maxBytes {
		return 0, nil
	}
	used, err := agent.RequestEstimatedTokens(base, tools)
	if err != nil {
		return 0, err
	}
	available := max(0, limit-used)
	var retrieved []contextGroup
	for _, group := range groups {
		if strings.HasPrefix(group.kind, "retrieved") {
			retrieved = append(retrieved, group)
		}
	}
	retrievalTokens, err := historyTokens(renderGroups(retrieved, 8192, 2048))
	fraction := s.settings.RecentHistoryFraction
	if fraction == 0 {
		fraction = 1.0 / 3
	}
	return max(int(float64(available)*fraction), available-retrievalTokens), err
}

func compactActiveEvidence(messages []conversation.Message) {
	latestAssistant := -1
	for n, message := range messages {
		if message.Role == conversation.AssistantRole {
			latestAssistant = n
		}
	}
	for n := range messages {
		if messages[n].Role == conversation.UserRole {
			continue
		}
		for j := range messages[n].Parts {
			part := &messages[n].Parts[j]
			switch part.Type {
			case conversation.ToolResultPartType:
				if len(part.Content) > 192 {
					part.Content = "[Tool executed. Result omitted to fit the context budget. Use a narrower query or pagination; do not repeat a mutation to recover its output.]"
				}
			case conversation.ToolCallPartType:
				if len(part.Arguments) > 1024 {
					part.Arguments = `{"omitted":"Tool already executed; arguments omitted to fit context. Do not repeat a mutation."}`
				}
			case conversation.TextPartType:
				if n != latestAssistant && len(part.Text) > 512 {
					part.Text = "[Earlier assistant text omitted to fit the context budget; original retained in history.]"
				}
			}
		}
	}
}

func (s *contextSnapshot) workingLimit(model agent.ModelContext) int {
	ceiling := s.settings.WorkingBudgetTokens
	if ceiling == 0 {
		ceiling = 64000
	}
	if override := s.settings.ModelBudgetTokens[model.Ref]; override > 0 {
		ceiling = override
	}
	fraction := s.settings.ContextWindowFraction
	if fraction == 0 {
		fraction = 0.25
	}
	if model.MaxContextTokens <= 0 {
		return min(ceiling, 16000)
	}
	replyReserve := min(8192, model.MaxContextTokens/4)
	if model.MaxOutputTokens > 0 {
		replyReserve = min(replyReserve, model.MaxOutputTokens)
	}
	safetyReserve := max(1, model.MaxContextTokens/20)
	return max(
		1,
		min(
			ceiling,
			int(float64(model.MaxContextTokens)*fraction),
			model.MaxContextTokens-replyReserve-safetyReserve,
		),
	)
}

func renderGroups(groups []contextGroup, toolCap, reasoningCap int) []conversation.Message {
	var result []conversation.Message
	for _, group := range selectedGroups(groups) {
		label := fmt.Sprintf(
			"[turn %d, %s, %s]",
			group.id,
			groupLabel(group),
			group.createdAt.Format("2006-01-02 15:04:05 Z07:00"),
		)
		if group.score != nil {
			label += fmt.Sprintf(" [cosine %.4f]", *group.score)
		} else if strings.HasPrefix(group.kind, "retrieved") {
			label += " [keyword match; semantic confidence unavailable]"
		}
		if group.kind == "recent" {
			messages := capParts(group.messages, toolCap, reasoningCap)
			if len(messages) > 0 {
				prependLabel(&messages[0], label)
				result = append(result, messages...)
			}
			continue
		}
		body := group.excerpt
		if group.kind == "gap" && group.digest != "" {
			body = group.digest
		}
		bound := 4096
		if group.kind == "gap" {
			bound = 1024
		}
		result = append(
			result,
			conversation.UserMessage(
				label+"\n<historical_context>\n"+boundedText(body, bound)+"\n</historical_context>",
			),
		)
	}
	return result
}

func groupPriority(kind string) int {
	if kind == "recent" {
		return 3
	}
	if kind == "gap" {
		return 2
	}
	return 1
}

func prependLabel(message *conversation.Message, label string) {
	for n := range message.Parts {
		if message.Parts[n].Type == conversation.TextPartType {
			message.Parts[n].Text = label + "\n" + message.Parts[n].Text
			return
		}
	}
	message.Parts = append([]conversation.Part{conversation.Text(label+"\n", "")}, message.Parts...)
}

func groupLabel(group contextGroup) string {
	switch group.kind {
	case "recent":
		return "verbatim; shortened parts are marked"
	case "gap":
		if group.digest != "" {
			return "gap digest"
		}
		return "gap source excerpt; summary pending"
	case "retrieved_digest":
		return "retrieved digest"
	default:
		return "retrieved source excerpt"
	}
}

func capParts(input []conversation.Message, toolCap, reasoningCap int) []conversation.Message {
	messages := conversation.CloneMessages(input)
	for n := range messages {
		for p := range messages[n].Parts {
			part := &messages[n].Parts[p]
			if part.Type == conversation.ToolResultPartType {
				part.Content = boundedText(part.Content, toolCap)
			}
			if part.Type == conversation.ReasoningPartType {
				part.Text = boundedText(part.Text, reasoningCap)
			}
		}
	}
	return messages
}

func contextLabels(groups []contextGroup) []string {
	var labels []string
	for _, group := range selectedGroups(groups) {
		label := fmt.Sprintf("%d:%s:%s", group.id, group.kind, group.createdAt.Format(time.RFC3339))
		if group.score != nil {
			label += fmt.Sprintf(":cosine=%.4f", *group.score)
		}
		labels = append(labels, label)
	}
	return labels
}

func selectedGroups(groups []contextGroup) []contextGroup {
	preferred := make(map[int64]int)
	for _, group := range groups {
		if !group.dropped {
			preferred[group.id] = max(preferred[group.id], groupPriority(group.kind))
		}
	}
	seen := make(map[int64]bool)
	var result []contextGroup
	for _, group := range groups {
		if !group.dropped && !seen[group.id] && groupPriority(group.kind) == preferred[group.id] {
			result = append(result, group)
			seen[group.id] = true
		}
	}
	return result
}

func cloneBudgets(input map[string]int) map[string]int {
	cloned := make(map[string]int, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}
