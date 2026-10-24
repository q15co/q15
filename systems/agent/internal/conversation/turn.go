package conversation

import "time"

// Turn is one completed conversation turn.
type Turn struct {
	Seq       int64
	CreatedAt time.Time
	Messages  []Message
}

// TurnPage pages completed turns newest first.
type TurnPage struct {
	Turns   []Turn
	HeadSeq int64
	HasMore bool
}
