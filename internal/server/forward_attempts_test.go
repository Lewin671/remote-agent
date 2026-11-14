package server

import (
	"context"
	"errors"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
)

func TestRefusalSealsTheAttemptBeforeRecovery(t *testing.T) {
	l := newAttemptLedger()
	deadline := time.Now().Add(time.Minute).UnixMilli()
	a, err := l.reserve("account", "client", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", l.epoch, deadline, [32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	shared := connect.NewError(connect.CodeUnavailable, errors.New("offline"))
	first := a.refuse(shared, "machine_offline")
	if unf(first) == nil || unf(shared) != nil {
		t.Fatal("refusal missing or shared error mutated")
	}
	b, err := l.reserve("account", "client", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", l.epoch, deadline, [32]byte{2})
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	if err = b.enqueue(context.Background(), func() bool { calls++; return true }); err != nil {
		t.Fatal(err)
	}
	// A1 got NotForwarded; capacity/machine recovery admits b, but late A0
	// must not acquire a permit. It carries the identical original body/epoch.
	late, err := l.reserve("account", "client", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", l.epoch, deadline, [32]byte{1})
	if late != nil {
		_ = late.enqueue(context.Background(), func() bool { calls++; return true })
	}
	if calls != 1 || unf(err) == nil || !proto.Equal(unf(first), unf(err)) {
		t.Fatalf("refused attempt was reopened: calls=%d error=%v", calls, err)
	}
	// Stateful authentication is not part of the replayed body. A valid but
	// different credential must not reopen an attempt refused for sender mismatch.
	changed, changedErr := l.reserve("another-account", "another-device", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", l.epoch, deadline, [32]byte{1})
	if changed != nil || unf(changedErr) == nil {
		t.Fatal("changed credentials reopened refused body", changedErr)
	}
	// A different body cannot borrow the old refusal's diagnostic proof.
	_, err = l.reserve("account", "client", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", l.epoch, deadline, [32]byte{3})
	if unf(err) != nil || connect.CodeOf(err) != connect.CodeAborted {
		t.Fatal("body mismatch reused a proof", err)
	}
}

func TestAttemptEpochCapacityAndClockCannotForgetDispatch(t *testing.T) {
	wall, mono := int64(1_000_000), int64(0)
	l := newAttemptLedger()
	l.limit = 1
	l.clock = func() (int64, int64) { return wall, mono }
	deadline := wall + 1_000
	a, err := l.reserve("account", "client", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", l.epoch, deadline, [32]byte{})
	if err != nil {
		t.Fatal(err)
	}
	if err = a.enqueue(context.Background(), func() bool { return true }); err != nil {
		t.Fatal(err)
	}
	checkUnknown := func(err error) {
		t.Helper()
		if err == nil || unf(err) != nil {
			t.Fatal("unsealed/old attempt claimed NotForwarded", err)
		}
	}
	_, err = l.reserve("account", "client", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", l.epoch, deadline, [32]byte{})
	checkUnknown(err)
	_, err = l.reserve("account", "client", "cccccccccccccccccccccccccccccccc", l.epoch, deadline, [32]byte{})
	checkUnknown(err)
	fresh := newAttemptLedger()
	fresh.clock = l.clock
	_, err = fresh.reserve("account", "client", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", l.epoch, deadline, [32]byte{})
	checkUnknown(err)
	_, err = l.reserve("account", "client", "dddddddddddddddddddddddddddddddd", "", deadline, [32]byte{})
	checkUnknown(err)
	// Wall time alone cannot evict the record and give a delayed copy fresh status.
	wall = deadline + resultQueryGrace + attemptGrace + 1
	_, err = l.reserve("account", "client", "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", l.epoch, wall+1_000, [32]byte{})
	checkUnknown(err)
	if len(l.entries) != 1 {
		t.Fatal("wall-only cleanup")
	}
	// Nor may elapsed time alone, while the envelope's wall deadline is still ahead.
	wall = 1_000_000
	mono = resultQueryGrace + attemptGrace + 2_000
	_, err = l.reserve("account", "client", "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", l.epoch, l.floor+1_000, [32]byte{})
	checkUnknown(err)
	if len(l.entries) != 1 {
		t.Fatal("monotonic-only cleanup")
	}
	wall = deadline + resultQueryGrace + attemptGrace + 1
	if _, err = l.reserve("account", "client", "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", l.epoch, wall+1_000, [32]byte{}); err != nil {
		t.Fatal("eligible cleanup failed", err)
	}
	wall = 1_000_000
	l.limit = 2
	_, err = l.reserve("account", "client", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", l.epoch, deadline, [32]byte{})
	checkUnknown(err)
}

func TestConcurrentCopiesHaveOneEnqueueOwner(t *testing.T) {
	l := newAttemptLedger()
	deadline := time.Now().Add(time.Minute).UnixMilli()
	var workers sync.WaitGroup
	var mu sync.Mutex
	calls := 0
	for range 12 {
		workers.Go(func() {
			p, err := l.reserve("account", "client", "ffffffffffffffffffffffffffffffff", l.epoch, deadline, [32]byte{})
			if err != nil {
				if unf(err) != nil {
					t.Error("duplicate claimed NotForwarded")
				}
				return
			}
			if err = p.enqueue(context.Background(), func() bool { mu.Lock(); calls++; mu.Unlock(); return true }); err != nil {
				t.Error(err)
			}
		})
	}
	workers.Wait()
	if calls != 1 {
		t.Fatalf("%d enqueue owners", calls)
	}
}

func TestRepeatedHTTPAttemptCannotDowngradeAfterEnqueue(t *testing.T) {
	ts := newTestServer(t)
	a := ts.account(t, ts.keys)
	env, _ := opaqueEnvelope(&pb.Header{Version: deviceauth.Version, Kind: pb.PayloadKind_PAYLOAD_KIND_COMMAND, MachineId: a.mid, Sender: a.cid, RequestId: "0123456789abcdef0123456789abcdef", DeadlineMs: time.Now().Add(time.Minute).UnixMilli()}, []byte("fixture"))
	req := forwardRequest(&pb.ForwardRequest{MachineId: a.mid, Command: env}, ts.srv.forwardAttempts.epoch)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := a.client.Forward(ctx, req); done <- err }()
	a.machineFrame.next(t, func(f *pb.ServerFrame) bool { return f.GetForwardCall() != nil })
	cancel()
	<-done
	ts.srv.hub.mu.Lock()
	delete(ts.srv.hub.machines, a.mid)
	ts.srv.hub.mu.Unlock()
	// Simulates the same POST replaying on a new connection after the original
	// response connection failed. The second body keeps its epoch and attempt ID.
	replay := connect.NewRequest(proto.Clone(req.Msg).(*pb.ForwardRequest))
	replay.Header().Set(attemptHeader, req.Msg.AttemptId)
	_, err := a.client.Forward(context.Background(), replay)
	if unf(err) != nil || connect.CodeOf(err) != connect.CodeAborted {
		t.Fatal("second POST erased first dispatch", err)
	}
	// Early authentication failures have no sealed decision and cannot mint proof.
	anonymous := connect.NewRequest(proto.Clone(req.Msg).(*pb.ForwardRequest))
	anonymous.Header().Set(attemptHeader, req.Msg.AttemptId)
	anon := remoteagentv1connect.NewClientServiceClient(http.DefaultClient, ts.url)
	_, err = anon.Forward(context.Background(), anonymous)
	if unf(err) != nil {
		t.Fatal("authentication reused an unsealed proof", err)
	}
}

func TestOldEpochBodyAndEarlyRefusalCannotMintEvidence(t *testing.T) {
	ts := newTestServer(t)
	a := ts.account(t, ts.keys)
	env, _ := opaqueEnvelope(&pb.Header{Version: deviceauth.Version, Kind: pb.PayloadKind_PAYLOAD_KIND_COMMAND, MachineId: a.mid, Sender: a.cid, RequestId: "0123456789abcdef0123456789abcdef", DeadlineMs: time.Now().Add(time.Minute).UnixMilli()}, []byte("fixture"))
	req := forwardRequest(&pb.ForwardRequest{MachineId: a.mid, Command: env}, ts.srv.forwardAttempts.epoch)
	for range cap(ts.srv.rpcAuthLookups) {
		ts.srv.rpcAuthLookups <- struct{}{}
	}
	_, err := a.client.Forward(context.Background(), req)
	for range cap(ts.srv.rpcAuthLookups) {
		<-ts.srv.rpcAuthLookups
	}
	if err == nil || unf(err) != nil {
		t.Fatal("pre-body admission invented proof", err)
	}
	oldEpoch := ts.srv.forwardAttempts.epoch
	if err := ts.srv.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := New(ts.srv.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	hs := httptest.NewServer(next.Handler())
	defer hs.Close()
	if oldEpoch == next.forwardAttempts.epoch {
		t.Fatal("restart retained epoch")
	}
	api := remoteagentv1connect.NewClientServiceClient(http.DefaultClient, hs.URL, withToken(a.ctok))
	_, err = api.Forward(context.Background(), req)
	if err == nil || unf(err) != nil || connect.CodeOf(err) != connect.CodeAborted {
		t.Fatal("old replay became a fresh refusal", err)
	}
}

func TestNotForwardedCopiesSharedErrors(t *testing.T) {
	shared := connect.NewError(connect.CodeResourceExhausted, errors.New("full"))
	for _, id := range []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"} {
		err := notForwarded(shared, id, "epoch", "admission_refused")
		if unf(err) == nil || unf(err).AttemptId != id {
			t.Fatal("unbound proof")
		}
		var ce *connect.Error
		errors.As(err, &ce)
		if len(ce.Details()) != 1 || len(shared.Details()) != 0 {
			t.Fatal("per-attempt details mutated shared error")
		}
	}
}

func TestAttemptQueryWindowKeepsSealedDecisions(t *testing.T) {
	wall, mono := int64(1_000_000), int64(0)
	l := newAttemptLedger()
	l.clock = func() (int64, int64) { return wall, mono }
	deadline := wall + 1000
	a, err := l.reserve("account", "client", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", l.epoch, deadline, [32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	refused := a.refuse(connect.NewError(connect.CodeUnavailable, errors.New("offline")), "machine_offline")
	b, err := l.reserve("account", "client", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", l.epoch, deadline, [32]byte{2})
	if err != nil {
		t.Fatal(err)
	}
	sent := 0
	if err = b.enqueue(context.Background(), func() bool { sent++; return true }); err != nil {
		t.Fatal(err)
	}
	for _, offset := range []int64{1, resultQueryGrace - 1} {
		wall = deadline + offset
		mono = 1000 + offset
		if p, err := l.reserve("account", "client", a.id, l.epoch, deadline, [32]byte{1}); p != nil || !proto.Equal(unf(err), unf(refused)) {
			t.Fatal("expired refusal changed", err)
		}
		if p, err := l.reserve("account", "client", b.id, l.epoch, deadline, [32]byte{2}); p != nil || unf(err) != nil || err.Error() != "aborted: attempt_duplicate" {
			t.Fatal("expired dispatch was forgotten", err)
		}
	}
	query, err := l.reserve("account", "client", "cccccccccccccccccccccccccccccccc", l.epoch, deadline, [32]byte{3})
	if err != nil {
		t.Fatal("explicit retained-result query refused", err)
	}
	if err = query.enqueue(context.Background(), func() bool { sent++; return true }); err != nil {
		t.Fatal("retained-result query not enqueued", err)
	}
	if sent != 2 {
		t.Fatal("wrong enqueue count", sent)
	}
	wall = deadline + resultQueryGrace
	mono = 1000 + resultQueryGrace
	if p, err := l.reserve("account", "client", "dddddddddddddddddddddddddddddddd", l.epoch, deadline, [32]byte{4}); p != nil || err == nil || unf(err) != nil {
		t.Fatal("past query window admitted or claimed not forwarded", err)
	}
}

func TestAttemptSealOutlivesTheOriginalCleanupPoint(t *testing.T) {
	wall, mono := int64(1_000_000), int64(0)
	l := newAttemptLedger()
	l.limit = 1
	l.clock = func() (int64, int64) { return wall, mono }
	deadline := wall + 1000
	a, err := l.reserve("account", "client", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", l.epoch, deadline, [32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	refused := a.refuse(connect.NewError(connect.CodeUnavailable, errors.New("offline")), "machine_offline")
	wall = deadline + attemptGrace + 1
	mono = 1000 + attemptGrace + 1
	// Both clocks crossed the former expiry, but late A0 is still queryable.
	// Full capacity must stay unknown, without evicting A1's sealed refusal.
	p, err := l.reserve("account", "client", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", l.epoch, wall+1000, [32]byte{2})
	if p != nil || err == nil || unf(err) != nil {
		t.Fatal("old cleanup reopened capacity during query window", err)
	}
	if p, err = l.reserve("account", "client", a.id, l.epoch, deadline, [32]byte{1}); p != nil || !proto.Equal(unf(err), unf(refused)) {
		t.Fatal("late A0 reopened refused authorization", err)
	}
	want := deadline + int64(deviceauth.ResultRetention/time.Millisecond) + attemptGrace
	if a.entry.expires != want || a.entry.monotonicExpiry != want-1_000_000 {
		t.Fatal("seal did not cover complete result window")
	}
}

func TestAttemptQueryWindowIsRecheckedAtEnqueue(t *testing.T) {
	wall, mono := int64(1_000_000), int64(0)
	l := newAttemptLedger()
	l.clock = func() (int64, int64) { return wall, mono }
	deadline := wall - 1000 // An expired execution authorization, still queryable.
	p, err := l.reserve("account", "client", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", l.epoch, deadline, [32]byte{})
	if err != nil {
		t.Fatal(err)
	}
	wall = deadline + resultQueryGrace
	mono += resultQueryGrace
	sent := false
	err = p.enqueue(context.Background(), func() bool { sent = true; return true })
	if sent || err == nil || unf(err) != nil {
		t.Fatal("delayed admission crossed result query window", err)
	}
}
