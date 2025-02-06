package rpc

import "github.com/shadowglass-xyz/prism/internal/model"

type (
	AgentStateArgs  struct{}
	AgentStateReply struct {
		Containers map[string]model.Container
		Claims     map[string]struct{}
	}
	RemoveClaimArgs struct {
		Claim string
	}
	RemoveClaimReply struct {
		Success    bool
		Containers map[string]model.Container
		Claims     map[string]struct{}
	}
	AddClaimArgs struct {
		Claim string
	}
	AddClaimReply struct {
		Success    bool
		Containers map[string]model.Container
		Claims     map[string]struct{}
	}
)
