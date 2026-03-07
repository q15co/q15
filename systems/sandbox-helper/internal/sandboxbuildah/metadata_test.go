package sandboxbuildah

import "testing"

func TestMetadataReturnsAuthoritativeRuntimeProperties(t *testing.T) {
	metadata := Metadata()

	if got, want := metadata.Runtime, sandboxRuntimeLabel; got != want {
		t.Fatalf("Metadata().Runtime = %q, want %q", got, want)
	}
	if got, want := metadata.BaseImage, sandboxBaseImage; got != want {
		t.Fatalf("Metadata().BaseImage = %q, want %q", got, want)
	}
}
