package server

import (
	"bytes"
	"connectrpc.com/connect"
	"context"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"net/http"
	"testing"
	"time"
)

func TestLeaseRenewalPreservesPreparedRecoveryAndChain(t *testing.T) {
	ts, parent, oldRecovery, c := recoveryFixture(t)
	recoveryAPI := remoteagentv1connect.NewRecoveryServiceClient(http.DefaultClient, ts.url)
	prepared := buildRecovery(t, parent, oldRecovery, 0x74)
	id := submitRecovery(t, recoveryAPI, prepared)
	api := remoteagentv1connect.NewClientServiceClient(http.DefaultClient, ts.url, withToken(ts.login(t, c.phoneEd, c.phoneID)))
	l, err := deviceauth.BuildLease(parent, c.phoneEd, uint64(time.Now().UnixMilli()))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := l.MarshalBinary()
	for i := 0; i < 3; i++ {
		if _, err := api.PutSecurityState(context.Background(), connect.NewRequest(&pb.PutSecurityStateRequest{Lease: raw})); err != nil {
			t.Fatal(err)
		}
	}
	chain, err := api.GetSecurityStates(context.Background(), connect.NewRequest(&pb.GetSecurityStatesRequest{AfterEpoch: parent.Core.Epoch}))
	if err != nil {
		t.Fatal(err)
	}
	if len(chain.Msg.States) != 0 || chain.Msg.HeadEpoch != parent.Core.Epoch || !bytes.Equal(chain.Msg.Lease, raw) || !chain.Msg.RecoveryPending {
		t.Fatal("lease altered membership/recovery")
	}
	completeRecovery(t, ts, recoveryAPI, id, prepared)
}
