package memory

import (
	"time"

	"github.com/q15co/q15/systems/agent/internal/agent"
)

type turnRecord struct {
	ID        string          `json:"id"`
	Seq       int64           `json:"seq"`
	CreatedAt time.Time       `json:"created_at"`
	Messages  []agent.Message `json:"messages"`
}

type headState struct {
	LastSeq   int64     `json:"last_seq"`
	UpdatedAt time.Time `json:"updated_at"`
}
