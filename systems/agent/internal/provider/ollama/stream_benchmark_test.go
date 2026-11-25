package ollama

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	ollamaapi "github.com/ollama/ollama/api"
)

var benchmarkBearerContent string

func BenchmarkBearerChatStreamDecode(b *testing.B) {
	payloads := map[string][]byte{
		"many_small_frames": benchmarkBearerPayload(1024, 128),
		"large_frames":      benchmarkBearerPayload(64, 64*1024),
	}
	for name, payload := range payloads {
		b.Run(name, func(b *testing.B) {
			b.SetBytes(int64(len(payload)))
			b.ReportAllocs()
			for b.Loop() {
				if err := decodeBearerChatStream(bytes.NewReader(payload), func(response ollamaapi.ChatResponse) error {
					benchmarkBearerContent = response.Message.Content
					return nil
				}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func benchmarkBearerPayload(frames, contentBytes int) []byte {
	var payload bytes.Buffer
	encoder := json.NewEncoder(&payload)
	for range frames {
		if err := encoder.Encode(ollamaapi.ChatResponse{
			Model: "benchmark-model",
			Message: ollamaapi.Message{
				Role:    "assistant",
				Content: strings.Repeat("x", contentBytes),
			},
		}); err != nil {
			panic(err)
		}
	}
	return payload.Bytes()
}
