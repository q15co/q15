package main

import "testing"

func TestActionRequiresBuildahEnvIncludesExecNixShellBash(t *testing.T) {
	if !actionRequiresBuildahEnv("exec-nix-shell-bash") {
		t.Fatal("exec-nix-shell-bash should require buildah environment setup")
	}
}
