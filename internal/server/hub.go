package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"
)

// hub tracks stream connections: every connected client of an account
// receives the account's pushes, and each machine has at most one current
// connection, which carries forwarded commands. Requests reach it only
// through accountHub, which is bound to one account.
type hub struct {
	s *Server

	mu sync.Mutex
	// clients are the connected clients by account.
	clients map[string]map[*peer]bool
	// machines are keyed by machine ID, which is unique across accounts;
	// a lookup through accountHub also checks the account.
	machines map[string]*peer
	nextCall uint64
	calls    map[uint64]*call

	// changeMu puts reading whether a machine is online and broadcasting
	// it in one step, so that broadcasts leave in the order of the states
	// they report.
	changeMu sync.Mutex
}

type peer struct {
	dev *device
	out chan queuedFrame
	// queued is the bytes of the frames waiting in out or being written.
	queued atomic.Int64
	cancel context.CancelFunc
}

// queuedFrame is a frame waiting for a peer, and its serialized size.
type queuedFrame struct {
	frame *pb.ServerFrame
	size  int64
}

type call struct {
	peer  *peer
	reply chan *pb.ForwardReply // nil value: the machine went away
}

func newHub(s *Server) *hub {
	return &hub{s: s, clients: map[string]map[*peer]bool{}, machines: map[string]*peer{}, calls: map[uint64]*call{}}
}

// accountHub is the hub as one account sees it.
type accountHub struct {
	h    *hub
	acct string
}

func (h *hub) account(acct string) accountHub { return accountHub{h: h, acct: acct} }

// machineLocked returns the account's connected machine.
func (a accountHub) machineLocked(machineID string) *peer {
	p := a.h.machines[machineID]
	if p == nil || p.dev.account != a.acct {
		return nil
	}
	return p
}


func (a accountHub) online(machineID string) bool {
	a.h.mu.Lock()
	defer a.h.mu.Unlock()
	return a.machineLocked(machineID) != nil
}

// stampSeen makes the current time the last-seen time of the clients with
// a stream open: they are in use now, whenever they connected.
func (a accountHub) stampSeen(ds ...*pb.Device) {
	now := nowMs()
	a.h.mu.Lock()
	defer a.h.mu.Unlock()
	for _, d := range ds {
		for p := range a.h.clients[a.acct] {
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
	p := &peer{dev: dev, out: make(chan queuedFrame, sendQueue), cancel: cancel}
	// Ready must be the first frame: peers drop a stream that starts with
	// anything else. Queue it before the peer can be sent broadcasts,
	// forwarded commands or heartbeats.
	h.send(p, &pb.ServerFrame{Kind: &pb.ServerFrame_Ready{Ready: &pb.Ready{ServerTimeMs: nowMs()}}})
	h.add(p)
	defer h.remove(p)

	go h.ping(ctx, conn, p)
	go h.read(ctx, conn, p)
	for {
		select {
		case <-ctx.Done():
			conn.Close(websocket.StatusGoingAway, "")
			return
		case q := <-p.out:
			b, err := proto.Marshal(q.frame)
			if err != nil {
				slog.Error("marshal frame", "err", err)
				p.queued.Add(-q.size)
				continue
			}
			wctx, wcancel := context.WithTimeout(ctx, 30*time.Second)
			err = conn.Write(wctx, websocket.MessageBinary, b)
			wcancel()
			p.queued.Add(-q.size)
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
			// Browsers cannot observe control-frame Ping/Pong. Send a visible
			// heartbeat through the same bounded queue as the other frames.
			h.send(p, &pb.ServerFrame{Kind: &pb.ServerFrame_Heartbeat{Heartbeat: &pb.Heartbeat{}}})
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
		if h.clients[p.dev.account] == nil {
			h.clients[p.dev.account] = map[*peer]bool{}
		}
		h.clients[p.dev.account][p] = true
	}
	h.mu.Unlock()
	if old != nil {
		old.cancel()
	}
	if p.dev.kind == pb.DeviceKind_DEVICE_KIND_MACHINE {
		slog.Info("machine connected", "machine", p.dev.id)
		h.account(p.dev.account).machineChanged(p.dev.id)
	} else {
		_ = h.s.store.tenant(p.dev.account).touchDevice(context.Background(), p.dev.id)
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
		delete(h.clients[p.dev.account], p)
		if len(h.clients[p.dev.account]) == 0 {
			delete(h.clients, p.dev.account)
		}
	}
	h.mu.Unlock()
	if p.dev.kind != pb.DeviceKind_DEVICE_KIND_MACHINE {
		_ = h.s.store.tenant(p.dev.account).touchDevice(context.Background(), p.dev.id)
	}
	if gone {
		slog.Info("machine disconnected", "machine", p.dev.id)
		h.account(p.dev.account).machineChanged(p.dev.id)
	}
}

// send queues f for p, dropping the connection if p is too slow: too many
// frames are waiting for it, or too many bytes.
func (h *hub) send(p *peer, f *pb.ServerFrame) bool {
	q := queuedFrame{frame: f, size: int64(proto.Size(f))}
	if p.queued.Add(q.size) <= sendQueueBytes {
		select {
		case p.out <- q:
			return true
		default:
		}
	}
	p.queued.Add(-q.size)
	slog.Warn("stream peer too slow; disconnecting", "device", p.dev.id)
	p.cancel()
	return false
}

// broadcast pushes f to every client of the account, after the change is
// stored.
func (a accountHub) broadcast(f *pb.ServerFrame) {
	if ev := f.GetEventsAppended(); ev != nil {
		for _, part := range splitEvents(ev) {
			a.broadcastFrame(&pb.ServerFrame{Kind: &pb.ServerFrame_EventsAppended{EventsAppended: part}})
		}
		return
	}
	a.broadcastFrame(f)
}

func (a accountHub) broadcastFrame(f *pb.ServerFrame) {
	a.h.mu.Lock()
	clients := a.h.clients[a.acct]
	peers := make([]*peer, 0, len(clients))
	for p := range clients {
		peers = append(peers, p)
	}
	a.h.mu.Unlock()
	for _, p := range peers {
		a.h.send(p, f)
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

// machineChanged tells the clients about a machine as it is now. Every
// connect and disconnect is followed by a call, and calls do not overlap:
// the last broadcast is the machine's last state. Clients take the online
// flag as it comes, so a stale one would stay until the next change.
func (a accountHub) machineChanged(id string) {
	a.h.changeMu.Lock()
	defer a.h.changeMu.Unlock()
	st := a.h.s.store.tenant(a.acct)
	// The machine was seen just now whichever way it changed: it
	// connected, sent metadata, or was connected until this moment.
	_ = st.touchMachine(context.Background(), id)
	m, err := st.machine(context.Background(), id)
	if errors.Is(err, errNoRows) {
		return // removed meanwhile
	}
	if err != nil {
		slog.Error("load machine", "machine", id, "err", err)
		return
	}
	m.Online = a.online(id)
	a.broadcast(&pb.ServerFrame{Kind: &pb.ServerFrame_MachineUpdated{MachineUpdated: &pb.MachineUpdated{Machine: m}}})
}

// errUnknown is returned when a command reached the daemon, or may have,
// but no result came back. Retrying with the same request ID is safe.
func errUnknown(why string) error {
	return connect.NewError(connect.CodeDeadlineExceeded,
		errors.New(why+"; the command may or may not have run, retry with the same request ID"))
}

// forward sends a sealed command to a machine and waits for the result.
func (a accountHub) forward(ctx context.Context, machineID string, fc *pb.ForwardCall) (*pb.Envelope, error) {
	h := a.h
	h.mu.Lock()
	p := a.machineLocked(machineID)
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
func (a accountHub) drop(machineID string) {
	a.h.mu.Lock()
	p := a.machineLocked(machineID)
	a.h.mu.Unlock()
	if p != nil {
		p.cancel()
	}
}
