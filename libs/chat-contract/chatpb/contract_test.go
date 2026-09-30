package chatpb

import "testing"

var _ int32 = ProtocolVersion

func TestGeneratedContractTypesCompile(t *testing.T) {
	t.Parallel()

	_ = &GetRuntimeInfoRequest{}
	_ = &GetRuntimeInfoResponse{}
	_ = &OpenSessionRequest{}
	_ = &OpenSessionResponse{}
	_ = &SendMessageRequest{}
	_ = &SendMessageResponse{}
	_ = &AbortRequest{}
	_ = &AbortResponse{}
	_ = &WatchEventsRequest{}
	_ = &WatchEventsResponse{}
	_ = &ListTurnsRequest{}
	_ = &ListTurnsResponse{}
	_ = &DeliverRequest{}
	_ = &DeliverResponse{}

	var _ ChatServiceClient = (*chatServiceClient)(nil)
	var _ ChatServiceServer = (*UnimplementedChatServiceServer)(nil)
}
