// Package sandboxcontract defines the shared IPC contract between the q15 agent
// runtime and the sandbox helper process.
package sandboxcontract

// Settings describes a persistent sandbox container and its mounted workspace.
type Settings struct {
	ContainerName    string `json:"container_name"`
	FromImage        string `json:"from_image"`
	WorkspaceHostDir string `json:"workspace_host_dir"`
	WorkspaceDir     string `json:"workspace_dir"`
	Network          string `json:"network"`
}

// HelperRequest is sent from the agent runtime to the sandbox helper.
type HelperRequest struct {
	Settings Settings `json:"settings"`
	Command  string   `json:"command,omitempty"`
}

// HelperResponse is returned by the sandbox helper.
type HelperResponse struct {
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}
