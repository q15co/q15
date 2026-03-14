package execpb

import "testing"

func TestGeneratedContractTypesCompile(t *testing.T) {
	t.Parallel()

	_ = &StartSessionRequest{}
	_ = &StartSessionResponse{}
	_ = &WatchSessionResponse{}
}
