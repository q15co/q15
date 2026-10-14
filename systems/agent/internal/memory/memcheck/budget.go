package memcheck

// OverLimit reports whether a byte size exceeds the limit it is judged
// against. The comparison is strict, matching the agreement the status report
// and the conversion rules already render: a file exactly at its limit is
// within it, and only a file strictly past the limit is over.
//
// size is whatever the caller measures. limit is the budget or cap the caller
// wants enforced; this package knows no configuration default, so a caller
// passing a budget the runtime resolved from config and a caller passing a
// post-run cap share exactly this one comparison.
func OverLimit(size, limit int64) bool {
	return size > limit
}
