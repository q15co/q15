package memory

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
	"github.com/q15co/q15/systems/agent/internal/conversation"
)

// legacyTurnRecord is a migration-only v1 transcript shape.
type legacyTurnRecord struct {
	ID        string          `json:"id"`
	Seq       int64           `json:"seq"`
	CreatedAt time.Time       `json:"created_at"`
	Messages  []legacyMessage `json:"messages"`
}

// legacyMessage is a migration-only v1 message shape.
type legacyMessage struct {
	Role        string           `json:"role"`
	Content     string           `json:"content"`
	Phase       string           `json:"phase,omitempty"`
	ToolCalls   []agent.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID  string           `json:"tool_call_id,omitempty"`
	ProviderRaw json.RawMessage  `json:"provider_raw,omitempty"`
}

func (m *legacyMessage) UnmarshalJSON(data []byte) error {
	type wireMessage struct {
		Role        string           `json:"role"`
		Content     string           `json:"content"`
		Phase       string           `json:"phase,omitempty"`
		ToolCalls   []agent.ToolCall `json:"tool_calls,omitempty"`
		ToolCallID  string           `json:"tool_call_id,omitempty"`
		ProviderRaw json.RawMessage  `json:"provider_raw,omitempty"`

		LegacyRole        string           `json:"Role"`
		LegacyContent     string           `json:"Content"`
		LegacyPhase       string           `json:"Phase"`
		LegacyToolCalls   []agent.ToolCall `json:"ToolCalls"`
		LegacyToolCallID  string           `json:"ToolCallID"`
		LegacyProviderRaw json.RawMessage  `json:"ProviderRaw"`
	}

	var wire wireMessage
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}

	m.Role = firstNonEmptyString(wire.Role, wire.LegacyRole)
	m.Content = firstNonEmptyString(wire.Content, wire.LegacyContent)
	m.Phase = firstNonEmptyString(wire.Phase, wire.LegacyPhase)
	m.ToolCalls = cloneLegacyToolCalls(wire.ToolCalls)
	if len(m.ToolCalls) == 0 {
		m.ToolCalls = cloneLegacyToolCalls(wire.LegacyToolCalls)
	}
	m.ToolCallID = firstNonEmptyString(wire.ToolCallID, wire.LegacyToolCallID)
	m.ProviderRaw = cloneRawMessage(wire.ProviderRaw)
	if len(m.ProviderRaw) == 0 {
		m.ProviderRaw = cloneRawMessage(wire.LegacyProviderRaw)
	}

	return nil
}

type historyUpgradeResult struct {
	Upgraded    int
	Quarantined int
}

// upgradeHistory is the only place where legacy transcript schemas are
// supported. Runtime reads and new writes stay on the current canonical schema
// only.
func (s *Store) upgradeHistory() (historyUpgradeResult, error) {
	paths, err := s.listTurnPaths()
	if err != nil {
		return historyUpgradeResult{}, err
	}

	var result historyUpgradeResult
	for _, path := range paths {
		upgraded, quarantined, err := s.upgradeTurn(path)
		if err != nil {
			return result, err
		}
		if upgraded {
			result.Upgraded++
		}
		if quarantined {
			result.Quarantined++
		}
	}
	return result, nil
}

func (s *Store) upgradeTurn(path string) (bool, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, false, fmt.Errorf("read turn record %q: %w", path, err)
	}

	var envelope struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return false, true, s.quarantineTurn(path, fmt.Errorf("decode turn record: %w", err))
	}

	switch envelope.SchemaVersion {
	case 0:
		var legacy legacyTurnRecord
		if err := json.Unmarshal(data, &legacy); err != nil {
			return false, true, s.quarantineTurn(path, fmt.Errorf("decode v1 turn record: %w", err))
		}
		record := turnRecord{
			SchemaVersion: conversation.SchemaVersion,
			ID:            legacy.ID,
			Seq:           legacy.Seq,
			CreatedAt:     legacy.CreatedAt,
			Messages:      sanitizeStoredMessages(migrateLegacyMessages(legacy.Messages)),
		}
		if err := writeJSONFileAtomic(path, record); err != nil {
			return false, false, fmt.Errorf("rewrite turn record %q: %w", path, err)
		}
		return true, false, nil
	case conversation.SchemaVersion:
		var record turnRecord
		if err := json.Unmarshal(data, &record); err != nil {
			return false, true, s.quarantineTurn(path, fmt.Errorf("decode v2 turn record: %w", err))
		}
		sanitized := sanitizeStoredMessages(record.Messages)
		if reflect.DeepEqual(record.Messages, sanitized) {
			return false, false, nil
		}
		record.Messages = sanitized
		if err := writeJSONFileAtomic(path, record); err != nil {
			return false, false, fmt.Errorf("rewrite sanitized turn record %q: %w", path, err)
		}
		return true, false, nil
	default:
		return false, true, s.quarantineTurn(
			path,
			fmt.Errorf("unsupported schema_version %d", envelope.SchemaVersion),
		)
	}
}

func migrateLegacyMessages(messages []legacyMessage) []conversation.Message {
	if len(messages) == 0 {
		return nil
	}

	out := make([]conversation.Message, 0, len(messages))
	for _, msg := range messages {
		role := conversation.Role(strings.TrimSpace(msg.Role))
		converted := conversation.Message{Role: role}
		switch role {
		case conversation.SystemRole, conversation.UserRole:
			if strings.TrimSpace(msg.Content) != "" {
				converted.Parts = append(converted.Parts, conversation.Text(msg.Content, ""))
			}
		case conversation.AssistantRole:
			if reasoning := legacyAssistantReasoning(msg.ProviderRaw); reasoning.Text != "" ||
				len(reasoning.Replay) > 0 {
				converted.Parts = append(converted.Parts, reasoning)
			}

			assistantContent := strings.TrimSpace(msg.Content)
			if assistantContent == "" {
				assistantContent = legacyAssistantContent(msg.ProviderRaw)
			}
			disposition := migrateLegacyDisposition(msg.Phase)
			if disposition == "" {
				disposition = migrateLegacyDisposition(legacyAssistantPhase(msg.ProviderRaw))
			}
			if assistantContent != "" {
				converted.Parts = append(
					converted.Parts,
					conversation.Text(assistantContent, disposition),
				)
			}
			toolCalls := cloneLegacyToolCalls(msg.ToolCalls)
			if len(toolCalls) == 0 {
				toolCalls = legacyAssistantToolCalls(msg.ProviderRaw)
			}
			for _, call := range toolCalls {
				converted.Parts = append(
					converted.Parts,
					conversation.ToolCall(call.ID, call.Name, call.Arguments),
				)
			}
		case conversation.ToolRole:
			if strings.TrimSpace(msg.ToolCallID) != "" {
				converted.Parts = append(
					converted.Parts,
					conversation.ToolResult(msg.ToolCallID, msg.Content, false),
				)
			}
		default:
			if strings.TrimSpace(msg.Content) != "" {
				converted.Parts = append(converted.Parts, conversation.Text(msg.Content, ""))
			}
		}
		converted = conversation.NormalizeMessage(converted)
		out = append(out, converted)
	}
	return out
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func cloneLegacyToolCalls(in []agent.ToolCall) []agent.ToolCall {
	if len(in) == 0 {
		return nil
	}

	out := make([]agent.ToolCall, len(in))
	copy(out, in)
	return out
}

func cloneRawMessage(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), raw...)
}

func legacyAssistantReasoning(raw json.RawMessage) conversation.Part {
	if len(raw) == 0 {
		return conversation.Part{}
	}

	var probe struct {
		ReasoningContent string `json:"reasoning_content"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return conversation.Part{}
	}

	text := strings.TrimSpace(probe.ReasoningContent)
	if text == "" {
		return conversation.Part{}
	}

	return conversation.Reasoning(text, nil)
}

func legacyAssistantContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var probe struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return ""
	}
	return strings.TrimSpace(probe.Content)
}

func legacyAssistantPhase(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var probe struct {
		Phase string `json:"phase"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return ""
	}
	return strings.TrimSpace(probe.Phase)
}

func legacyAssistantToolCalls(raw json.RawMessage) []agent.ToolCall {
	if len(raw) == 0 {
		return nil
	}

	var probe struct {
		ToolCalls []struct {
			ID       string `json:"id"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil
	}

	out := make([]agent.ToolCall, 0, len(probe.ToolCalls))
	for _, call := range probe.ToolCalls {
		name := strings.TrimSpace(call.Function.Name)
		if name == "" {
			continue
		}
		arguments := strings.TrimSpace(call.Function.Arguments)
		if arguments == "" {
			arguments = "{}"
		}
		out = append(out, agent.ToolCall{
			ID:        strings.TrimSpace(call.ID),
			Name:      name,
			Arguments: arguments,
		})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func migrateLegacyDisposition(phase string) conversation.TextDisposition {
	switch strings.TrimSpace(phase) {
	case "commentary":
		return conversation.TextDispositionCommentary
	case "final", "final_answer":
		return conversation.TextDispositionFinal
	default:
		return ""
	}
}

func (s *Store) quarantineTurn(path string, reason error) error {
	base := filepath.Join(s.rootDir, "history", "turns")
	relative, err := filepath.Rel(base, path)
	if err != nil {
		return fmt.Errorf("resolve turn quarantine path for %q: %w", path, err)
	}

	target := filepath.Join(s.rootDir, "history", "quarantine", relative)
	if _, err := os.Stat(target); err == nil {
		target = filepath.Join(
			s.rootDir,
			"history",
			"quarantine",
			strings.TrimSuffix(
				relative,
				filepath.Ext(relative),
			)+"-"+time.Now().
				UTC().
				Format("20060102T150405Z")+
				filepath.Ext(
					relative,
				),
		)
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("create quarantine dir for %q: %w", target, err)
	}
	if err := os.Rename(path, target); err != nil {
		return fmt.Errorf("quarantine turn record %q: %w", path, err)
	}
	log.Printf("q15: quarantined unreadable transcript %q -> %q: %v", path, target, reason)
	return nil
}
