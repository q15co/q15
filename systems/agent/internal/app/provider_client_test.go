package app

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/agent"
	q15media "github.com/q15co/q15/systems/agent/internal/media"
	"github.com/q15co/q15/systems/agent/internal/modelcatalog"
	"github.com/q15co/q15/systems/agent/internal/provider/ollama"
)

func TestOllamaProviderClientIsSharedAcrossPorts(t *testing.T) {
	registry := testRegistry(
		t,
		map[string][]modelcatalog.Model{
			"api": {
				{
					ProviderModel: "both",
					Capabilities:  modelcatalog.Capabilities{Chat: true, Decision: true},
				},
			},
		},
	)
	var builds atomic.Int32
	adapter, err := newModelAdapterWithFactory(
		registry,
		nil,
		func(_ modelcatalog.Model, store q15media.Store) (agent.ModelClient, error) {
			builds.Add(1)
			return ollama.NewClient("http://unused.invalid", "", store, nil, "")
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	model := registry.Snapshot()[0]
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := adapter.decisionClientFor(model); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	chat, err := adapter.clientFor(model)
	if err != nil {
		t.Fatal(err)
	}
	typed, err := adapter.decisionClientFor(model)
	if err != nil {
		t.Fatal(err)
	}
	if any(chat) != any(typed) || builds.Load() != 1 {
		t.Fatalf(
			"provider constructed %d times, chat and decision equal = %v",
			builds.Load(),
			any(chat) == any(typed),
		)
	}
}
