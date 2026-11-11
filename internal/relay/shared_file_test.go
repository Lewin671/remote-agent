package relay_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/remoteclient"
	"google.golang.org/protobuf/proto"
)

const testBlock = 768 << 10

func (f *fakeLocal) GetSharedFileChunk(ctx context.Context, req *connect.Request[pb.GetSharedFileChunkRequest]) (*connect.Response[pb.GetSharedFileChunkResponse], error) {
	if o := agent.OriginFrom(ctx); o == nil || o.Kind != pb.OriginKind_ORIGIN_KIND_CLIENT || o.DeviceId == "" {
		return nil, connect.NewError(connect.CodePermissionDenied, nil)
	}
	data := make([]byte, testBlock)
	if req.Msg.FileId == "random" {
		_, _ = rand.Read(data)
	}
	return connect.NewResponse(&pb.GetSharedFileChunkResponse{Data: data, TotalBytes: proto.Uint64(10)}), nil
}

// A block crosses the relay as a session command, and its result is sealed
// without compression: a block of zeros and a random one look alike.
func TestSharedFileBlocksAreSealedWithoutCompression(t *testing.T) {
	e := setup(t)
	e.backend.add(t, "sess1")
	e.startRelay()
	s := e.session(t, "sess1", 0)
	e.online(t, true)
	ctx := context.Background()
	var sizes []int
	for _, id := range []string{"zeros", "random"} {
		cmd := &pb.Command{Kind: &pb.Command_GetSharedFileChunk{GetSharedFileChunk: &pb.GetSharedFileChunkRequest{SessionId: "sess1", FileId: id}}}
		env, err := e.client.Seal(s.MachineID, s.ID, remoteclient.NewRequestID(), time.Now().Add(time.Minute), cmd)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := e.client.API().Forward(ctx, connect.NewRequest(&pb.ForwardRequest{MachineId: s.MachineID, Command: env}))
		if err != nil {
			t.Fatal(err)
		}
		h := &pb.Header{}
		if err := proto.Unmarshal(raw.Msg.Result.Header, h); err != nil || h.Compression != pb.Compression_COMPRESSION_NONE {
			t.Fatalf("%s: compression %v %v", id, h.Compression, err)
		}
		sizes = append(sizes, len(raw.Msg.Result.Ciphertext))
		// Read-only: the same sealed request may be sent again and runs again.
		res, err := e.client.Forward(ctx, s.MachineID, env)
		if err != nil || len(res.GetGetSharedFileChunk().GetData()) != testBlock {
			t.Fatalf("%s: %v %v", id, res, err)
		}
		if id == "zeros" && !bytes.Equal(res.GetGetSharedFileChunk().Data, make([]byte, testBlock)) {
			t.Fatal("wrong block")
		}
	}
	if sizes[0] != sizes[1] {
		t.Fatalf("ciphertext lengths differ: %v", sizes)
	}
	// The header binds the command to its session.
	other := &pb.Command{Kind: &pb.Command_GetSharedFileChunk{GetSharedFileChunk: &pb.GetSharedFileChunkRequest{SessionId: "sess2", FileId: "zeros"}}}
	var ce *remoteclient.CommandError
	if _, err := e.client.Do(ctx, s, other); err == nil || !asCommandError(err, &ce) || ce.Code != "not_found" {
		t.Fatalf("mismatched session: %v", err)
	}
}

func asCommandError(err error, target **remoteclient.CommandError) bool {
	ce, ok := err.(*remoteclient.CommandError)
	if ok {
		*target = ce
	}
	return ok
}
