package memory

import (
	"time"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

type turnRecord struct {
	SchemaVersion int                    `json:"schema_version"`
	ID            string                 `json:"id"`
	Seq           int64                  `json:"seq"`
	CreatedAt     time.Time              `json:"created_at"`
	Messages      []conversation.Message `json:"messages"`
}

type headState struct {
	LastSeq   int64     `json:"last_seq"`
	UpdatedAt time.Time `json:"updated_at"`
}
