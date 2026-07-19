package turnctx

import (
	"context"
	"testing"
)

func TestOriginRoundTrip(t *testing.T) {
	t.Parallel()

	want := Origin{
		Channel:   "telegram",
		ChatID:    "chat-123",
		UserID:    "user-456",
		MessageID: "message-789",
	}
	ctx := WithOrigin(context.Background(), want)

	got, ok := OriginFrom(ctx)
	if !ok {
		t.Fatal("OriginFrom() ok = false, want true")
	}
	if got != want {
		t.Fatalf("OriginFrom() = %#v, want %#v", got, want)
	}
}

func TestOriginFromMissingContext(t *testing.T) {
	t.Parallel()

	if got, ok := OriginFrom(context.Background()); ok || got != (Origin{}) {
		t.Fatalf("OriginFrom(background) = %#v, %t, want zero, false", got, ok)
	}
}

func TestWithOriginReplacesParentOrigin(t *testing.T) {
	t.Parallel()

	parent := WithOrigin(context.Background(), Origin{Channel: "parent"})
	want := Origin{Channel: "child", ChatID: "chat"}
	child := WithOrigin(parent, want)

	got, ok := OriginFrom(child)
	if !ok || got != want {
		t.Fatalf("OriginFrom(child) = %#v, %t, want %#v, true", got, ok, want)
	}
}
