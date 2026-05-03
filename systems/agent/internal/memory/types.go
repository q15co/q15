package memory

import (
	"time"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

type turnRecord struct {
	SchemaVersion int       `json:"schema_version"`
	ID            string    `json:"id"`
	Seq           int64     `json:"seq"`
	CreatedAt     time.Time `json:"created_at"`
	// Messages always uses the current canonical conversation.Message schema.
	// Legacy transcript compatibility is intentionally handled only by startup
	// migration before runtime replay begins.
	Messages []conversation.Message `json:"messages"`
}

type headState struct {
	LastSeq   int64     `json:"last_seq"`
	UpdatedAt time.Time `json:"updated_at"`
}

type consolidationCheckpointState struct {
	LastConsolidatedTurnID string    `json:"last_consolidated_turn_id,omitempty"`
	LastConsolidatedSeq    int64     `json:"last_consolidated_seq,omitempty"`
	LastConsolidatedAt     time.Time `json:"last_consolidated_at,omitempty"`
	UpdatedAt              time.Time `json:"updated_at,omitempty"`
}

type semanticExtractionCheckpointState struct {
	LastExtractedTurnID string    `json:"last_extracted_turn_id,omitempty"`
	LastExtractedSeq    int64     `json:"last_extracted_seq,omitempty"`
	LastExtractedAt     time.Time `json:"last_extracted_at,omitempty"`
	UpdatedAt           time.Time `json:"updated_at,omitempty"`
}

type turnPathEntry struct {
	Path string
	Seq  int64
}
