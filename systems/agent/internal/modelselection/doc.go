// Package modelselection infers currently available request requirements and
// plans capability-aware model selection for agent turns.
//
// Today q15 infers only text requirements from the canonical request path.
// Image-input and tool-calling requirement inference are intentionally deferred
// until the transcript/runtime can prove those requirements explicitly.
package modelselection
