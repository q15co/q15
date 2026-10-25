package buildinfo

import (
	"runtime/debug"
	"strings"
	"testing"
)

func TestExecutableProvenanceUsesLinkedValuesOrEmbeddedVCS(t *testing.T) {
	linked := strings.Repeat("a", 40)
	embedded := strings.Repeat("b", 40)
	vcs := &debug.BuildInfo{
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: embedded},
			{Key: "vcs.modified", Value: "true"},
		},
	}
	for _, tc := range []struct {
		name, commit, modified string
		build                  *debug.BuildInfo
		want                   Info
	}{
		{"linked takes precedence", linked, "false", vcs, Info{linked, "false", "linked"}},
		{"dirty linked build", linked, "true", nil, Info{linked, "true", "linked"}},
		{"embedded VCS", "", "", vcs, Info{embedded, "true", "go-vcs"}},
		{"unversioned source", "", "", nil, Info{"unknown", "unknown", "unknown"}},
		{"unknown dirty state", linked, "", nil, Info{linked, "unknown", "linked"}},
		{"invalid linked hash", `</agent_build>`, "false", vcs, Info{embedded, "true", "go-vcs"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolve(tc.commit, tc.modified, tc.build); got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
