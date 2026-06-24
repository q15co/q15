// Package modelselection infers currently available request requirements and
// plans capability-aware model selection for agent turns.
//
// The package provides two inference entry points:
//   - InferRequirements returns text-only requirements (backward-compatible
//     default used by the engine when the planner does not implement
//     InferencePlanner).
//   - InferRequirementsWithCandidates can require tool_calling when no
//     non-tool-capable fallback exists in the candidate set.
//
// The Score function implements the deterministic hybrid selection policy:
// it filters candidates by capability, then sorts by cheapest known cost,
// best benchmark, and configured order. When the deterministic pick has low
// confidence (ambiguous tie or no signal), Plan.Escalation records an advisory
// reason for logging. The agent-research escalation loop is future work.
package modelselection
