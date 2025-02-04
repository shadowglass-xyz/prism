package rpc

type (
	AgentStateArgs  struct{}
	AgentStateReply struct {
		Containers map[string]struct{}
		Claims     map[string]struct{}
	}
	RemoveClaimArgs struct {
		Claim string
	}
	RemoveClaimReply struct {
		Success    bool
		Containers map[string]struct{}
		Claims     map[string]struct{}
	}
	AddClaimArgs struct {
		Claim string
	}
	AddClaimReply struct {
		Success    bool
		Containers map[string]struct{}
		Claims     map[string]struct{}
	}
)
