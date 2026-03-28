package service

import (
	"reflect"
	"testing"
)

func TestBuildNixShellArgsUsesBashAndInjectsRuntimePackage(t *testing.T) {
	t.Parallel()

	args := buildNixShellArgs("printf alpha", []string{"nixpkgs#git"}, "/certs/ca.pem")

	want := []string{
		"--extra-experimental-features",
		"nix-command flakes",
		"--option",
		"ssl-cert-file",
		"/certs/ca.pem",
		"shell",
		"nixpkgs#git",
		"nixpkgs#bash",
		"--command",
		"bash",
		"-lc",
		"printf alpha",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("buildNixShellArgs() = %#v, want %#v", args, want)
	}
}

func TestBuildNixShellArgsDoesNotDuplicateRuntimePackage(t *testing.T) {
	t.Parallel()

	args := buildNixShellArgs(
		"printf alpha",
		[]string{"nixpkgs#bash", "nixpkgs#git"},
		"/certs/ca.pem",
	)

	var bashCount int
	for _, arg := range args {
		if arg == runtimeBashPackage {
			bashCount++
		}
	}
	if bashCount != 1 {
		t.Fatalf("runtime bash package count = %d, want 1 in %#v", bashCount, args)
	}
}
