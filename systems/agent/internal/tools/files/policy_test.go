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

func TestPathAccessPolicyAllowsDirectChildrenOfListedDirs(t *testing.T) {
	t.Parallel()

	policy := PathAccessPolicy{
		ReadPaths:  []string{"/srv/notes/facts.md"},
		WritePaths: []string{"/srv/notes/facts.md"},
		ReadDirs:   []string{"/srv/notes/topics/"},
		WriteDirs:  []string{"/srv/notes/topics"},
	}
	for _, call := range []agent.ToolCall{
		{Name: "read_file", Arguments: `{"path":"/srv/notes/topics/q15.md"}`},
		{Name: "write_file", Arguments: `{"path":"/srv/notes/topics/new-topic.md","content":"x"}`},
		{Name: "edit_file", Arguments: `{"path":"/srv/notes/topics/./q15.md","old_text":"a","new_text":"b"}`},
		{Name: "apply_patch", Arguments: `{"patch":"*** Begin Patch\n*** Add File: /srv/notes/topics/fresh.md\n+x\n*** End Patch"}`},
		{Name: "write_file", Arguments: `{"path":"/srv/notes/facts.md","content":"x"}`},
	} {
		if err := policy.CheckToolCall(call); err != nil {
			t.Fatalf("CheckToolCall(%s %s) error = %v", call.Name, call.Arguments, err)
		}
	}
}

func TestPathAccessPolicyDirMatchingIsNotAPrefixMatch(t *testing.T) {
	t.Parallel()

	policy := PathAccessPolicy{
		ReadDirs:  []string{"/srv/notes/topics"},
		WriteDirs: []string{"/srv/notes/topics"},
	}
	for _, target := range []string{
		"/srv/notes/topics",                 // the directory itself
		"/srv/notes/topics/nested/deep.md",  // nested subdirectory
		"/srv/notes/topicsevil.md",          // shared string prefix
		"/srv/notes/topics-old/a.md",        // sibling directory
		"/srv/notes/topics/../facts.md",     // traversal out of the dir
		"/srv/notes/topics/../../core/x.md", // traversal further out
		"srv/notes/topics/a.md",             // relative path
		"/srv/core/AGENT.md",                // unrelated path
	} {
		for _, call := range []agent.ToolCall{
			{Name: "read_file", Arguments: `{"path":"` + target + `"}`},
			{Name: "write_file", Arguments: `{"path":"` + target + `","content":"x"}`},
		} {
			if err := policy.CheckToolCall(call); err == nil {
				t.Fatalf("CheckToolCall(%s %q) error = nil, want rejection", call.Name, target)
			}
		}
	}

	// Moving a topic file out of the directory must be rejected on the
	// destination even though the source is allowed.
	err := policy.CheckToolCall(agent.ToolCall{
		Name:      "apply_patch",
		Arguments: `{"patch":"*** Begin Patch\n*** Update File: /srv/notes/topics/a.md\n*** Move to: /srv/core/AGENT.md\n*** End Patch"}`,
	})
	if err == nil || !strings.Contains(err.Error(), "/srv/core/AGENT.md") {
		t.Fatalf("CheckToolCall(move out) error = %v, want rejection", err)
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
