package remoteclient

import (
	"bytes"
	"connectrpc.com/connect"
	"context"
	"encoding/base64"
	"encoding/json"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"google.golang.org/protobuf/proto"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type payloadServer struct {
	remoteagentv1connect.UnimplementedClientServiceHandler
	endpoint *deviceauth.Endpoint
	received [][]byte
}

func (s *payloadServer) Forward(_ context.Context, r *connect.Request[pb.ForwardRequest]) (*connect.Response[pb.ForwardResponse], error) {
	w, err := deviceauth.FromEnvelope(r.Msg.Command)
	if err != nil {
		return nil, err
	}
	h, p, err := s.endpoint.Open(w, nil, false, uint64(time.Now().UnixMilli()))
	if err != nil {
		return nil, err
	}
	plain, err := deviceauth.Unpack(h, p)
	if err != nil {
		return nil, err
	}
	s.received = append(s.received, bytes.Clone(plain))
	key, payload, _ := deviceauth.RequestIdentity(h, p)
	body, _ := proto.Marshal(&pb.CommandResult{Outcome: "executed", Reason: "completed", ResultAvailable: true, RequestKeyHash: key[:], PayloadHash: payload[:], Kind: &pb.CommandResult_CreateSession{CreateSession: &pb.CreateSessionResponse{Session: &pb.Session{Id: "created"}}}})
	result, err := s.endpoint.Prepare(deviceauth.Header{Kind: 6, Purpose: deviceauth.Result, Machine: h.Machine, Session: h.Session, Request: h.Request}, nil, body)
	if err != nil {
		return nil, err
	}
	env, _ := result.Envelope()
	return connect.NewResponse(&pb.ForwardResponse{Result: env, Outcome: "executed", Reason: "completed", AttemptId: r.Msg.AttemptId}), nil
}
func TestCreateSessionRetryRetainsOriginalPayloadBytes(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/transport/prepared-command.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct{ Plain string }
	json.Unmarshal(raw, &f)
	plain, _ := base64.StdEncoding.DecodeString(f.Plain)
	cmd := &pb.Command{}
	if err = proto.Unmarshal(plain, cmd); err != nil {
		t.Fatal(err)
	}
	encoded, _ := proto.MarshalOptions{Deterministic: true}.Marshal(cmd)
	if bytes.Equal(encoded, plain) || len(cmd.GetCreateSession().GetEnv()) != 3 {
		t.Fatal("fixture must expose re-encoding")
	}
	client, machine, mid := syntheticTerminalSecurity(t)
	h := deviceauth.Header{Kind: 5, Purpose: deviceauth.Command, Machine: mid, Request: NewRequestID(), Deadline: uint64(time.Now().Add(time.Minute).UnixMilli())}
	sealed, err := client.endpoint.Prepare(h, nil, plain)
	if err != nil {
		t.Fatal(err)
	}
	env, _ := sealed.Envelope()
	handler := &payloadServer{endpoint: machine}
	_, service := remoteagentv1connect.NewClientServiceHandler(handler)
	server := httptest.NewServer(testEpochHandler(service))
	defer server.Close()
	client.api = remoteagentv1connect.NewClientServiceClient(server.Client(), server.URL)
	for range 2 {
		if _, err = client.Forward(context.Background(), mid, env); err != nil {
			t.Fatal(err)
		}
	}
	// Persist the already prepared envelope, never the decoded Command.
	saved, _ := proto.Marshal(env)
	restored := &pb.Envelope{}
	proto.Unmarshal(saved, restored)
	path := filepath.Join(t.TempDir(), "client.db")
	if err = client.endpoint.Save(path); err != nil {
		t.Fatal(err)
	}
	client.endpoint.Close()
	endpoint, err := deviceauth.OpenEndpoint(path)
	if err != nil {
		t.Fatal(err)
	}
	defer endpoint.Close()
	restarted := NewClient(server.URL, "fixture", client.DeviceID, endpoint)
	fresh, err := restarted.Reseal(restored)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(fresh.Nonce, restored.Nonce) {
		t.Fatal("did not re-encrypt")
	}
	if _, err = restarted.Forward(context.Background(), mid, fresh); err != nil {
		t.Fatal(err)
	}
	if len(handler.received) != 3 {
		t.Fatal(len(handler.received))
	}
	for _, got := range handler.received {
		if !bytes.Equal(got, plain) {
			t.Fatal("retry changed the authorized bytes")
		}
	}
}
