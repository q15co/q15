package protocol

import "slices"

const (
	// MaxMessageBytes bounds decoded UTF-8 text, before JSON escaping.
	MaxMessageBytes = 64 * 1024

	// ChunkBytes keeps streaming encryption independent of payload size.
	ChunkBytes = 32 * 1024

	// MaxEnvelopeBytes bounds plaintext collectors and provisional streaming output.
	MaxEnvelopeBytes = 16 * 1024 * 1024
	// MaxContentTypeBytes bounds the encrypted type header.
	MaxContentTypeBytes = 1024
	// MaxMediaBytes bounds file bytes independently of descriptor overhead.
	MaxMediaBytes = 8 * 1024 * 1024
	// MaxMediaHeaderBytes bounds the encrypted batch descriptors.
	MaxMediaHeaderBytes = 16 * 1024
	// MaxMediaFiles bounds per-batch storage and rendering work.
	MaxMediaFiles = 16
	// MaxMediaPlainBytes includes the length prefix and descriptor header.
	MaxMediaPlainBytes = MaxMediaBytes + MaxMediaHeaderBytes + 4
	// MaxMediaChunks includes the encrypted type header and final chunk.
	MaxMediaChunks = (MaxMediaPlainBytes+MaxContentTypeBytes+2)/ChunkBytes + 1
	// MaxMediaWireBytes covers tags, base64 and JSON envelope framing.
	MaxMediaWireBytes = (MaxMediaPlainBytes+MaxContentTypeBytes+2+MaxMediaChunks*16)*4/3 + MaxMediaChunks*64 + 4096
	// MaxEnvelopeChunks includes the type header and a possibly empty final chunk.
	MaxEnvelopeChunks = (MaxEnvelopeBytes+MaxContentTypeBytes+2)/ChunkBytes + 1
	// MaxChunkDataChars includes the GCM tag and base64url rounding.
	MaxChunkDataChars = ((ChunkBytes+16)*4 + 2) / 3
	// MaxClientFrameBytes covers six-byte JSON escapes, base64 and frame overhead.
	MaxClientFrameBytes = MaxMessageBytes*8 + 16*1024
	// MaxEnvelopeCipherBytes includes the encrypted header and per-chunk GCM tags.
	MaxEnvelopeCipherBytes = MaxEnvelopeBytes + MaxContentTypeBytes + 2 + MaxEnvelopeChunks*16
	// MaxServerFrameBytes covers tags, base64 and JSON framing of a full envelope.
	MaxServerFrameBytes = MaxEnvelopeCipherBytes*4/3 + MaxEnvelopeChunks*64 + 4096
	// MaxContentStreams bounds replay state for the lifetime of a content key.
	MaxContentStreams = 65536
	// RekeyAfter leaves room for in-flight frames while pending sends drain.
	RekeyAfter = MaxContentStreams - 256
)

var plaintextFrameTypes = [...]string{
	Hello, Sync, Abort, Ack, Status, Presence, Ping, Ready, TurnStart, Pong, Error, Key,
}

// Content defaults to sealing: a new frame cannot expose a payload by omission.
func Content(kind string) bool {
	return !slices.Contains(plaintextFrameTypes[:], kind)
}
