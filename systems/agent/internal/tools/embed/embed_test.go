package embedtools

import (
	"context"
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/embed"
)

func TestSourcesAddDefaultsEnabledAndReturnsStableJSON(t *testing.T) {
	service := &fakeService{}
	tool := NewSources(service)

	got, err := tool.Run(context.Background(), `{
		"action": "add",
		"id": "library/book",
		"collection": "library",
		"source_type": "chunked_markdown_tree",
		"path": "/workspace/library/author/book/chunks"
	}`)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !service.added.Enabled {
		t.Fatal("added source Enabled = false, want true")
	}
	for _, want := range []string{
		`"id": "library/book"`,
		`"collection": "library"`,
		`"source_type": "chunked_markdown_tree"`,
		`"enabled": true`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

func TestSourcesAddRequiresTypedSourceFields(t *testing.T) {
	tool := NewSources(&fakeService{})
	_, err := tool.Run(context.Background(), `{"action":"add","collection":"library"}`)
	if err == nil ||
		!strings.Contains(err.Error(), "missing required argument for add: source_type") {
		t.Fatalf("Run() error = %v, want missing source_type", err)
	}
}

func TestSearchReturnsServiceResultsAsJSON(t *testing.T) {
	tool := NewSearch(&fakeService{
		results: []embed.SearchResult{
			{
				Collection: "semantic",
				ID:         "point-1",
				Score:      0.9,
				Payload:    map[string]any{"source_id": "docs"},
			},
		},
	})
	got, err := tool.Run(
		context.Background(),
		`{"query":"hello","collection":"semantic","limit":1}`,
	)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, want := range []string{
		`"collection": "semantic"`,
		`"id": "point-1"`,
		`"source_id": "docs"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
}

type fakeService struct {
	added   embed.Source
	results []embed.SearchResult
}

func (f *fakeService) ListSources(ctx context.Context) ([]embed.Source, error) {
	_ = ctx
	return nil, nil
}

func (f *fakeService) AddSource(
	ctx context.Context,
	source embed.Source,
) (embed.Source, error) {
	_ = ctx
	f.added = source
	return source, nil
}

func (f *fakeService) RemoveSource(ctx context.Context, id string) (embed.Source, error) {
	_, _ = ctx, id
	return embed.Source{}, nil
}

func (f *fakeService) SetSourceEnabled(
	ctx context.Context,
	id string,
	enabled bool,
) (embed.Source, error) {
	_, _ = ctx, id
	return embed.Source{Enabled: enabled}, nil
}

func (f *fakeService) Sync(
	ctx context.Context,
	opts embed.SyncOptions,
) (embed.SyncResult, error) {
	_, _ = ctx, opts
	return embed.SyncResult{}, nil
}

func (f *fakeService) Search(
	ctx context.Context,
	opts embed.SearchOptions,
) ([]embed.SearchResult, error) {
	_, _ = ctx, opts
	return f.results, nil
}

func (f *fakeService) Status(ctx context.Context, collection string) (embed.Status, error) {
	_, _ = ctx, collection
	return embed.Status{}, nil
}
