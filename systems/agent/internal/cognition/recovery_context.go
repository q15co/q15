package cognition

import "context"

type memoryRecoveryOwnerKey struct{}

// MemoryRecoveryOwner identifies the trusted cognition run making a file write.
// It is set by Runner rather than by model-provided tool arguments.
func MemoryRecoveryOwner(ctx context.Context) string {
	owner, _ := ctx.Value(memoryRecoveryOwnerKey{}).(string)
	return owner
}

func withMemoryRecoveryOwner(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, memoryRecoveryOwnerKey{}, id)
}
