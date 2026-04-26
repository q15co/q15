package files

import (
	"strings"
	"testing"

	"github.com/q15co/q15/systems/agent/internal/agent"
)

func TestPathAccessPolicyAllowsExactReadAndWritePaths(t *testing.T) {
	t.Parallel()

	policy := PathAccessPolicy{
		ReadPaths:  []string{"/memory/semantic/facts.md"},
		WritePaths: []string{"/memory/semantic/facts.md"},
	}
	for _, call := range []agent.ToolCall{
		{
			ID:        "read-1",
			Name:      "read_file",
			Arguments: `{"path":"/memory/semantic/facts.md"}`,
		},
		{
			ID:        "write-1",
			Name:      "write_file",
			Arguments: `{"path":"/memory/semantic/facts.md","content":"# Facts"}`,
		},
		{
			ID:        "edit-1",
			Name:      "edit_file",
			Arguments: `{"path":"/memory/semantic/facts.md","old_text":"old","new_text":"new"}`,
		},
		{
			ID:        "patch-1",
			Name:      "apply_patch",
			Arguments: `{"patch":"*** Begin Patch\n*** Update File: /memory/semantic/facts.md\n@@\n-old\n+new\n*** End Patch"}`,
		},
	} {
		if err := policy.CheckToolCall(call); err != nil {
			t.Fatalf("CheckToolCall(%s) error = %v", call.Name, err)
		}
	}
}

func TestPathAccessPolicyRejectsPathsOutsideAllowlist(t *testing.T) {
	t.Parallel()

	policy := PathAccessPolicy{
		ReadPaths:  []string{"/memory/semantic/facts.md"},
		WritePaths: []string{"/memory/semantic/facts.md"},
	}
	for _, tc := range []struct {
		name string
		call agent.ToolCall
		want string
	}{
		{
			name: "read",
			call: agent.ToolCall{
				ID:        "read-1",
				Name:      "read_file",
				Arguments: `{"path":"/memory/core/AGENT.md"}`,
			},
			want: `read_file path "/memory/core/AGENT.md" is outside allowed read paths`,
		},
		{
			name: "write",
			call: agent.ToolCall{
				ID:        "write-1",
				Name:      "write_file",
				Arguments: `{"path":"/memory/core/AGENT.md","content":"x"}`,
			},
			want: `write_file path "/memory/core/AGENT.md" is outside allowed write paths`,
		},
		{
			name: "patch",
			call: agent.ToolCall{
				ID:        "patch-1",
				Name:      "apply_patch",
				Arguments: `{"patch":"*** Begin Patch\n*** Update File: /memory/semantic/facts.md\n@@\n-old\n+new\n*** Update File: /memory/core/AGENT.md\n@@\n-old\n+new\n*** End Patch"}`,
			},
			want: `apply_patch path "/memory/core/AGENT.md" is outside allowed write paths`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := policy.CheckToolCall(tc.call)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("CheckToolCall() error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestInspectToolCallAccessExtractsPatchMovePaths(t *testing.T) {
	t.Parallel()

	access, ok, err := InspectToolCallAccess(agent.ToolCall{
		ID:        "patch-1",
		Name:      "apply_patch",
		Arguments: `{"patch":"*** Begin Patch\n*** Update File: /memory/semantic/facts.md\n*** Move to: /memory/semantic/projects.md\n@@\n-old\n+new\n*** End Patch"}`,
	})
	if err != nil {
		t.Fatalf("InspectToolCallAccess() error = %v", err)
	}
	if !ok {
		t.Fatal("InspectToolCallAccess() ok = false, want true")
	}
	if got, want := len(access.WritePaths), 2; got != want {
		t.Fatalf("WritePaths len = %d, want %d", got, want)
	}
	if access.WritePaths[0] != "/memory/semantic/facts.md" ||
		access.WritePaths[1] != "/memory/semantic/projects.md" {
		t.Fatalf("WritePaths = %#v", access.WritePaths)
	}
}
