package history

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/q15co/q15/systems/agent/internal/history/historydb"
)

const evidencePageSize = 16

type evidenceCursor struct {
	TurnID int64  `json:"turn_id"`
	View   string `json:"view"`
	Offset int    `json:"evidence_offset"`
}

type toolEvidence struct {
	Tool    string       `json:"tool"`
	Preview string       `json:"result_preview"`
	Read    sourceCursor `json:"read"`
}

type recallOverview struct {
	TurnID         int64           `json:"turn_id"`
	View           string          `json:"view"`
	Conversation   []sourcePage    `json:"conversation,omitempty"`
	Evidence       []toolEvidence  `json:"evidence"`
	EvidenceOffset int             `json:"evidence_offset"`
	NextEvidence   *evidenceCursor `json:"next_evidence"`
	Source         map[string]any  `json:"all_source"`
}

func (t recallTool) readOverview(ctx context.Context, args readArguments) (string, error) {
	q := historydb.New(t.indexer.store.pool)
	ids, err := q.ReadConversationPartIDs(
		ctx,
		historydb.ReadConversationPartIDsParams{
			TurnID:  args.TurnID,
			AgentID: t.indexer.store.agentID,
		},
	)
	if err != nil {
		return "", err
	}
	if len(ids) == 0 {
		// Completed turns can contain media or tool evidence without text.
		if _, err := t.readSourcePage(ctx, readArguments{TurnID: args.TurnID, sourceCursor: sourceCursor{Field: "body"}, Limit: 1}); err != nil {
			return "", err
		}
	}
	overview := recallOverview{
		TurnID:         args.TurnID,
		View:           "conversation",
		Evidence:       []toolEvidence{},
		EvidenceOffset: args.EvidenceOffset,
		Source:         map[string]any{"turn_id": args.TurnID, "view": "source"},
	}
	if args.View == "evidence" {
		overview.View = "evidence"
	} else {
		for _, id := range ids {
			page, err := t.readSourcePage(ctx, readArguments{TurnID: args.TurnID, sourceCursor: sourceCursor{PartID: id, Field: "body"}, Limit: args.Limit})
			if err != nil {
				return "", err
			}
			// Conversation pages continue within their own part; tool evidence has its own list.
			if page.Next != nil && (page.Next.PartID != page.PartID || page.Next.Field != "body") {
				page.Next = nil
			}
			overview.Conversation = append(overview.Conversation, page)
		}
	}
	rows, err := q.ReadToolEvidence(
		ctx,
		historydb.ReadToolEvidenceParams{
			TurnID:     args.TurnID,
			AgentID:    t.indexer.store.agentID,
			PageOffset: int32(args.EvidenceOffset),
			PageLimit:  evidencePageSize + 1,
		},
	)
	if err != nil {
		return "", err
	}
	for _, row := range rows[:min(len(rows), evidencePageSize)] {
		overview.Evidence = append(
			overview.Evidence,
			toolEvidence{Tool: row.ToolName, Preview: row.Preview,
				Read: sourceCursor{TurnID: args.TurnID, PartID: row.PartID, Field: "body"}},
		)
	}
	if len(rows) > evidencePageSize {
		overview.setEvidenceContinuation()
	}
	return encodeOverview(overview, recallResultBytes)
}

func (overview *recallOverview) setEvidenceContinuation() {
	overview.NextEvidence = &evidenceCursor{
		TurnID: overview.TurnID,
		View:   "evidence",
		Offset: overview.EvidenceOffset + len(overview.Evidence),
	}
}

func encodeOverview(overview recallOverview, limit int) (string, error) {
	overview.Conversation = slices.Clone(overview.Conversation)
	overview.Evidence = slices.Clone(overview.Evidence)
	for {
		raw, err := json.Marshal(overview)
		if err != nil || len(raw) <= limit {
			return string(raw), err
		}
		if len(overview.Evidence) > 0 {
			overview.Evidence = overview.Evidence[:len(overview.Evidence)-1]
			overview.setEvidenceContinuation()
			continue
		}
		largest := -1
		for n := range overview.Conversation {
			if overview.Conversation[n].Source != "" &&
				(largest < 0 || len(overview.Conversation[n].Source) > len(overview.Conversation[largest].Source)) {
				largest = n
			}
		}
		if largest < 0 || !overview.Conversation[largest].shorten() {
			return omittedRecall(overview.TurnID), nil
		}
	}
}

func encodeSourcePage(page sourcePage, limit int) (string, error) {
	for {
		raw, err := json.Marshal(page)
		if err != nil || len(raw) <= limit {
			return string(raw), err
		}
		if !page.shorten() {
			return omittedRecall(page.TurnID), nil
		}
	}
}

func encodeSearchResult(result SearchResult, limit int) (string, error) {
	result.Hits = slices.Clone(result.Hits)
	for {
		raw, err := json.Marshal(result)
		if err != nil || len(raw) <= limit {
			return string(raw), err
		}
		result.Truncated = true
		if len(result.Hits) > 1 {
			result.Hits = result.Hits[:len(result.Hits)-1]
			continue
		}
		if len(result.Hits) == 0 || len(result.Hits[0].Text) == 0 {
			return `{"hits":[],"truncated":true}`, nil
		}
		body := []rune(result.Hits[0].Text)
		result.Hits[0].Text = string(body[:len(body)/2])
	}
}

func omittedRecall(turnID int64) string {
	return fmt.Sprintf(
		`{"turn_id":%d,"omitted":"History evidence exceeded the context budget; request a smaller page with limit=128."}`,
		turnID,
	)
}

func boundedRecallResult(content, tool string, limit int) string {
	if len(content) <= limit {
		return content
	}
	if tool == "history_search" {
		var result SearchResult
		if json.Unmarshal([]byte(content), &result) == nil {
			if raw, err := encodeSearchResult(result, limit); err == nil {
				return raw
			}
		}
	} else {
		var header struct {
			View string `json:"view"`
		}
		if json.Unmarshal([]byte(content), &header) == nil {
			if header.View != "" {
				var overview recallOverview
				if json.Unmarshal([]byte(content), &overview) == nil {
					if raw, err := encodeOverview(overview, limit); err == nil {
						return raw
					}
				}
			} else {
				var page sourcePage
				if json.Unmarshal([]byte(content), &page) == nil && page.PartID > 0 && page.Encoding != "" {
					if raw, err := encodeSourcePage(page, limit); err == nil {
						return raw
					}
				}
			}
		}
	}
	return boundedText(content, limit)
}
