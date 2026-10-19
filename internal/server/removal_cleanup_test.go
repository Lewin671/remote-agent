package server

import (
	"connectrpc.com/connect"
	"context"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"net/http"
	"testing"
	"time"
)

func TestCommittedRemovalRevokesStreamsWhenLeaseWriteFails(t *testing.T) {
	ts := newTestServer(t)
	_, mid, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_MACHINE, "setup")
	if err != nil {
		t.Fatal(err)
	}
	c := ts.authority(t)
	parent := c.states[len(c.states)-1]
	members := []deviceauth.Member{}
	for _, m := range parent.Core.Members {
		if m.ID != mid {
			members = append(members, m)
		}
	}
	secret := make([]byte, 32)
	secret[0] = 97
	next, err := deviceauth.BuildMembershipState(parent, members, append(parent.Core.Removed, mid), c.secret, secret, c.phoneEd, uint64(time.Now().UnixMilli()))
	if err != nil {
		t.Fatal(err)
	}
	lease, err := deviceauth.BuildLease(next, c.phoneEd, next.Core.IssuedAt)
	if err != nil {
		t.Fatal(err)
	}
	stateRaw, _ := next.MarshalBinary()
	leaseRaw, _ := lease.MarshalBinary()
	streamCtx, streamCancel := context.WithCancel(context.Background())
	defer streamCancel()
	hubCtx, hubCancel := context.WithCancel(context.Background())
	defer hubCancel()
	ts.srv.streams[mid] = map[*streamAuth]struct{}{{cancel: streamCancel}: {}}
	ts.srv.hub.machines[mid] = &peer{dev: &device{id: mid, account: ts.acct()}, cancel: hubCancel}
	if _, err := ts.tenant().db.Exec(`CREATE TRIGGER fail_lease BEFORE INSERT ON security_leases BEGIN SELECT RAISE(ABORT,'synthetic lease failure'); END`); err != nil {
		t.Fatal(err)
	}
	api := remoteagentv1connect.NewClientServiceClient(http.DefaultClient, ts.url, withToken(ts.login(t, c.phoneEd, c.phoneID)))
	if _, err := api.PutSecurityState(context.Background(), connect.NewRequest(&pb.PutSecurityStateRequest{States: []*pb.SecurityState{{State: stateRaw}}, Lease: leaseRaw})); err == nil {
		t.Fatal("lease failure not injected")
	}
	head, _, err := ts.tenant().securityHead(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := head.Core.Member(mid); ok {
		t.Fatal("membership did not commit")
	}
	select {
	case <-streamCtx.Done():
	default:
		t.Error("committed removal left authenticated stream alive")
	}
	select {
	case <-hubCtx.Done():
	default:
		t.Error("committed removal skipped hub drop")
	}
}
