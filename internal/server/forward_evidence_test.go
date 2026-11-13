package server

import (
	"connectrpc.com/connect"
	"context"
	"errors"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"testing"
	"time"
)

func unf(err error) *pb.NotForwarded {
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return nil
	}
	for _, d := range ce.Details() {
		v, e := d.Value()
		if n, ok := v.(*pb.NotForwarded); e == nil && ok {
			return n
		}
	}
	return nil
}
func TestForwardEvidenceStopsAtEnqueue(t *testing.T) {
	ts := newTestServer(t)
	a := ts.account(t, ts.keys)
	env, _ := opaqueEnvelope(&pb.Header{Version: deviceauth.Version, Kind: pb.PayloadKind_PAYLOAD_KIND_COMMAND, MachineId: a.mid, Sender: a.cid, RequestId: "0123456789abcdef0123456789abcdef", DeadlineMs: time.Now().Add(time.Minute).UnixMilli()}, []byte("fixture"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := forwardRequest(&pb.ForwardRequest{MachineId: a.mid, Command: env})
	done := make(chan error, 1)
	go func() { _, err := a.client.Forward(ctx, req); done <- err }()
	a.machineFrame.next(t, func(f *pb.ServerFrame) bool { return f.GetForwardCall() != nil })
	cancel()
	err := <-done
	if err == nil || unf(err) != nil {
		t.Fatal("post-enqueue cancellation claimed not forwarded")
	}
	// A nonexistent machine cannot be queued; proof identifies just this attempt.
	req = forwardRequest(&pb.ForwardRequest{MachineId: "0123456789abcdef0123456789abcdef", Command: env})
	_, err = a.client.Forward(context.Background(), req)
	if p := unf(err); p == nil || p.AttemptId != req.Msg.AttemptId {
		t.Fatal("pre-forward envelope rejection lacks bound proof", err)
	}
	// Quota/admission paths use the same detail without inspecting any plaintext.
	for _, code := range []connect.Code{connect.CodeUnavailable, connect.CodeResourceExhausted, connect.CodeUnauthenticated, connect.CodePermissionDenied, connect.CodeFailedPrecondition, connect.CodeInvalidArgument} {
		err := notForwarded(connect.NewError(code, deviceauth.ErrCapacity), req.Msg.AttemptId, "admission_refused")
		if p := unf(err); p == nil || p.AttemptId != req.Msg.AttemptId {
			t.Fatal(code)
		}
	}
}

func TestOfflineAndFullQueueCarryOnlyThisAttempt(t *testing.T) {
	h := newHub(&Server{cfg: Config{ForwardTimeout: time.Second}})
	const attempt = "0123456789abcdef0123456789abcdef"
	fc := &pb.ForwardCall{AttemptId: attempt}
	_, err := h.account("account").forward(context.Background(), "machine", fc)
	if p := unf(err); p == nil || p.AttemptId != attempt || p.Reason != "machine_offline" {
		t.Fatal("offline", err)
	}
	p := &peer{dev: &device{id: "machine", account: "account", kind: pb.DeviceKind_DEVICE_KIND_MACHINE}, out: make(chan queuedFrame), cancel: func() {}}
	h.machines["machine"] = p
	_, err = h.account("account").forward(context.Background(), "machine", fc)
	if p := unf(err); p == nil || p.AttemptId != attempt || p.Reason != "queue_full" {
		t.Fatal("queue full", err)
	}
	if len(p.out) != 0 || len(h.calls) != 0 || h.queueBytes != 0 {
		t.Fatal("rejected attempt was queued or leaked admission")
	}
}
