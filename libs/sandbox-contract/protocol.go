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
	SkillsHostDir    string         `json:"skills_host_dir,omitempty"`
	SkillsDir        string         `json:"skills_dir,omitempty"`
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

// ExecNixShellBashRequest describes a command run that requires explicit Nix
// packages and executes via bash inside a nix shell.
type ExecNixShellBashRequest struct {
	Command  string   `json:"command"`
	Packages []string `json:"packages"`
}

// ReadFileRequest describes a rooted text file read with optional paging.
type ReadFileRequest struct {
	Path        string `json:"path"`
	OffsetLines int    `json:"offset_lines,omitempty"`
	LimitLines  int    `json:"limit_lines,omitempty"`
}

// WriteFileRequest describes an atomic text file write inside a rooted area.
type WriteFileRequest struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// EditFileRequest describes an exact text replacement inside one rooted file.
type EditFileRequest struct {
	Path    string `json:"path"`
	OldText string `json:"old_text"`
	NewText string `json:"new_text"`
}

// ApplyPatchRequest describes a multi-file Codex-style patch application.
type ApplyPatchRequest struct {
	Patch string `json:"patch"`
}

// HelperRequest is sent from the agent runtime to the sandbox helper.
type HelperRequest struct {
	Settings         Settings                 `json:"settings"`
	Command          string                   `json:"command,omitempty"`
	ExecNixShellBash *ExecNixShellBashRequest `json:"exec_nix_shell_bash,omitempty"`
	ReadFile         *ReadFileRequest         `json:"read_file,omitempty"`
	WriteFile        *WriteFileRequest        `json:"write_file,omitempty"`
	EditFile         *EditFileRequest         `json:"edit_file,omitempty"`
	ApplyPatch       *ApplyPatchRequest       `json:"apply_patch,omitempty"`
}

// RuntimeMetadata describes sandbox runtime properties owned by the helper.
type RuntimeMetadata struct {
	Runtime   string `json:"runtime,omitempty"`
	BaseImage string `json:"base_image,omitempty"`
}

// ReadFileResult describes one rooted text file read.
type ReadFileResult struct {
	Content         string `json:"content"`
	Truncated       bool   `json:"truncated,omitempty"`
	NextOffsetLines int    `json:"next_offset_lines,omitempty"`
	TotalLines      int    `json:"total_lines,omitempty"`
}

// WriteFileResult describes one successful atomic file write.
type WriteFileResult struct {
	Path         string `json:"path"`
	BytesWritten int    `json:"bytes_written"`
}

// EditFileResult describes one exact text replacement.
type EditFileResult struct {
	Path             string `json:"path"`
	Diff             string `json:"diff,omitempty"`
	FirstChangedLine int    `json:"first_changed_line,omitempty"`
}

// ApplyPatchResult describes one successful multi-file patch application.
type ApplyPatchResult struct {
	ChangedFiles []string `json:"changed_files,omitempty"`
	Diff         string   `json:"diff,omitempty"`
	Summary      string   `json:"summary,omitempty"`
}

// HelperResponse is returned by the sandbox helper.
type HelperResponse struct {
	Output     string            `json:"output,omitempty"`
	Metadata   *RuntimeMetadata  `json:"metadata,omitempty"`
	ReadFile   *ReadFileResult   `json:"read_file,omitempty"`
	WriteFile  *WriteFileResult  `json:"write_file,omitempty"`
	EditFile   *EditFileResult   `json:"edit_file,omitempty"`
	ApplyPatch *ApplyPatchResult `json:"apply_patch,omitempty"`
	Error      string            `json:"error,omitempty"`
}
