package relay

import (
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
)

func TestPairChainTooLarge(t *testing.T) {
	states := make([]*pb.SecurityState, deviceauth.MaxPairStates+1)
	if _, _, _, err := verifyPairChain(deviceauth.Pin{}, states, nil, nil); err != deviceauth.ErrChainTooLarge {
		t.Fatalf("state count: %v", err)
	}
	states = make([]*pb.SecurityState, 2)
	big := make([]byte, deviceauth.MaxPairChainBytes/2+1)
	for i := range states {
		states[i] = &pb.SecurityState{State: big}
	}
	if _, _, _, err := verifyPairChain(deviceauth.Pin{}, states, nil, nil); err != deviceauth.ErrChainTooLarge {
		t.Fatalf("byte size: %v", err)
	}
}
