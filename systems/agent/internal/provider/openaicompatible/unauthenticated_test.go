package openaicompatible

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/conversation"
)

func TestOpenAIClientOptionalAuthentication(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "unrelated-key")
	for _, key := range []string{"", "configured-key"} {
		t.Run(key, func(t *testing.T) {
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					wantAuth := ""
					if key != "" {
						wantAuth = "Bearer " + key
					}
					if got := r.Header.Get("Authorization"); got != wantAuth {
						t.Errorf("Authorization = %q, want %q", got, wantAuth)
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write(
						[]byte(
							`{"id":"test","object":"chat.completion","created":1,"model":"test","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`,
						),
					)
				}),
			)
			defer server.Close()
			client, err := NewClient(server.URL, key, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Complete(
				context.Background(),
				"test",
				[]conversation.Message{conversation.UserMessage("hi")},
				nil,
			)
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}
