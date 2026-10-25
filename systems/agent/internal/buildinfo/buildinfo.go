// Package buildinfo identifies the source used to build the running executable.
package buildinfo

import (
	"encoding/hex"
	"runtime/debug"
)

// These values are set by the build when VCS metadata is unavailable, including
// container builds whose source context excludes .git.
var commit, modified string

// Info describes the executable, independently of any runtime workspace checkout.
type Info struct {
	Commit   string
	Modified string
	Source   string
}

// Current reads linked provenance or Go's embedded VCS metadata.
func Current() Info {
	info, _ := debug.ReadBuildInfo()
	return resolve(commit, modified, info)
}

func resolve(linkedCommit, linkedModified string, build *debug.BuildInfo) Info {
	result := Info{Commit: "unknown", Modified: "unknown", Source: "unknown"}
	if validCommit(linkedCommit) {
		result.Commit, result.Modified, result.Source = linkedCommit, sourceModified(
			linkedModified,
		), "linked"
		return result
	}
	if build == nil {
		return result
	}
	for _, setting := range build.Settings {
		switch setting.Key {
		case "vcs.revision":
			if validCommit(setting.Value) {
				result.Commit, result.Source = setting.Value, "go-vcs"
			}
		case "vcs.modified":
			result.Modified = sourceModified(setting.Value)
		}
	}
	return result
}

func validCommit(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func sourceModified(value string) string {
	if value == "true" || value == "false" {
		return value
	}
	return "unknown"
}
