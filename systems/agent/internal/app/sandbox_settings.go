package app

import (
	"github.com/q15co/q15/systems/agent/internal/config"
	"github.com/q15co/q15/systems/agent/internal/sandbox"
)

func buildSandboxSettings(
	rt config.AgentRuntime,
	proxySettings *sandbox.ProxySettings,
) sandbox.Settings {
	settings := sandbox.Settings{
		ContainerName:    rt.SandboxContainerName,
		WorkspaceHostDir: rt.WorkspaceHostDir,
		WorkspaceDir:     rt.WorkspaceDir,
		MemoryHostDir:    rt.MemoryHostDir,
		MemoryDir:        rt.MemoryDir,
	}
	settings.Proxy = proxySettings

	return settings
}
