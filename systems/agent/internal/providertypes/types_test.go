package providertypes

import "testing"

func TestNormalize(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{name: "ollama", in: " Ollama ", want: Ollama, ok: true},
		{
			name: "openai compatible canonical",
			in:   "openai-compatible",
			want: OpenAICompatible,
			ok:   true,
		},
		{
			name: "openai compatible underscore",
			in:   "openai_compatible",
			want: OpenAICompatible,
			ok:   true,
		},
		{name: "legacy moonshot alias", in: "moonshot", want: OpenAICompatible, ok: true},
		{name: "codex canonical", in: "openai-codex", want: OpenAICodex, ok: true},
		{name: "codex underscore", in: "openai_codex", want: OpenAICodex, ok: true},
		{name: "unsupported", in: "unknown", want: "", ok: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := Normalize(tc.in)
			if got != tc.want || ok != tc.ok {
				t.Fatalf("Normalize(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.ok)
			}
			if got := MustNormalize(tc.in); got != tc.want {
				t.Fatalf("MustNormalize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
