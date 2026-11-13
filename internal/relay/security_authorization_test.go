package relay

import (
	"bytes"
	"context"
	"errors"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/protocol"
	"google.golang.org/protobuf/proto"
	"testing"
	"time"
)

func TestAuthorizationPauseRetiresStreamsButNotEndpoint(t *testing.T) {
	c := &conn{}
	keys := initializeTestSecurityKeys(t, c)
	s := c.security
	ctx, closeStream, err := s.streamContext(context.Background(), keys.states[0].Core.Members[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	defer closeStream()
	s.setAuthorized(false)
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("authentication failure left stream alive")
	}
	if !s.blocked.Load() {
		t.Fatal("control was not paused")
	}
	if _, err := s.accept(context.Background(), deviceauth.Wire{}, nil, s.now(), func(deviceauth.Header, []byte) (deviceauth.Completion, error) {
		t.Fatal("paused callback ran")
		return deviceauth.Final(nil), nil
	}); !errors.Is(err, deviceauth.ErrInvalid) {
		t.Fatal(err)
	}
	for _, seal := range []func(*pb.Header, deviceauth.Purpose, []byte, []byte) (*pb.Envelope, error){s.seal, s.sealRaw} {
		if _, err := seal(&pb.Header{}, deviceauth.Result, nil, []byte("fixture")); !errors.Is(err, deviceauth.ErrUntrusted) {
			t.Fatalf("paused output path: %v", err)
		}
	}
	if err := s.endpoint.CheckLease(s.now()); err != nil {
		t.Fatalf("pausing destroyed endpoint: %v", err)
	}
	s.setAuthorized(true)
	fresh, closeFresh, err := s.streamContext(context.Background(), keys.states[0].Core.Members[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFresh()
	if fresh.Err() != nil || ctx.Err() == nil {
		t.Fatal("fresh/retired stream lifetimes mixed")
	}
	s.Close()
	select {
	case <-fresh.Done():
	case <-time.After(time.Second):
		t.Fatal("endpoint shutdown left stream alive")
	}
}

func TestAuthorizationPauseStillReturnsAcceptedResult(t *testing.T) {
	c := &conn{}
	keys := initializeTestSecurityKeys(t, c)
	pin, _, _ := c.security.endpoint.Snapshot()
	member := keys.states[0].Core.Members[0]
	lease, err := deviceauth.BuildLease(keys.states[1], keys.phone, keys.states[1].Core.IssuedAt)
	if err != nil {
		t.Fatal(err)
	}
	client, err := deviceauth.InitEndpoint("", pin, keys.states, []*deviceauth.RecoveryStatement{nil, nil}, member.ID, keys.phone.Seed(), keys.phoneX, lease)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	now := c.security.now()
	h := deviceauth.Header{Purpose: deviceauth.Command, Kind: 5, Machine: c.acct.DeviceID, Request: "pause-query-result", Deadline: now + 60000}
	body, _ := proto.Marshal(&pb.Command{ProtocolVersion: protocol.Version, Kind: &pb.Command_UpdateSettings{UpdateSettings: &pb.UpdateSettingsRequest{}}})
	saved, _ := proto.Marshal(&pb.CommandResult{ErrorCode: "not_found"})
	w, err := client.Prepare(h, nil, body)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	fn := func(deviceauth.Header, []byte) (deviceauth.Completion, error) {
		calls++
		return deviceauth.Final(saved), nil
	}
	if _, err = c.security.accept(context.Background(), w, nil, now, fn); err != nil {
		t.Fatal(err)
	}
	c.security.setAuthorized(false)
	out, err := c.security.accept(context.Background(), w, nil, now, fn)
	if err != nil || !bytes.Equal(out, saved) || calls != 1 {
		t.Fatal("pause hid durable result", err)
	}
	env, err := c.security.sealResult(&pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_COMMAND_RESULT, MachineId: c.acct.DeviceID, RequestId: h.Request}, nil, out, false)
	if err != nil {
		t.Fatal(err)
	}
	result, err := deviceauth.FromEnvelope(env)
	if err != nil {
		t.Fatal(err)
	}
	_, plain, err := client.Open(result, nil, false, now)
	if err != nil || !bytes.Equal(plain, saved) {
		t.Fatal("paused result cannot be authenticated", err)
	}
	h.Request = "pause-new-command"
	w, err = client.Prepare(h, nil, body)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.security.accept(context.Background(), w, nil, now, fn); !errors.Is(err, deviceauth.ErrUntrusted) || calls != 1 {
		t.Fatal("pause admitted new operation", err)
	}
}

func TestTransientAdmissionRefusalDoesNotBecomeRejected(t *testing.T) {
	c := &conn{}
	keys := initializeTestSecurityKeys(t, c)
	pin, _, _ := c.security.endpoint.Snapshot()
	member := keys.states[0].Core.Members[0]
	lease, err := deviceauth.BuildLease(keys.states[1], keys.phone, keys.states[1].Core.IssuedAt)
	if err != nil {
		t.Fatal(err)
	}
	client, err := deviceauth.InitEndpoint("", pin, keys.states, []*deviceauth.RecoveryStatement{nil, nil}, member.ID, keys.phone.Seed(), keys.phoneX, lease)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	now := c.security.now()
	body, _ := proto.Marshal(&pb.Command{ProtocolVersion: protocol.Version, Kind: &pb.Command_UpdateSettings{UpdateSettings: &pb.UpdateSettingsRequest{}}})
	wire, err := client.Prepare(deviceauth.Header{Purpose: deviceauth.Command, Kind: 5, Machine: c.acct.DeviceID, Request: "transient-same-request", Deadline: now + 60000}, nil, body)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	fn := func(deviceauth.Header, []byte) (deviceauth.Completion, error) {
		calls++
		return deviceauth.Final([]byte("settled")), nil
	}
	c.security.setAuthorized(false)
	_, err = c.security.accept(context.Background(), wire, nil, now, fn)
	var decision *deviceauth.EntryDecision
	if !errors.As(err, &decision) || decision.Outcome != "unknown" || calls != 0 {
		t.Fatal(err)
	}
	c.security.setAuthorized(true)
	for range 2 {
		if _, err = c.security.accept(context.Background(), wire, nil, now, fn); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatal("late duplicate ran twice", calls)
	}
}

func TestMalformedCommandHasBoundProofOnlyAfterAuthentication(t *testing.T) {
	c := &conn{}
	keys := initializeTestSecurityKeys(t, c)
	pin, _, _ := c.security.endpoint.Snapshot()
	member := keys.states[0].Core.Members[0]
	lease, err := deviceauth.BuildLease(keys.states[1], keys.phone, keys.states[1].Core.IssuedAt)
	if err != nil {
		t.Fatal(err)
	}
	client, err := deviceauth.InitEndpoint("", pin, keys.states, []*deviceauth.RecoveryStatement{nil, nil}, member.ID, keys.phone.Seed(), keys.phoneX, lease)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	now := c.security.now()
	id := "malformed-command-request"
	wire, err := client.Prepare(deviceauth.Header{Purpose: deviceauth.Command, Kind: 5, Machine: c.acct.DeviceID, Request: id, Deadline: now + 60000}, nil, []byte{0xff})
	if err != nil {
		t.Fatal(err)
	}
	fn := func(deviceauth.Header, []byte) (deviceauth.Completion, error) {
		t.Fatal("malformed Command executed")
		return deviceauth.Undetermined(), nil
	}
	body, err := c.security.accept(context.Background(), wire, nil, now, fn)
	proof, ok := deviceauth.CommandResult(body, err)
	if !ok || proof.Outcome != "rejected" || proof.Reason != "invalid_body" || len(proof.PayloadHash) != 32 {
		t.Fatal("missing authenticated entry proof", err)
	}
	plain, _ := proto.Marshal(proof)
	env, err := c.security.sealResult(&pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_COMMAND_RESULT, MachineId: c.acct.DeviceID, RequestId: id}, nil, plain, false)
	if err != nil {
		t.Fatal(err)
	}
	result, _ := deviceauth.FromEnvelope(env)
	_, opened, err := client.Open(result, nil, false, now)
	if err != nil || !bytes.Equal(opened, plain) {
		t.Fatal("entry shell did not open", err)
	}
	wire.Origin[0] ^= 1
	body, err = c.security.accept(context.Background(), wire, nil, now, fn)
	if _, ok := deviceauth.CommandResult(body, err); ok {
		t.Fatal("unauthenticated ID received a rejection proof")
	}
}
