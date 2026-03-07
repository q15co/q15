package sandboxbuildah

// RuntimeMetadata describes authoritative sandbox runtime properties.
type RuntimeMetadata struct {
	Runtime   string
	BaseImage string
}

// Metadata returns the helper-owned sandbox runtime and base image values.
func Metadata() RuntimeMetadata {
	return RuntimeMetadata{
		Runtime:   sandboxRuntimeLabel,
		BaseImage: sandboxBaseImage,
	}
}
