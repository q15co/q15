package chatpb

// ProtocolVersion is the version of the ChatService wire contract this module
// implements. Bump it only for a breaking change: fields are additive, numbers
// are never reused, and a removed field has both its number and its name
// reserved. Consumers compare it against GetRuntimeInfoResponse.ProtocolVersion
// and must treat a mismatch as a hard startup failure rather than a warning.
const ProtocolVersion int32 = 2
