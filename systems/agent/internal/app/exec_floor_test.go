package app

import (
	"strings"
	"testing"

	"github.com/q15co/q15/libs/exec-contract/execpb"
)

func TestExecFloorCommandsReachToolGuidance(t *testing.T) {
	t.Parallel()

	floorCommands := []string{"sed", "awk", "grep", "patch", "ps", "hostname", "rg", "jq"}
	runtimeInfo, err := resolveRuntimeEnvironment(&execpb.GetRuntimeInfoResponse{
		ExecutorType:      "local-nix-shell",
		WorkspaceDir:      "/workspace",
		MemoryDir:         "/memory",
		MediaDir:          "/media",
		SkillsDir:         "/skills",
		ExecFloorCommands: floorCommands,
	})
	if err != nil {
		t.Fatalf("resolveRuntimeEnvironment() error = %v", err)
	}

	toolList, err := buildToolList(toolDeps{execFloorCommands: runtimeInfo.ExecFloorCommands})
	if err != nil {
		t.Fatalf("buildToolList() error = %v", err)
	}
	want := "Base commands present with no `packages`: awk, grep, hostname, jq, patch, ps, rg, sed."
	for _, tool := range toolList {
		if tool.Definition().Name != "exec" {
			continue
		}
		guidance := strings.Join(tool.Definition().PromptGuidance, "\n")
		if !strings.Contains(guidance, want) {
			t.Fatalf("exec guidance missing runtime floor %q:\n%s", want, guidance)
		}
		return
	}
	t.Fatal("tool list is missing exec")
}
