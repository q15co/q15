package history

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
)

// ContextSettings separates tunable working limits from catalog context ceilings.
type ContextSettings struct {
	WorkingBudgetBytes int
	ModelBudgetBytes   map[string]int
	RecentTurns        int
	HorizonTurns       int
	RetrievalLimit     int
	MinSimilarity      float32
	Tools              []agent.ToolDefinition
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
	if settings.WorkingBudgetBytes == 0 {
		settings.WorkingBudgetBytes = 256 * 1024
	}
	if settings.RecentTurns == 0 {
		settings.RecentTurns = 8
	}
	if settings.HorizonTurns == 0 {
		settings.HorizonTurns = 40
	}
	if settings.RetrievalLimit == 0 {
		settings.RetrievalLimit = 6
	}
	if settings.WorkingBudgetBytes < 1024 || settings.RecentTurns < 1 ||
		settings.HorizonTurns < settings.RecentTurns ||
		settings.HorizonTurns > 500 ||
		settings.RetrievalLimit < 1 ||
		settings.RetrievalLimit > 32 {
		return nil, errors.New("invalid history context budget or tier limits")
	}
	settings.ModelBudgetBytes = cloneBudgets(settings.ModelBudgetBytes)
	settings.Tools = slices.Clone(settings.Tools)
	return &ContextService{indexer: indexer, settings: settings}, nil
}

type contextGroup struct {
	id            int64
	createdAt     time.Time
	kind          string
	messages      []conversation.Message
	digest        string
	excerpt       string
	matchedSource string
	score         *float32
	protected     bool
	dropped       bool
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
		s.settings.HorizonTurns,
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
	if errors.As(err, &budgetError) {
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
	toolCap, reasoningCap := 8192, 2048
	materialize := func() ([]conversation.Message, int, error) {
		result := append(
			conversation.CloneMessages(system),
			renderGroups(groups, toolCap, reasoningCap)...)
		result = append(result, capParts(active, toolCap, reasoningCap)...)
		size, err := agent.RequestEncodedBytes(result, tools)
		return result, size, err
	}
	result, size, err := materialize()
	if err != nil {
		return nil, err
	}
	for n := range groups {
		if size <= limit {
			break
		}
		if groups[n].kind != "recent" || groups[n].protected {
			continue
		}
		groups[n].kind = "gap"
		result, size, err = materialize()
		if err != nil {
			return nil, err
		}
	}
	for size > limit && toolCap > 512 {
		toolCap /= 2
		reasoningCap = max(256, reasoningCap/2)
		result, size, err = materialize()
		if err != nil {
			return nil, err
		}
	}
	for _, kind := range []string{"gap", "retrieved"} {
		for n := range groups {
			if size <= limit {
				break
			}
			if !strings.HasPrefix(groups[n].kind, kind) {
				continue
			}
			groups[n].dropped = true
			result, size, err = materialize()
			if err != nil {
				return nil, err
			}
		}
	}
	if size > limit {
		return nil, &agent.RequestBudgetError{Bytes: size, Limit: limit}
	}
	if emit {
		log.Printf(
			"q15: history context ref=%q canonical_bytes=%d limit_bytes=%d lexical_fallback=%t selected=%v",
			model.Ref,
			size,
			limit,
			s.lexicalFallback,
			contextLabels(groups),
		)
	}
	return result, nil
}

func (s *contextSnapshot) workingLimit(model agent.ModelContext) int {
	limit := s.settings.WorkingBudgetBytes
	if override := s.settings.ModelBudgetBytes[model.Ref]; override > 0 {
		limit = override
	}
	// Match the catalog planner's conservative text allowance. This is a hard
	// safety reserve, not an asserted quality threshold or a tokenizer count.
	if model.MaxContextTokens > 0 {
		responseReserve := min(
			model.MaxContextTokens/4,
			max(4096, min(model.MaxOutputTokens, 8192)),
		)
		framingReserve := model.MaxContextTokens / 8
		limit = min(limit, model.MaxContextTokens-responseReserve-framingReserve)
	}
	return limit
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
		if group.matchedSource != "" {
			body = "Generated digest:\n" + body + "\nMatched original-message excerpt:\n" + boundedText(
				group.matchedSource,
				2048,
			)
			bound = 4096
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
		if group.matchedSource != "" {
			return "gap digest with retrieved source excerpt"
		}
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
			if group.kind == "gap" {
				for _, candidate := range groups {
					if candidate.id == group.id && !candidate.dropped &&
						candidate.kind == "retrieved_chunk" {
						if group.digest != "" {
							group.matchedSource = candidate.excerpt
						} else {
							group.excerpt = candidate.excerpt
						}
						group.score = candidate.score
						break
					}
				}
			}
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
