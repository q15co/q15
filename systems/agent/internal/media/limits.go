package media

const (
	// DefaultMaxImageBytes caps image files registered for model inspection or
	// outbound delivery at 20 MiB.
	DefaultMaxImageBytes = 20 << 20
	// DefaultMaxAudioBytes preserves the same bounded registration policy for
	// outbound audio files.
	DefaultMaxAudioBytes = 20 << 20
)
