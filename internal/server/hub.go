package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"
)

// hub tracks stream connections: every connected client receives the
// pushes, and each machine has at most one current connection, which
// carries forwarded commands.
type hub struct {
	s *Server

	mu       sync.Mutex
	clients  map[*peer]bool
	machines map[string]*peer
	nextCall uint64
	calls    map[uint64]*call
}

type peer struct {
	dev    *device
	out    chan *pb.ServerFrame
	cancel context.CancelFunc
}

type call struct {
	peer  *peer
	reply chan *pb.ForwardReply // nil value: the machine went away
}

func newHub(s *Server) *hub {
	return &hub{s: s, clients: map[*peer]bool{}, machines: map[string]*peer{}, calls: map[uint64]*call{}}
}

func (h *hub) online(machineID string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.machines[machineID] != nil
}

// stampSeen makes the current time the last-seen time of the clients with
// a stream open: they are in use now, whenever they connected.
func (h *hub) stampSeen(ds ...*pb.Device) {
	now := nowMs()
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, d := range ds {
		for p := range h.clients {
			if p.dev.id == d.Id {
				d.LastSeenMs = now
				break
			}
		}
	}
}

// serve handles one stream connection.
func (h *hub) serve(w http.ResponseWriter, r *http.Request) {
	releaseHello, ok := h.s.acquireStreamHello(w)
	if !ok {
		return
	}
	defer releaseHello()
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: h.s.originPatterns()})
	if err != nil {
		// Accept has written the response.
		slog.Info("stream upgrade refused", "proto", r.Proto, "method", r.Method, "err", err)
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(maxStreamHelloBytes)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	token, err := h.hello(ctx, conn)
	var dev *device
	if err == nil {
		var release context.CancelFunc
		ctx, dev, release, err = h.s.beginStream(ctx, token)
		if release != nil {
			defer release()
		}
	}
	releaseHello()
	if err != nil {
		slog.Info("stream hello refused", "err", err)
		conn.Close(websocket.StatusPolicyViolation, "authentication failed")
		return
	}
	conn.SetReadLimit(maxFrameBytes)
	p := &peer{dev: dev, out: make(chan *pb.ServerFrame, sendQueue), cancel: cancel}
	h.add(p)
	defer h.remove(p)

	go h.ping(ctx, conn, p)
	go h.read(ctx, conn, p)
	h.send(p, &pb.ServerFrame{Kind: &pb.ServerFrame_Ready{Ready: &pb.Ready{ServerTimeMs: nowMs()}}})
	for {
		select {
		case <-ctx.Done():
			conn.Close(websocket.StatusGoingAway, "")
			return
		case f := <-p.out:
			b, err := proto.Marshal(f)
			if err != nil {
				slog.Error("marshal frame", "err", err)
				continue
			}
			wctx, wcancel := context.WithTimeout(ctx, 30*time.Second)
			err = conn.Write(wctx, websocket.MessageBinary, b)
			wcancel()
			if err != nil {
				return
			}
		}
	}
}

// hello reads the first frame and authenticates it.
func (h *hub) hello(ctx context.Context, conn *websocket.Conn) (string, error) {
	hctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	typ, b, err := conn.Read(hctx)
	if err != nil {
		return "", err
	}
	f := &pb.PeerFrame{}
	if typ != websocket.MessageBinary || proto.Unmarshal(b, f) != nil || f.GetHello() == nil {
		return "", errors.New("expected hello")
	}
	return f.GetHello().Token, nil
}

func (h *hub) ping(ctx context.Context, conn *websocket.Conn, p *peer) {
	t := time.NewTicker(h.s.cfg.PingInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, h.s.cfg.PingInterval)
			err := conn.Ping(pctx)
			cancel()
			if err != nil {
				p.cancel()
				return
			}
		}
	}
}

func (h *hub) read(ctx context.Context, conn *websocket.Conn, p *peer) {
	defer p.cancel()
	for {
		typ, b, err := conn.Read(ctx)
		if err != nil {
			return
		}
		f := &pb.PeerFrame{}
		if typ != websocket.MessageBinary || proto.Unmarshal(b, f) != nil {
			return
		}
		if r := f.GetForwardReply(); r != nil && p.dev.kind == pb.DeviceKind_DEVICE_KIND_MACHINE {
			h.mu.Lock()
			c := h.calls[r.CallId]
			if c != nil && c.peer == p {
				delete(h.calls, r.CallId)
			} else {
				c = nil
			}
			h.mu.Unlock()
			if c != nil {
				c.reply <- r
			}
		}
	}
}

func (h *hub) add(p *peer) {
	h.mu.Lock()
	var old *peer
	if p.dev.kind == pb.DeviceKind_DEVICE_KIND_MACHINE {
		old = h.machines[p.dev.id]
		h.machines[p.dev.id] = p
	} else {
		h.clients[p] = true
	}
	h.mu.Unlock()
	if old != nil {
		old.cancel()
	}
	if p.dev.kind == pb.DeviceKind_DEVICE_KIND_MACHINE {
		slog.Info("machine connected", "machine", p.dev.id)
		h.machineChanged(p.dev.id)
	} else {
		_ = h.s.store.touchDevice(context.Background(), p.dev.id)
	}
}

func (h *hub) remove(p *peer) {
	h.mu.Lock()
	gone := false
	if p.dev.kind == pb.DeviceKind_DEVICE_KIND_MACHINE {
		if h.machines[p.dev.id] == p {
			delete(h.machines, p.dev.id)
			gone = true
		}
		for id, c := range h.calls {
			if c.peer == p {
				delete(h.calls, id)
				c.reply <- nil
			}
		}
	} else {
		delete(h.clients, p)
	}
	h.mu.Unlock()
	if p.dev.kind != pb.DeviceKind_DEVICE_KIND_MACHINE {
		_ = h.s.store.touchDevice(context.Background(), p.dev.id)
	}
	if gone {
		slog.Info("machine disconnected", "machine", p.dev.id)
		h.machineChanged(p.dev.id)
	}
}

// send queues f for p, dropping the connection if p is too slow.
func (h *hub) send(p *peer, f *pb.ServerFrame) bool {
	select {
	case p.out <- f:
		return true
	default:
		slog.Warn("stream peer too slow; disconnecting", "device", p.dev.id)
		p.cancel()
		return false
	}
}

// broadcast pushes f to every client, after the change is stored.
func (h *hub) broadcast(f *pb.ServerFrame) {
	if ev := f.GetEventsAppended(); ev != nil {
		for _, part := range splitEvents(ev) {
			h.broadcastFrame(&pb.ServerFrame{Kind: &pb.ServerFrame_EventsAppended{EventsAppended: part}})
		}
		return
	}
	h.broadcastFrame(f)
}

func (h *hub) broadcastFrame(f *pb.ServerFrame) {
	h.mu.Lock()
	peers := make([]*peer, 0, len(h.clients))
	for p := range h.clients {
		peers = append(peers, p)
	}
	h.mu.Unlock()
	for _, p := range peers {
		h.send(p, f)
	}
}

// splitEvents keeps pushed frames below the frame limit.
func splitEvents(ev *pb.EventsAppended) []*pb.EventsAppended {
	var out []*pb.EventsAppended
	cur := &pb.EventsAppended{MachineId: ev.MachineId, SessionId: ev.SessionId}
	size := 0
	for _, e := range ev.Events {
		n := proto.Size(e)
		if len(cur.Events) > 0 && size+n > maxFrameBytes/2 {
			out = append(out, cur)
			cur = &pb.EventsAppended{MachineId: ev.MachineId, SessionId: ev.SessionId}
			size = 0
		}
		cur.Events = append(cur.Events, e)
		size += n
	}
	if len(cur.Events) > 0 {
		out = append(out, cur)
	}
	return out
}

func (h *hub) machineChanged(id string) {
	m, err := h.s.store.machine(context.Background(), id)
	if err != nil {
		slog.Error("load machine", "machine", id, "err", err)
		return
	}
	m.Online = h.online(id)
	if m.Online {
		_ = h.s.store.touchMachine(context.Background(), id)
	}
	h.broadcast(&pb.ServerFrame{Kind: &pb.ServerFrame_MachineUpdated{MachineUpdated: &pb.MachineUpdated{Machine: m}}})
}

// errUnknown is returned when a command reached the daemon, or may have,
// but no result came back. Retrying with the same request ID is safe.
func errUnknown(why string) error {
	return connect.NewError(connect.CodeDeadlineExceeded,
		errors.New(why+"; the command may or may not have run, retry with the same request ID"))
}

// forward sends a sealed command to a machine and waits for the result.
func (h *hub) forward(ctx context.Context, machineID string, fc *pb.ForwardCall) (*pb.Envelope, error) {
	h.mu.Lock()
	p := h.machines[machineID]
	if p == nil {
		h.mu.Unlock()
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("machine is offline"))
	}
	h.nextCall++
	id := h.nextCall
	c := &call{peer: p, reply: make(chan *pb.ForwardReply, 1)}
	h.calls[id] = c
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.calls, id)
		h.mu.Unlock()
	}()
	fc = proto.CloneOf(fc)
	fc.CallId = id
	if !h.send(p, &pb.ServerFrame{Kind: &pb.ServerFrame_ForwardCall{ForwardCall: fc}}) {
		return nil, connect.NewError(connect.CodeUnavailable, errors.New("machine is not keeping up"))
	}
	timer := time.NewTimer(h.s.cfg.ForwardTimeout)
	defer timer.Stop()
	select {
	case r := <-c.reply:
		switch {
		case r == nil:
			return nil, errUnknown("the machine disconnected")
		case r.Error != "":
			return nil, connect.NewError(connect.CodeAborted, errors.New("machine: "+strings.TrimSpace(r.Error)))
		case r.Result == nil:
			return nil, errUnknown("the machine sent no result")
		}
		if _, err := checkEnvelope(r.Result, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_COMMAND_RESULT, MachineId: machineID}); err != nil {
			return nil, errUnknown("the machine sent a malformed result")
		}
		return r.Result, nil
	case <-timer.C:
		return nil, errUnknown("no reply from the machine in time")
	case <-ctx.Done():
		return nil, errUnknown("request canceled")
	}
}

// drop disconnects a machine, e.g. after it was removed.
func (h *hub) drop(machineID string) {
	h.mu.Lock()
	p := h.machines[machineID]
	h.mu.Unlock()
	if p != nil {
		p.cancel()
	}
}
