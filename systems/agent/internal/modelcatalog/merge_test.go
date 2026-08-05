package modelcatalog

import (
	"reflect"
	"testing"
	"time"
)

func TestMerge_FillsZeroFields(t *testing.T) {
	base := []Model{
		{
			ProviderModel: "kimi-k2:cloud",
			Name:          "kimi-k2:cloud",
			Capabilities:  Capabilities{Text: true, ToolCalling: true},
			Source:        SourceOllama,
		},
	}
	enriched := []Model{
		{
			ProviderModel:    "kimi-k2",
			Name:             "Kimi K2",
			Capabilities:     Capabilities{ImageInput: true, Reasoning: true},
			CostTier:         "standard",
			CostPerMTokIn:    0.5,
			CostPerMTokOut:   1.5,
			MaxContextTokens: 256000,
			BenchmarkScores:  map[string]float64{"human_eval": 0.82},
		},
	}

	out := Merge(base, enriched)
	if len(out) != 1 {
		t.Fatalf("expected 1 model, got %d", len(out))
	}
	m := out[0]
	// Source preserved from roster.
	if m.Source != SourceOllama {
		t.Errorf("Source = %q, want %q", m.Source, SourceOllama)
	}
	// Name upgrades from the tagged ID to the human label.
	if m.Name != "Kimi K2" {
		t.Errorf("Name = %q, want %q", m.Name, "Kimi K2")
	}
	// Capabilities are OR'd: roster's Text+ToolCalling plus enriched ImageInput+Reasoning.
	wantCaps := Capabilities{Text: true, ToolCalling: true, ImageInput: true, Reasoning: true}
	if !reflect.DeepEqual(m.Capabilities, wantCaps) {
		t.Errorf("Capabilities = %+v, want %+v", m.Capabilities, wantCaps)
	}
	if m.CostTier != "standard" {
		t.Errorf("CostTier = %q", m.CostTier)
	}
	if m.MaxContextTokens != 256000 {
		t.Errorf("MaxContextTokens = %d", m.MaxContextTokens)
	}
	if m.BenchmarkScores["human_eval"] != 0.82 {
		t.Errorf("benchmark missing")
	}
}

func TestMerge_PreservesRosterWhenNoEnrichment(t *testing.T) {
	base := []Model{
		{
			ProviderModel: "lonely-model",
			Capabilities:  Capabilities{Text: true},
			Source:        SourceOllama,
		},
	}
	out := Merge(base, nil)
	if len(out) != 1 {
		t.Fatalf("expected 1 model, got %d", len(out))
	}
	if !reflect.DeepEqual(out[0], base[0]) {
		t.Errorf("model changed without enrichment: got %+v", out[0])
	}
}

func TestMerge_EmptyBase(t *testing.T) {
	out := Merge(nil, []Model{{ProviderModel: "x"}})
	if out != nil {
		t.Errorf("expected nil for empty base, got %d", len(out))
	}
}

func TestMerge_DoesNotOverwriteRosterCapabilities(t *testing.T) {
	base := []Model{{
		ProviderModel: "m",
		Capabilities:  Capabilities{Text: true, ToolCalling: true},
	}}
	enriched := []Model{{
		ProviderModel: "m",
		Capabilities:  Capabilities{ImageInput: true}, // no ToolCalling
	}}
	out := Merge(base, enriched)
	// Roster's ToolCalling must survive even though enriched doesn't list it.
	if !out[0].Capabilities.ToolCalling {
		t.Error("roster ToolCalling was dropped")
	}
	if !out[0].Capabilities.ImageInput {
		t.Error("enriched ImageInput was not added")
	}
}

func TestApplyFilters_IncludeWhitelist(t *testing.T) {
	models := []Model{
		{ProviderModel: "kimi-k2"},
		{ProviderModel: "llama3"},
		{ProviderModel: "minimax-m3"},
	}
	out := ApplyFilters(models, []string{"kimi-*", "minimax-*"}, nil)
	if len(out) != 2 {
		t.Fatalf("expected 2 models, got %d", len(out))
	}
	if out[0].ProviderModel != "kimi-k2" || out[1].ProviderModel != "minimax-m3" {
		t.Errorf("unexpected models: %+v", out)
	}
}

func TestApplyFilters_ExcludeBlacklist(t *testing.T) {
	models := []Model{
		{ProviderModel: "kimi-k2"},
		{ProviderModel: "text-embed"},
		{ProviderModel: "llama3"},
	}
	out := ApplyFilters(models, nil, []string{"*-embed"})
	if len(out) != 2 {
		t.Fatalf("expected 2 models, got %d", len(out))
	}
	for _, m := range out {
		if m.ProviderModel == "text-embed" {
			t.Error("excluded model was not filtered out")
		}
	}
}

func TestApplyFilters_IncludeAndExclude(t *testing.T) {
	models := []Model{
		{ProviderModel: "kimi-k2"},
		{ProviderModel: "kimi-embed"},
		{ProviderModel: "llama3"},
	}
	// Include all kimi-*, then exclude *-embed.
	out := ApplyFilters(models, []string{"kimi-*"}, []string{"*-embed"})
	if len(out) != 1 {
		t.Fatalf("expected 1 model, got %d", len(out))
	}
	if out[0].ProviderModel != "kimi-k2" {
		t.Errorf("expected kimi-k2, got %q", out[0].ProviderModel)
	}
}

func TestApplyFilters_NoFiltersReturnsCopy(t *testing.T) {
	models := []Model{{ProviderModel: "a"}, {ProviderModel: "b"}}
	out := ApplyFilters(models, nil, nil)
	if len(out) != 2 {
		t.Fatalf("expected 2 models, got %d", len(out))
	}
	// Verify it's a copy, not the same slice.
	out[0].ProviderModel = "changed"
	if models[0].ProviderModel == "changed" {
		t.Error("ApplyFilters did not return a copy")
	}
}

func TestApplyFilters_EmptyInput(t *testing.T) {
	out := ApplyFilters(nil, []string{"*"}, nil)
	if out != nil {
		t.Errorf("expected nil for empty input, got %d", len(out))
	}
}

func TestApplyFilters_InvalidPatternIgnored(t *testing.T) {
	models := []Model{{ProviderModel: "a"}}
	// "[" is an invalid glob in path.Match.
	out := ApplyFilters(models, []string{"["}, nil)
	if len(out) != 0 {
		t.Errorf("invalid pattern should match nothing, got %d", len(out))
	}
}

func TestModelKey_StripsTagAndLowercases(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"kimi-k2:cloud", "kimi-k2"},
		{"Kimi-K2", "kimi-k2"},
		{"llama3", "llama3"},
		{"  Spaced  ", "spaced"},
		{"", ""},
	}
	for _, tc := range tests {
		got := ModelKey(tc.input)
		if got != tc.want {
			t.Errorf("ModelKey(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestCandidateKeys(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		// No tag — single candidate.
		{"deepseek-v4-flash", []string{"deepseek-v4-flash"}},
		// Versioned tag — full ID first, then stripped.
		{"deepseek-v4-flash:0731", []string{"deepseek-v4-flash:0731", "deepseek-v4-flash"}},
		// Versioned + deployment suffix — three candidates.
		{"deepseek-v4-flash:0731-cloud", []string{"deepseek-v4-flash:0731-cloud", "deepseek-v4-flash:0731", "deepseek-v4-flash"}},
		// Bare deployment marker — full ID, then stripped.
		{"kimi-k2.7-code:cloud", []string{"kimi-k2.7-code:cloud", "kimi-k2.7-code"}},
		// Size tag preserved.
		{"gpt-oss:20b", []string{"gpt-oss:20b", "gpt-oss"}},
		// Empty.
		{"", nil},
		// Uppercase normalized.
		{"DeepSeek-V4-Flash:0731", []string{"deepseek-v4-flash:0731", "deepseek-v4-flash"}},
	}
	for _, tc := range tests {
		got := CandidateKeys(tc.input)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("CandidateKeys(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

func TestMerge_VersionedTagHitsCorrectEnrichment(t *testing.T) {
	// Two base models: the old unversioned and the new versioned.
	base := []Model{
		{ProviderModel: "deepseek-v4-flash", Name: "deepseek-v4-flash", Source: SourceOllama},
		{ProviderModel: "deepseek-v4-flash:0731", Name: "deepseek-v4-flash:0731", Source: SourceOllama},
	}
	// Two enriched entries from models.dev with distinct keys.
	enriched := []Model{
		{
			ProviderModel: "deepseek-v4-flash",
			Name:          "DeepSeek V4 Flash",
			ReleaseDate:   time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC),
		},
		{
			ProviderModel: "deepseek-v4-flash:0731",
			Name:          "DeepSeek V4 Flash 0731",
			ReleaseDate:   time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC),
		},
	}

	out := Merge(base, enriched)
	if len(out) != 2 {
		t.Fatalf("expected 2 models, got %d", len(out))
	}

	// The unversioned model must enrich from the old entry.
	if out[0].Name != "DeepSeek V4 Flash" {
		t.Errorf("unversioned Name = %q, want %q", out[0].Name, "DeepSeek V4 Flash")
	}
	if !out[0].ReleaseDate.Equal(time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("unversioned ReleaseDate = %v, want 2026-04-24", out[0].ReleaseDate)
	}

	// The versioned model must enrich from the new entry, not the old one.
	if out[1].Name != "DeepSeek V4 Flash 0731" {
		t.Errorf("versioned Name = %q, want %q", out[1].Name, "DeepSeek V4 Flash 0731")
	}
	if !out[1].ReleaseDate.Equal(time.Date(2026, 7, 31, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("versioned ReleaseDate = %v, want 2026-07-31", out[1].ReleaseDate)
	}
}
