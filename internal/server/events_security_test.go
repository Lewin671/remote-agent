package server

import (
	"context"
	"crypto/rand"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"google.golang.org/protobuf/proto"
)

func TestEventPagesBoundBytesAndPreserveCursor(t *testing.T) {
	ts := newTestServer(t)
	_, mid, _ := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_MACHINE, "setup")
	cp, cid, _ := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "")
	ctx := context.Background()
	key := e2e.NewKey()
	env, _ := e2e.Seal(key, &pb.Header{Version: 2, Kind: pb.PayloadKind_PAYLOAD_KIND_SESSION_KEY, MachineId: mid, SessionId: "large-events"}, key[:])
	raw, _ := marshalEnv(env)
	if _, err := ts.tenant().putSession(ctx, mid, "large-events", raw, nil, 0, 0, nil); err != nil {
		t.Fatal(err)
	}
	const count = 24
	events := make([]*pb.StoredEvent, 0, count)
	// Random bytes: a payload that packs would not fill the pages.
	payload := make([]byte, 512<<10)
	rand.Read(payload)
	for seq := uint64(1); seq <= count; seq++ {
		envelope, err := e2e.Seal(key, &pb.Header{Version: 2, Kind: pb.PayloadKind_PAYLOAD_KIND_EVENT, MachineId: mid, SessionId: "large-events", Seq: seq}, payload)
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, &pb.StoredEvent{Seq: seq, Envelope: envelope})
	}
	if _, _, err := ts.tenant().appendEvents(ctx, mid, "large-events", events, nil); err != nil {
		t.Fatal(err)
	}
	client := remoteagentv1connect.NewClientServiceClient(http.DefaultClient, ts.url, withToken(ts.login(t, cp, cid)))
	var after uint64
	pages := 0
	for {
		res, err := client.GetEvents(ctx, connect.NewRequest(&pb.GetEventsRequest{SessionId: "large-events", AfterSeq: after, Limit: maxEventPage}))
		if err != nil {
			t.Fatal(err)
		}
		pages++
		if n := proto.Size(res.Msg); n > maxEventPageBytes {
			t.Fatalf("event page is %d bytes; maximum is %d", n, maxEventPageBytes)
		}
		if len(res.Msg.Events) == 0 {
			t.Fatal("empty page before the final cursor")
		}
		for _, event := range res.Msg.Events {
			if event.Seq != after+1 {
				t.Fatalf("cursor gap: %d after %d", event.Seq, after)
			}
			after = event.Seq
		}
		if !res.Msg.More {
			break
		}
	}
	if after != count || pages < 2 {
		t.Fatalf("paging lost events: cursor=%d pages=%d", after, pages)
	}
}
