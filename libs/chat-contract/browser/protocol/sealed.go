package protocol

// HelloPayload selects a replay cursor and offers an ephemeral ECDH key.
type HelloPayload struct {
	Cursor    int64  `json:"cursor,string"`
	Binding   string `json:"binding"`
	PublicKey string `json:"public_key"`
}

// KeyPayload contains only public material; channel_id expires with the socket.
type KeyPayload struct {
	ChannelID string `json:"channel_id"`
	PublicKey string `json:"public_key"`
	Binding   string `json:"binding"`
}

// Sealed frames bytes independently in bounded authenticated chunks. Metadata,
// including the content type, is inside the first encrypted chunk.
type Sealed struct {
	Version int     `json:"version"`
	Stream  string  `json:"stream"`
	Chunks  []Chunk `json:"chunks"`
}

// Chunk authenticates its index and terminal bit; gaps and truncation must be rejected.
type Chunk struct {
	Index uint32 `json:"index"`
	Final bool   `json:"final"`
	Data  string `json:"data"`
}
