// Package sandboxcontract defines the shared IPC contract between the q15 agent
// runtime and the sandbox helper process.
package sandboxcontract

// Settings describes a persistent sandbox container and its mounted workspace.
type Settings struct {
	ContainerName    string         `json:"container_name"`
	WorkspaceHostDir string         `json:"workspace_host_dir"`
	WorkspaceDir     string         `json:"workspace_dir"`
	MemoryHostDir    string         `json:"memory_host_dir"`
	MemoryDir        string         `json:"memory_dir"`
	Proxy            *ProxySettings `json:"proxy,omitempty"`
}

// ProxySettings describes optional proxy/trust wiring for sandbox command runs.
type ProxySettings struct {
	Enabled              bool              `json:"enabled"`
	HTTPProxyURL         string            `json:"http_proxy_url,omitempty"`
	HTTPSProxyURL        string            `json:"https_proxy_url,omitempty"`
	AllProxyURL          string            `json:"all_proxy_url,omitempty"`
	NoProxy              string            `json:"no_proxy,omitempty"`
	CACertHostPath       string            `json:"ca_cert_host_path,omitempty"`
	CACertContainerPath  string            `json:"ca_cert_container_path,omitempty"`
	SetLowercaseProxyEnv bool              `json:"set_lowercase_proxy_env,omitempty"`
	Env                  map[string]string `json:"env,omitempty"`
}

// HelperRequest is sent from the agent runtime to the sandbox helper.
type HelperRequest struct {
	Settings Settings `json:"settings"`
	Command  string   `json:"command,omitempty"`
}

// RuntimeMetadata describes sandbox runtime properties owned by the helper.
type RuntimeMetadata struct {
	Runtime   string `json:"runtime,omitempty"`
	BaseImage string `json:"base_image,omitempty"`
}

// HelperResponse is returned by the sandbox helper.
type HelperResponse struct {
	Output   string           `json:"output,omitempty"`
	Metadata *RuntimeMetadata `json:"metadata,omitempty"`
	Error    string           `json:"error,omitempty"`
}
