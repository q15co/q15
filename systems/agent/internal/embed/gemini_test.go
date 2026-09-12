package embed

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestGeminiEmbedderBatchSize(t *testing.T) {
	if _, err := NewGeminiEmbedder(
		context.Background(),
		"key",
		"model",
		3,
		-1,
	); err == nil {
		t.Fatal("expected error for negative batch size")
	}
	embedder, err := NewGeminiEmbedder(context.Background(), "key", "model", 3, 0)
	if err != nil {
		t.Fatalf("NewGeminiEmbedder: %v", err)
	}
	if embedder.batchSize != geminiEmbedBatchSize {
		t.Fatalf("default batch size = %d, want %d", embedder.batchSize, geminiEmbedBatchSize)
	}
}

func TestGeminiEmbedRetryDetection(t *testing.T) {
	retriable := []error{
		errors.New("429 Too Many Requests"),
		errors.New("RESOURCE_EXHAUSTED: quota exceeded"),
		errors.New("rate limit exceeded"),
	}
	for _, err := range retriable {
		if !isRetriableGeminiEmbedError(err) {
			t.Fatalf("isRetriableGeminiEmbedError(%q) = false, want true", err)
		}
	}
	if isRetriableGeminiEmbedError(errors.New("400 invalid request")) {
		t.Fatal("non-rate-limit Gemini error was marked retriable")
	}
}

func TestGeminiEmbedRetryDelayCaps(t *testing.T) {
	if got, want := geminiEmbedRetryDelay(0), 5*time.Second; got != want {
		t.Fatalf("first delay = %s, want %s", got, want)
	}
	if got, want := geminiEmbedRetryDelay(99), 60*time.Second; got != want {
		t.Fatalf("capped delay = %s, want %s", got, want)
	}
}
