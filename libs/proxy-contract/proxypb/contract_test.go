package proxypb

import "testing"

func TestGeneratedContractTypesCompile(t *testing.T) {
	t.Parallel()

	_ = &GetRuntimeInfoRequest{}
	_ = &GetRuntimeInfoResponse{}
	_ = &RuntimeCapability{}
}
