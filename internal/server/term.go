package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"
)

// Terminal streams: a client and a daemon each connect to /v1/term with the
// same stream ID and the server joins them. Frames are sealed; the server
// passes them along without storing them. A side that cannot keep up ends
// the stream, and the client reconnects for a fresh snapshot.
const (
	termFrameBytes = 512 << 10
	termJoinWait   = 20 * time.Second
	termWriteWait  = 10 * time.Second
)

type termEnd struct {
	conn    *websocket.Conn
	dev     *device
	machine string // for clients: the machine that must join
	joined  chan *termEnd
	ctx     context.Context
	cancel  context.CancelFunc
}

// A waiting side does not read until joined, so frames it is sent early
// stay in its connection's buffers.

type termHub struct {
	s       *Server
	mu      sync.Mutex
	waiting map[string]*termEnd
}

func newTermHub(s *Server) *termHub { return &termHub{s: s, waiting: map[string]*termEnd{}} }

func (t *termHub) serve(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: t.s.originPatterns()})
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(termFrameBytes)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	hctx, hcancel := context.WithTimeout(ctx, 10*time.Second)
	typ, b, err := conn.Read(hctx)
	hcancel()
	hello := &pb.TermHello{}
	if err != nil || typ != websocket.MessageBinary || proto.Unmarshal(b, hello) != nil || len(hello.StreamId) < 16 {
		conn.Close(websocket.StatusPolicyViolation, "bad hello")
		return
	}
	dev, err := t.s.authenticate(ctx, hello.Token)
	if err != nil {
		conn.Close(websocket.StatusPolicyViolation, "authentication failed")
		return
	}
	me := &termEnd{conn: conn, dev: dev, machine: hello.MachineId, joined: make(chan *termEnd, 1), ctx: ctx, cancel: cancel}
	if dev.kind == pb.DeviceKind_DEVICE_KIND_CLIENT && hello.MachineId == "" {
		conn.Close(websocket.StatusPolicyViolation, "no machine")
		return
	}

	peer, err := t.join(hello.StreamId, me)
	if err != nil {
		conn.Close(websocket.StatusPolicyViolation, err.Error())
		return
	}
	if peer == nil {
		// Wait for the other side.
		select {
		case peer = <-me.joined:
		case <-time.After(termJoinWait):
			t.leave(hello.StreamId, me)
			conn.Close(websocket.StatusTryAgainLater, "the other side did not join")
			return
		case <-ctx.Done():
			t.leave(hello.StreamId, me)
			return
		}
	}
	slog.Info("terminal stream joined", "stream", hello.StreamId[:8])
	// Each side pumps what it reads to the other; either ending ends both.
	defer peer.cancel()
	t.pump(me, peer)
}

// join pairs me with the side already waiting, or registers me as waiting
// and returns nil.
func (t *termHub) join(id string, me *termEnd) (*termEnd, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	other, ok := t.waiting[id]
	if !ok {
		t.waiting[id] = me
		return nil, nil
	}
	client, machine := me, other
	if me.dev.kind == pb.DeviceKind_DEVICE_KIND_MACHINE {
		client, machine = other, me
	}
	if client.dev.kind != pb.DeviceKind_DEVICE_KIND_CLIENT || machine.dev.kind != pb.DeviceKind_DEVICE_KIND_MACHINE || client.machine != machine.dev.id {
		return nil, errors.New("stream belongs to someone else")
	}
	delete(t.waiting, id)
	other.joined <- me
	return other, nil
}

func (t *termHub) leave(id string, me *termEnd) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.waiting[id] == me {
		delete(t.waiting, id)
	}
}

// pump copies messages from me to peer until either side ends.
func (t *termHub) pump(me, peer *termEnd) {
	for {
		typ, b, err := me.conn.Read(me.ctx)
		if err != nil || typ != websocket.MessageBinary {
			peer.conn.Close(websocket.StatusNormalClosure, "the other side left")
			return
		}
		wctx, cancel := context.WithTimeout(peer.ctx, termWriteWait)
		err = peer.conn.Write(wctx, websocket.MessageBinary, b)
		cancel()
		if err != nil {
			me.conn.Close(websocket.StatusTryAgainLater, "the other side is too slow")
			return
		}
	}
}
