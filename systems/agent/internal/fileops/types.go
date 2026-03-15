package fileops

// Settings describes the agent-local rooted directory mapping used by file ops.
type Settings struct {
	WorkspaceLocalDir   string
	WorkspaceRuntimeDir string
	MemoryLocalDir      string
	MemoryRuntimeDir    string
	SkillsLocalDir      string
	SkillsRuntimeDir    string
}

// ReadResult describes one rooted text file read.
type ReadResult struct {
	Content         string
	Truncated       bool
	NextOffsetLines int
	TotalLines      int
}

// WriteResult describes one successful atomic file write.
type WriteResult struct {
	Path         string
	BytesWritten int
}

// EditResult describes one exact text replacement.
type EditResult struct {
	Path             string
	Diff             string
	FirstChangedLine int
}

// ApplyPatchResult describes one successful multi-file patch application.
type ApplyPatchResult struct {
	ChangedFiles []string
	Diff         string
	Summary      string
}
