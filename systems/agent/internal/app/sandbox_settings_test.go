package app

import (
	"testing"

	"github.com/q15co/q15/systems/agent/internal/config"
)

func TestBuildSandboxSettings_WithoutSkills(t *testing.T) {
	rt := config.AgentRuntime{
		Name:                 "agent-a",
		SandboxContainerName: "q15-agent-a",
		WorkspaceHostDir:     "/tmp/q15/agent-a",
		WorkspaceDir:         "/workspace",
		MemoryHostDir:        "/tmp/q15/agent-a/.q15-memory",
		MemoryDir:            "/memory",
	}

	got := buildSandboxSettings(rt)
	if got.ContainerName != rt.SandboxContainerName {
		t.Fatalf("unexpected container name: %q", got.ContainerName)
	}
	if got.Proxy != nil {
		t.Fatalf("expected nil proxy settings, got %#v", got.Proxy)
	}
}

func TestBuildSandboxSettings_WithSkills(t *testing.T) {
	rt := config.AgentRuntime{
		Name:                 "agent-a",
		SandboxContainerName: "q15-agent-a",
		WorkspaceHostDir:     "/tmp/q15/agent-a",
		WorkspaceDir:         "/workspace",
		MemoryHostDir:        "/tmp/q15/agent-a/.q15-memory",
		MemoryDir:            "/memory",
		SkillsHostDir:        "/tmp/q15/skills",
		SkillsDir:            "/skills",
	}

	got := buildSandboxSettings(rt)
	if got.SkillsHostDir != "/tmp/q15/skills" || got.SkillsDir != "/skills" {
		t.Fatalf("unexpected skills settings: %#v", got)
	}
}
