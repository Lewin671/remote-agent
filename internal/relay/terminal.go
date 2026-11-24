package relay

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/transport"
	"github.com/Lewin671/remote-agent/shared/devicesec"
	"github.com/Lewin671/remote-agent/shared/linkcore"
	"github.com/Lewin671/remote-agent/shared/peerwire"
	"google.golang.org/protobuf/proto"
)

// Terminal is a live view of a session's terminal; see daemon.TermStream.
type Terminal interface {
	Snapshot() (*pb.TerminalSnapshot, error)
	History(*pb.TerminalHistoryRequest) (*pb.TerminalHistoryPage, error)
	Output() <-chan []byte
	SizeChanged() <-chan struct{}
	Done() <-chan struct{}
	Reason() string
	Input(b []byte) error
	Resize(cols, rows int, fit bool) error
	Close() error
}

const (
	// termBatch bounds how much output goes into one frame.
	termBatch = 64 << 10
	// termInputChunk bounds each tmux injection slice so partial failures report exact byte bounds.
	termInputChunk = 256
	// termMaxSnapshots bounds frozen InputStatus query snapshots retained for pagination.
	termMaxSnapshots = 64
	// termSnapshotTTLMs bounds how long a frozen InputStatus query snapshot is retained.
	termSnapshotTTLMs = 30_000
	// termMaxActiveStreams / termMaxRetiredStreams match docs/transport.md §5.
	termMaxActiveStreams  = 16
	termMaxRetiredStreams = 16
	// termZeroLegTimeout bounds how long a terminal stream with zero attached legs
	// (both relay and direct closed) waits for AttachPath before automatic GC (M2).
	termZeroLegTimeout = 30 * time.Second
	// termPendingDirectTTL / termMaxPendingDirect bound early direct AttachPath
	// arrivals before openTerminal finishes creating the stream (H-B).
	termPendingDirectTTL = 5 * time.Second
	termMaxPendingDirect = 16
)

type frozenStatusCache struct {
	stream    string
	peer      string
	snapshot  linkcore.TermStatusSnapshot
	expiresAt uint64
}

type pendingDirectAttach struct {
	peerID    string
	sessionID string
	legID     string
	gen       uint64
	expiresAt time.Time
}

type terminalHub struct {
	mu            sync.Mutex
	registry      *linkcore.TerminalRegistry
	dedup         *linkcore.CipherDedup
	live          map[string]*liveTerminal
	pendingDirect map[string]pendingDirectAttach
	snapshots     map[string]frozenStatusCache
	instance      string
	nowFn         func() time.Time
	zeroLegTTL    time.Duration
}

func (h *terminalHub) now() time.Time {
	if h != nil && h.nowFn != nil {
		return h.nowFn()
	}
	return time.Now()
}

func (h *terminalHub) zeroLegTimeout() time.Duration {
	if h != nil && h.zeroLegTTL > 0 {
		return h.zeroLegTTL
	}
	return termZeroLegTimeout
}

func (c *endpoint) terminalHubOwner() *terminalHub {
	c.termHubOnce.Do(func() {
		inst := c.acct.DeviceID
		if dm := c.direct.Load(); dm != nil && dm.self != nil && len(dm.self.Instance) > 0 {
			inst = hex.EncodeToString(dm.self.Instance)
		}
		c.termHub = &terminalHub{
			registry:      linkcore.NewTerminalRegistry(termMaxActiveStreams, termMaxRetiredStreams),
			dedup:         linkcore.NewCipherDedup(),
			live:          map[string]*liveTerminal{},
			pendingDirect: map[string]pendingDirectAttach{},
			snapshots:     map[string]frozenStatusCache{},
			instance:      inst,
			nowFn:         c.r.termClock,
			zeroLegTTL:    c.r.termZeroLegTTL,
		}
		c.r.termHubRef.Store(c.termHub)
	})
	return c.termHub
}

func (h *terminalHub) storePendingDirect(streamID string, pd pendingDirectAttach, now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for k, v := range h.pendingDirect {
		if !now.Before(v.expiresAt) {
			delete(h.pendingDirect, k)
		}
	}
	if _, exists := h.pendingDirect[streamID]; !exists && len(h.pendingDirect) >= termMaxPendingDirect {
		return false
	}
	h.pendingDirect[streamID] = pd
	return true
}

func (h *terminalHub) takePendingDirectLocked(streamID, peerID, sessionID string, now time.Time) (pendingDirectAttach, bool) {
	pd, ok := h.pendingDirect[streamID]
	if !ok {
		return pendingDirectAttach{}, false
	}
	if !now.Before(pd.expiresAt) {
		delete(h.pendingDirect, streamID)
		return pendingDirectAttach{}, false
	}
	if pd.peerID != peerID || pd.sessionID != sessionID {
		return pendingDirectAttach{}, false
	}
	delete(h.pendingDirect, streamID)
	return pd, true
}

func (h *terminalHub) getLive(streamID string) *liveTerminal {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.live[streamID]
}

func (h *terminalHub) getQueryable(streamID string, now uint64) (*liveTerminal, *linkcore.TerminalStream) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.registry.Prune(now)
	return h.live[streamID], h.registry.GetQueryable(streamID)
}

func (h *terminalHub) storeSnapshot(key, stream, peer string, snap linkcore.TermStatusSnapshot, now uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for k, v := range h.snapshots {
		if now >= v.expiresAt {
			delete(h.snapshots, k)
		}
	}
	if len(h.snapshots) >= termMaxSnapshots {
		for k := range h.snapshots {
			delete(h.snapshots, k)
			break
		}
	}
	h.snapshots[key] = frozenStatusCache{
		stream:    stream,
		peer:      peer,
		snapshot:  snap,
		expiresAt: now + termSnapshotTTLMs,
	}
}

func (h *terminalHub) loadSnapshot(key, stream, peer string, now uint64) (linkcore.TermStatusSnapshot, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	v, ok := h.snapshots[key]
	if !ok || now >= v.expiresAt || v.stream != stream || v.peer != peer {
		if ok && now >= v.expiresAt {
			delete(h.snapshots, key)
		}
		return linkcore.TermStatusSnapshot{}, false
	}
	return v.snapshot, true
}

type inboundTermFrame struct {
	raw        []byte
	fromDirect bool
}

type liveTerminal struct {
	ep        *endpoint
	hub       *terminalHub
	stream    *linkcore.TerminalStream
	t         Terminal
	key       []byte
	sessionID string
	streamID  string
	ownerPeer string

	ctx    context.Context
	cancel context.CancelFunc

	mu                sync.Mutex
	relayPath         transport.Path
	relayLegID        string
	relayGen          uint64
	relayAttachGen    uint64
	relayDialInFlight int
	relayCancel       context.CancelFunc
	directGen         uint64
	zeroLegSince      time.Time
	outSeq            uint64

	inbound         chan inboundTermFrame
	historyRequests chan *pb.TerminalHistoryRequest
	historyPages    chan *pb.TerminalHistoryPage
	closed          atomic.Bool
}

// openTerminal joins the stream a client asked for and serves it in the
// background until either side leaves.
func (c *endpoint) openTerminal(ctx context.Context, sessionID, streamID string) error {
	if len(streamID) < minRequestID {
		return errors.New("invalid stream ID")
	}
	lifetime := c.lifetime
	if lifetime == nil {
		lifetime = ctx
	}
	peer := agent.OriginFrom(ctx).GetDeviceId()
	streamCtx, streamCancel, err := c.security.streamContext(lifetime, peer)
	if err != nil {
		return err
	}
	established := false
	defer func() {
		if !established {
			streamCancel()
		}
	}()
	opening, cancelOpening := context.WithCancel(ctx)
	defer cancelOpening()
	stopOpening := context.AfterFunc(streamCtx, cancelOpening)
	defer stopOpening()
	ctx = opening
	key, err := c.sessionKey(sessionID, true)
	if err != nil {
		return err
	}
	t, err := c.r.backend.OpenTerminal(sessionID)
	if err != nil {
		return err
	}
	if err := c.security.confirmStream(streamCtx); err != nil {
		t.Close()
		return err
	}
	path, err := c.link.Router.OpenStream(ctx, transport.StreamRoute{ID: streamID})
	if err != nil {
		t.Close()
		return err
	}
	if err := c.security.confirmStream(streamCtx); err != nil {
		t.Close()
		path.Close()
		return err
	}

	hub := c.terminalHubOwner()
	nowMs := uint64(hub.now().UnixMilli())
	ts := linkcore.NewTerminalStream(streamID, sessionID, peer, hub.instance, nowMs)
	_ = ts.AttachLeg("relay", streamID, hub.instance, 1)

	ltCtx, ltCancel := context.WithCancel(streamCtx)
	lt := &liveTerminal{
		ep:              c,
		hub:             hub,
		stream:          ts,
		t:               t,
		key:             key,
		sessionID:       sessionID,
		streamID:        streamID,
		ownerPeer:       peer,
		ctx:             ltCtx,
		cancel:          ltCancel,
		relayPath:       path,
		relayLegID:      streamID,
		relayGen:        1,
		relayAttachGen:  1,
		inbound:         make(chan inboundTermFrame, 128),
		historyRequests: make(chan *pb.TerminalHistoryRequest, 1),
		historyPages:    make(chan *pb.TerminalHistoryPage, 1),
	}

	hub.mu.Lock()
	hub.registry.Prune(nowMs)
	if err := hub.registry.Add(ts); err != nil {
		hub.mu.Unlock()
		ltCancel()
		t.Close()
		path.Close()
		return err
	}
	hub.live[streamID] = lt
	if pd, ok := hub.takePendingDirectLocked(streamID, peer, sessionID, hub.now()); ok {
		lt.directGen = pd.gen
		_ = lt.stream.AttachLeg("direct", pd.legID, hub.instance, pd.gen)
		lt.noteLegStateLocked(hub.now())
	}
	hub.mu.Unlock()

	if !c.link.ServeStream(func() {
		defer streamCancel()
		lt.serve()
	}) {
		hub.mu.Lock()
		delete(hub.live, streamID)
		hub.registry.ForceRetire(streamID, uint64(hub.now().UnixMilli()))
		hub.mu.Unlock()
		ltCancel()
		t.Close()
		path.Close()
		return context.Canceled
	}
	established = true
	return nil
}

func (lt *liveTerminal) serve() {
	var workers sync.WaitGroup
	defer func() {
		lt.closed.Store(true)
		lt.cancel()
		lt.mu.Lock()
		rp := lt.relayPath
		rc := lt.relayCancel
		lt.relayPath = nil
		lt.relayCancel = nil
		lt.mu.Unlock()
		if rc != nil {
			rc()
		}
		if rp != nil {
			rp.Close()
		}
		workers.Wait()
		lt.t.Close()
		nowMs := uint64(lt.hub.now().UnixMilli())
		lt.hub.mu.Lock()
		if lt.hub.live[lt.streamID] == lt {
			delete(lt.hub.live, lt.streamID)
		}
		lt.hub.registry.ForceRetire(lt.streamID, nowMs)
		lt.hub.mu.Unlock()
	}()

	snap, err := lt.t.Snapshot()
	if err != nil {
		_ = lt.sendOutput(&pb.TerminalFrame{Kind: &pb.TerminalFrame_Closed{Closed: "cannot read the screen"}})
		return
	}
	if err := lt.sendOutput(&pb.TerminalFrame{Kind: &pb.TerminalFrame_Snapshot{Snapshot: snap}}); err != nil {
		return
	}

	workers.Go(func() {
		for {
			select {
			case <-lt.ctx.Done():
				return
			case req := <-lt.historyRequests:
				page, err := lt.t.History(req)
				if err != nil {
					page = &pb.TerminalHistoryPage{RequestId: req.RequestId, Error: "cannot read terminal history"}
				}
				select {
				case lt.historyPages <- page:
				case <-lt.ctx.Done():
					return
				}
			}
		}
	})

	workers.Go(func() {
		lt.inputLoop()
	})

	// Start reading the initial relay leg.
	lt.mu.Lock()
	initialPath := lt.relayPath
	initialGen := lt.relayGen
	legCtx, legCancel := context.WithCancel(lt.ctx)
	lt.relayCancel = legCancel
	lt.mu.Unlock()
	workers.Go(func() {
		lt.readRelayLeg(legCtx, initialPath, initialGen)
	})

	ackTicker := time.NewTicker(time.Duration(linkcore.TermAckIntervalMs) * time.Millisecond)
	defer ackTicker.Stop()

	for {
		select {
		case <-lt.ctx.Done():
			return
		case <-ackTicker.C:
			nowT := lt.hub.now()
			lt.mu.Lock()
			emitACK := lt.stream.Advance(uint64(nowT.UnixMilli()))
			var wm linkcore.TermWatermark
			if emitACK {
				wm = lt.stream.CurrentInputWatermark()
			}
			zeroLegExpired := !lt.zeroLegSince.IsZero() && nowT.Sub(lt.zeroLegSince) >= lt.hub.zeroLegTimeout()
			lt.mu.Unlock()
			if emitACK {
				lt.sendControlAck(wm)
			}
			if zeroLegExpired {
				return
			}
		case <-lt.t.Done():
			_ = lt.sendOutput(&pb.TerminalFrame{Kind: &pb.TerminalFrame_Closed{Closed: lt.t.Reason()}})
			lt.mu.Lock()
			rp := lt.relayPath
			lt.mu.Unlock()
			if rp != nil {
				rp.Finish()
			}
			return
		case page := <-lt.historyPages:
			if err := lt.sendOutput(&pb.TerminalFrame{Kind: &pb.TerminalFrame_HistoryPage{HistoryPage: page}}); err != nil {
				return
			}
		case <-lt.t.SizeChanged():
			snap, err := lt.t.Snapshot()
			if err != nil {
				return
			}
			if err := lt.sendOutput(&pb.TerminalFrame{Kind: &pb.TerminalFrame_Snapshot{Snapshot: snap}}); err != nil {
				return
			}
		case b := <-lt.t.Output():
			// Send what has piled up as one frame.
			for more := true; more && len(b) < termBatch; {
				select {
				case next := <-lt.t.Output():
					b = append(b, next...)
				default:
					more = false
				}
			}
			if err := lt.sendOutput(&pb.TerminalFrame{Kind: &pb.TerminalFrame_Output{Output: b}}); err != nil {
				return
			}
		}
	}
}

func (lt *liveTerminal) sendOutput(f *pb.TerminalFrame) error {
	if err := lt.ep.security.confirmStream(lt.ctx); err != nil {
		return err
	}
	nowTime := time.Now()
	nowMs := uint64(nowTime.UnixMilli())

	lt.mu.Lock()
	lt.outSeq++
	seq := lt.outSeq
	rp := lt.relayPath
	lt.mu.Unlock()

	env, err := lt.ep.security.sealMessage(&pb.Header{
		Kind:      pb.PayloadKind_PAYLOAD_KIND_TERMINAL_OUTPUT,
		MachineId: lt.ep.acct.DeviceID,
		SessionId: lt.sessionID,
		RequestId: lt.streamID,
		Seq:       seq,
		Sender:    lt.ep.acct.DeviceID,
	}, devicesec.TerminalOutput, lt.key, f)
	if err != nil {
		return err
	}
	raw, err := proto.Marshal(env)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)

	lt.mu.Lock()
	lt.stream.RecordOutputReplay(nowMs, linkcore.TermFrame{
		Seq:         seq,
		PayloadHash: sum,
		RawBytes:    len(raw),
		RawEnvelope: raw,
	})
	hasDirectLeg := lt.stream.HasLeg("direct")
	mirror := lt.stream.ShouldMirrorOutput(int64(nowMs), len(raw))
	lt.mu.Unlock()

	var dp *directPeer
	if dm := lt.ep.direct.Load(); dm != nil && hasDirectLeg {
		if activePeer, ok := dm.activePeerFor(lt.ownerPeer); ok && activePeer.peerTerminalDirect.Load() {
			dp = activePeer
		}
	}

	sentDirect := false
	if dp != nil {
		sentDirect = dp.enqueueTerm(raw, nowTime.Add(10*time.Second))
	}
	if sentDirect && !mirror {
		return nil
	}
	if rp != nil {
		wctx, wcancel := context.WithTimeout(lt.ctx, 10*time.Second)
		werr := rp.Write(wctx, raw)
		wcancel()
		if werr == nil {
			return nil
		}
		if sentDirect {
			// Direct leg delivered; relay leg failed — let readRelayLeg/AttachPath handle relay recovery.
			return nil
		}
		return werr
	}
	if sentDirect {
		return nil
	}
	return errors.New("terminal output path unavailable")
}

func (lt *liveTerminal) noteLegStateLocked(now time.Time) {
	if lt.relayPath == nil && !lt.stream.HasLeg("direct") {
		if lt.zeroLegSince.IsZero() {
			lt.zeroLegSince = now
		}
	} else {
		lt.zeroLegSince = time.Time{}
	}
}

func (lt *liveTerminal) readRelayLeg(legCtx context.Context, path transport.Path, gen uint64) {
	defer path.Close()
	for {
		b, err := path.Read(legCtx)
		if err != nil {
			lt.onRelayLegEnded(path, gen)
			return
		}
		if !lt.enqueueInbound(legCtx, b, false) {
			return
		}
	}
}

func (lt *liveTerminal) onRelayLegEnded(path transport.Path, gen uint64) {
	lt.mu.Lock()
	if lt.relayPath != path || lt.relayGen != gen {
		// Stale leg already replaced by AttachPath: never disturb active stream.
		lt.mu.Unlock()
		return
	}
	lt.relayPath = nil
	lt.stream.CloseLeg("relay", gen)
	keepAlive := lt.stream.HasLeg("direct") && lt.ep.directPeerActive(lt.ownerPeer)
	lt.noteLegStateLocked(lt.hub.now())
	lt.mu.Unlock()
	if !keepAlive {
		lt.cancel()
	}
}

// enqueueInbound applies blocking WS backpressure for relay frames (M1 — never
// silently dropping relay input frames) while keeping direct reads non-blocking
// so a slow terminal cannot stall the shared direct DataChannel read loop.
func (lt *liveTerminal) enqueueInbound(legCtx context.Context, raw []byte, fromDirect bool) bool {
	if lt.closed.Load() {
		return false
	}
	cp := append([]byte(nil), raw...)
	if !fromDirect {
		select {
		case lt.inbound <- inboundTermFrame{raw: cp, fromDirect: false}:
			return true
		case <-legCtx.Done():
			return false
		case <-lt.ctx.Done():
			return false
		}
	}
	select {
	case lt.inbound <- inboundTermFrame{raw: cp, fromDirect: true}:
		return true
	case <-lt.ctx.Done():
		return false
	default:
		slog.Warn("relay: direct terminal inbound queue full", "stream", lt.streamID[:min(8, len(lt.streamID))])
		return false
	}
}

func (lt *liveTerminal) inputLoop() {
	for {
		select {
		case <-lt.ctx.Done():
			return
		case item := <-lt.inbound:
			if !lt.processInbound(item.raw, item.fromDirect) {
				lt.cancel()
				return
			}
		}
	}
}

// processInbound verifies and reorders inbound terminal frames before invoking
// Accept, replacing legacy seq <= last close-on-duplicate with duplicate ACK.
func (lt *liveTerminal) processInbound(raw []byte, fromDirect bool) bool {
	env := &pb.Envelope{}
	if proto.Unmarshal(raw, env) != nil {
		return fromDirect
	}
	wire, err := deviceauth.FromEnvelope(env)
	if err != nil {
		return fromDirect
	}
	h, err := devicesec.ParseHeader(wire.Header)
	if err != nil || h.Machine != lt.ep.acct.DeviceID || h.Session != lt.sessionID || h.Request != lt.streamID || !h.Purpose.Control() || h.Purpose == devicesec.Command {
		return fromDirect
	}
	if lt.ownerPeer != "" && h.Sender != lt.ownerPeer {
		return fromDirect
	}
	nowMs := uint64(time.Now().UnixMilli())
	cipherHash := wireCipherHash(wire)
	lt.hub.mu.Lock()
	seenBefore := lt.hub.dedup.Seen(lt.ownerPeer, cipherHash, int64(nowMs))
	lt.hub.mu.Unlock()
	if seenBefore {
		// Verified byte-identical duplicate: ACK current watermark without closing or re-executing.
		lt.mu.Lock()
		wm := lt.stream.CurrentInputWatermark()
		lt.mu.Unlock()
		lt.sendControlAck(wm)
		return true
	}

	// Authenticate (Ed25519 + AES-GCM + membership/lease) BEFORE reordering and BEFORE Accept.
	auth, err := lt.ep.security.endpoint.Authenticate(wire, lt.key, nowMs)
	if errors.Is(err, deviceauth.ErrUnknown) {
		if rerr := lt.ep.security.refreshWithin(lt.ctx, 5*time.Second); rerr == nil {
			auth, err = lt.ep.security.endpoint.Authenticate(wire, lt.key, uint64(time.Now().UnixMilli()))
		}
	}
	if err != nil {
		// Unauthenticated/forged frames are never cached.
		slog.Warn("relay: terminal input auth", "stream", lt.streamID[:min(8, len(lt.streamID))], "err", err)
		return fromDirect
	}

	plain, err := deviceauth.Unpack(auth.Header(), auth.Body())
	if err != nil {
		return fromDirect
	}
	f := &pb.TerminalFrame{}
	if proto.Unmarshal(plain, f) != nil {
		return fromDirect
	}
	isInput := f.GetInput() != nil
	var inputBytes uint32
	if isInput {
		inputBytes = uint32(len(f.GetInput()))
	}
	payloadHash := sha256.Sum256(plain)

	lt.mu.Lock()
	action, readyFrame, rerr := lt.stream.ReceiveInput(nowMs, linkcore.TermFrame{
		Seq:         h.Seq,
		PayloadHash: payloadHash,
		RawBytes:    len(raw),
		IsInput:     isInput,
		InputBytes:  inputBytes,
		DeadlineMs:  h.Deadline,
		RawEnvelope: raw,
	})
	var dupWM linkcore.TermWatermark
	if action == linkcore.TermInputDuplicateACK {
		dupWM = lt.stream.CurrentInputWatermark()
	}
	lt.mu.Unlock()

	// Record verified ciphertext hash ONLY AFTER ReceiveInput accepts the frame (M8),
	// so budget-rejected frames are never swallowed for 10s on client retry.
	if action == linkcore.TermInputRun || action == linkcore.TermInputBuffered || action == linkcore.TermInputDuplicateACK {
		lt.hub.mu.Lock()
		lt.hub.dedup.RecordVerified(lt.ownerPeer, cipherHash, int64(nowMs))
		lt.hub.mu.Unlock()
	}

	switch action {
	case linkcore.TermInputDuplicateACK:
		// Do NOT close stream on duplicate; ACK current watermark so sender frees its input cache.
		lt.sendControlAck(dupWM)
		return true
	case linkcore.TermInputReject:
		if errors.Is(rerr, linkcore.ErrTermConflict) {
			slog.Warn("relay: terminal conflicting seq", "stream", lt.streamID[:min(8, len(lt.streamID))], "seq", h.Seq)
			return false
		}
		if rerr != nil {
			slog.Warn("relay: terminal input reject", "stream", lt.streamID[:min(8, len(lt.streamID))], "err", rerr)
		}
		return true
	case linkcore.TermInputBuffered:
		return true
	case linkcore.TermInputRun:
		for cur := readyFrame; cur != nil; {
			next := lt.executeContiguousInput(cur)
			cur = next
		}
		return true
	default:
		return true
	}
}

func (lt *liveTerminal) executeContiguousInput(tf *linkcore.TermFrame) *linkcore.TermFrame {
	nowMs := uint64(time.Now().UnixMilli())
	lt.mu.Lock()
	blocked := lt.stream.IsInputBlocked(*tf)
	lt.mu.Unlock()

	var (
		outcome            linkcore.TermOutcome
		definitelyInjected uint32
		possiblyInjected   uint32
		failureReason      uint32
	)
	if blocked {
		// Fail-stop is non-bypassable: subsequent Input frames are never injected into tmux.
		outcome = linkcore.TermOutcomeInputBlocked
		failureReason = 1
	} else {
		env := &pb.Envelope{}
		if proto.Unmarshal(tf.RawEnvelope, env) != nil {
			outcome = linkcore.TermOutcomeNonInputFailed
		} else if wire, err := deviceauth.FromEnvelope(env); err != nil {
			outcome = linkcore.TermOutcomeNonInputFailed
		} else {
			outcome, definitelyInjected, possiblyInjected, failureReason = lt.acceptAndApplyFrame(wire, tf, nowMs)
		}
	}

	lt.mu.Lock()
	next, emitACK, failStop, resolvedQueries, resolvedBarrier := lt.stream.CompleteInput(
		uint64(time.Now().UnixMilli()),
		tf.Seq,
		outcome,
		definitelyInjected,
		possiblyInjected,
		failureReason,
	)
	var wm linkcore.TermWatermark
	var fs linkcore.TermFlowState
	if emitACK || failStop != nil {
		wm = lt.stream.CurrentInputWatermark()
		fs = linkcore.TermFlowState{
			ToDaemon: wm,
			ToClient: lt.stream.CurrentOutputWatermark(),
		}
	}
	lt.mu.Unlock()

	if failStop != nil {
		lt.sendControlStreamFailed(*failStop, fs)
	}
	if emitACK {
		lt.sendControlAck(wm)
	}
	for _, q := range resolvedQueries {
		if qid, err := hex.DecodeString(q.QueryID); err == nil {
			lt.hub.storeSnapshot(lt.streamID+"/"+q.QueryID, lt.streamID, lt.ownerPeer, q.Snapshot, uint64(time.Now().UnixMilli()))
			pages := linkcore.PaginateInputStatusReply(lt.streamID, qid, q.Snapshot, 0)
			if len(pages) > 0 {
				lt.sendDirectControl(linkcore.ControlInputStatusReply, linkcore.EncodeInputStatusReply(pages[0]))
			}
		}
	}
	if resolvedBarrier != nil {
		if bid, err := hex.DecodeString(resolvedBarrier.BarrierID); err == nil {
			lt.sendDirectControl(linkcore.ControlBarrierReply, linkcore.EncodeBarrierReply(linkcore.BarrierReplyMsg{
				Stream:    lt.streamID,
				BarrierID: bid,
				State:     resolvedBarrier.Snapshot.State,
				Final:     true,
				Accepted:  true,
			}))
		}
	}
	return next
}

func (lt *liveTerminal) acceptAndApplyFrame(wire devicesec.Wire, tf *linkcore.TermFrame, nowMs uint64) (linkcore.TermOutcome, uint32, uint32, uint32) {
	outcome := linkcore.TermOutcomeApplied
	var definitelyInjected, possiblyInjected, failureReason uint32
	callbackEntered := false

	_, err := lt.ep.security.accept(lt.ctx, wire, lt.key, nowMs, func(h devicesec.Header, plain []byte) (deviceauth.Completion, error) {
		callbackEntered = true
		plain, err := deviceauth.Unpack(h, plain)
		if err != nil {
			outcome = linkcore.TermOutcomeNonInputFailed
			return deviceauth.Undetermined(), err
		}
		f := &pb.TerminalFrame{}
		if proto.Unmarshal(plain, f) != nil {
			outcome = linkcore.TermOutcomeNonInputFailed
			return deviceauth.Undetermined(), devicesec.ErrInvalid
		}
		switch k := f.Kind.(type) {
		case *pb.TerminalFrame_Input:
			data := k.Input
			total := uint32(len(data))
			for off := 0; off < len(data); off += termInputChunk {
				end := min(off+termInputChunk, len(data))
				if err := lt.t.Input(data[off:end]); err != nil {
					definitelyInjected = uint32(off)
					possiblyInjected = uint32(end)
					outcome = linkcore.TermOutcomeInputPartialUnknown
					failureReason = 2
					return deviceauth.Undetermined(), err
				}
			}
			definitelyInjected = total
			possiblyInjected = total
			outcome = linkcore.TermOutcomeApplied
			return deviceauth.Final(nil), nil
		case *pb.TerminalFrame_Resize:
			if err := lt.t.Resize(int(k.Resize.Cols), int(k.Resize.Rows), k.Resize.Fit); err != nil {
				// Resize failure MUST NOT trigger Input fail-stop; advances processed as NON_INPUT_FAILED.
				outcome = linkcore.TermOutcomeNonInputFailed
				failureReason = 3
				return deviceauth.Final(nil), nil
			}
			outcome = linkcore.TermOutcomeApplied
			return deviceauth.Final(nil), nil
		case *pb.TerminalFrame_HistoryRequest:
			if k.HistoryRequest == nil || len(k.HistoryRequest.RequestId) == 0 || len(k.HistoryRequest.RequestId) > 128 || len(k.HistoryRequest.Cursor) > 128 {
				outcome = linkcore.TermOutcomeNonInputFailed
				failureReason = 4
				return deviceauth.Final(nil), nil
			}
			select {
			case lt.historyRequests <- k.HistoryRequest:
				outcome = linkcore.TermOutcomeApplied
				return deviceauth.Final(nil), nil
			default:
				outcome = linkcore.TermOutcomeNonInputFailed
				failureReason = 5
				return deviceauth.Final(nil), nil
			}
		default:
			outcome = linkcore.TermOutcomeNonInputFailed
			return deviceauth.Undetermined(), devicesec.ErrInvalid
		}
	})
	if err != nil {
		slog.Warn("relay: terminal input", "stream", lt.streamID[:min(8, len(lt.streamID))], "err", err)
		if !callbackEntered {
			// L1 + N6: accept failed BEFORE callback ever ran (expired, lease/member/capacity);
			// never injected into tmux -> report exact 0/0 bounds as ExpiredNotApplied/NotApplied
			// (which enters user confirmation per N6 instead of silently skipping), and mark
			// non-input Resize/History as NonInputFailed without blocking subsequent Inputs.
			if tf.IsInput {
				if errors.Is(err, deviceauth.ErrExpired) || (tf.DeadlineMs > 0 && nowMs >= tf.DeadlineMs) {
					outcome = linkcore.TermOutcomeExpiredNotApplied
				} else {
					outcome = linkcore.TermOutcomeNotApplied
				}
				definitelyInjected = 0
				possiblyInjected = 0
				failureReason = 6
			} else {
				outcome = linkcore.TermOutcomeNonInputFailed
				failureReason = 6
			}
		} else if tf.IsInput && outcome == linkcore.TermOutcomeApplied {
			outcome = linkcore.TermOutcomeInputPartialUnknown
			possiblyInjected = tf.InputBytes
			failureReason = 2
		}
	}
	return outcome, definitelyInjected, possiblyInjected, failureReason
}

func (lt *liveTerminal) sendDirectControl(typ uint64, body []byte) {
	dm := lt.ep.direct.Load()
	if dm == nil || len(body) == 0 {
		return
	}
	if dp, ok := dm.activePeerFor(lt.ownerPeer); ok && dp.session != nil {
		_ = dp.session.SendControl(typ, body)
	}
}

func (lt *liveTerminal) sendControlAck(wm linkcore.TermWatermark) {
	lt.sendDirectControl(linkcore.ControlAck, linkcore.EncodeTermAck(linkcore.TermAckMsg{
		Stream:    lt.streamID,
		Direction: linkcore.TermDirToDaemon,
		State:     wm,
	}))
}

func (lt *liveTerminal) sendControlStreamFailed(detail linkcore.TermFrameStatus, state linkcore.TermFlowState) {
	lt.sendDirectControl(linkcore.ControlStreamFailed, linkcore.EncodeStreamFailed(linkcore.StreamFailedMsg{
		Stream:       lt.streamID,
		InputFailure: detail,
		State:        state,
	}))
}

func (c *endpoint) directPeerActive(peerID string) bool {
	dm := c.direct.Load()
	if dm == nil {
		return false
	}
	_, ok := dm.activePeerFor(peerID)
	return ok
}

func (c *endpoint) onDirectPeerPathClosed(peerID string) {
	if c.termHub == nil {
		return
	}
	c.termHub.mu.Lock()
	var list []*liveTerminal
	for _, lt := range c.termHub.live {
		if lt.ownerPeer == peerID {
			list = append(list, lt)
		}
	}
	c.termHub.mu.Unlock()
	nowT := c.termHub.now()
	nowMs := uint64(nowT.UnixMilli())
	for _, lt := range list {
		lt.mu.Lock()
		gen := lt.stream.LegGen("direct")
		if gen > 0 {
			lt.stream.CloseLeg("direct", gen)
		}
		// N3: reset directGen when direct leg closes so re-attaching after a new direct session
		// (whose generation restarts at 1) is never silently ignored.
		lt.directGen = 0
		rp := lt.relayPath
		var replayFrames []linkcore.TermFrame
		if rp != nil {
			// M4: replay any direct-only output frames unacknowledged by the client over the live relay leg
			// so a direct disconnect never leaves a frozen output gap on the client.
			fromSeq := lt.stream.ClientOutputAck() + 1
			if fromSeq <= lt.outSeq {
				replayFrames, _ = lt.stream.ReplayOutput(nowMs, fromSeq, lt.outSeq, 1<<20, 128)
			}
		}
		lt.noteLegStateLocked(nowT)
		lt.mu.Unlock()
		if rp != nil && len(replayFrames) > 0 {
			c.link.Run(func(context.Context) {
				for _, f := range replayFrames {
					if lt.closed.Load() || lt.ctx.Err() != nil {
						return
					}
					wctx, wcancel := context.WithTimeout(lt.ctx, 10*time.Second)
					err := rp.Write(wctx, f.RawEnvelope)
					wcancel()
					if err != nil {
						return
					}
				}
			})
		}
	}
}

func (c *endpoint) handleDirectTerminalInput(peerID, streamID string, raw []byte) {
	hub := c.terminalHubOwner()
	lt := hub.getLive(streamID)
	if lt == nil || lt.ownerPeer != peerID {
		return
	}
	lt.enqueueInbound(lt.ctx, raw, true)
}

func (c *endpoint) handleDirectTerminalControl(m *directManager, p *directPeer, o peerwire.Opened, now uint64) (uint64, []byte) {
	if !m.peerAuthorized(p) {
		return 0, nil
	}
	peerID := p.binding.Peer.ID
	hub := c.terminalHubOwner()
	receipt := func() (uint64, []byte) {
		return 4, peerwire.PB(
			peerwire.U(4, o.Context.Class),
			peerwire.U(5, o.Context.Seq),
			peerwire.B(6, o.Hash),
			peerwire.U(7, o.Context.Generation),
			peerwire.U(8, o.Context.Use),
		)
	}

	switch o.Type {
	case linkcore.ControlAttachPath:
		msg, err := linkcore.DecodeAttachPath(o.Payload)
		if err != nil || msg.Sender != peerID {
			return 0, nil
		}
		lt := hub.getLive(msg.Stream)
		if lt == nil {
			if strings.HasPrefix(msg.LegID, "d:") {
				// H-B: buffer direct AttachPath arriving slightly ahead of openTerminal for up to 5s.
				nowT := hub.now()
				if hub.storePendingDirect(msg.Stream, pendingDirectAttach{
					peerID:    peerID,
					sessionID: msg.Session,
					legID:     msg.LegID,
					gen:       msg.PathGeneration,
					expiresAt: nowT.Add(termPendingDirectTTL),
				}, nowT) {
					return receipt()
				}
			}
			// Explicitly reject unknown-stream AttachPath instead of silently dropping (H-B).
			return 8, peerwire.PB(peerwire.U(1, o.Context.Class), peerwire.U(2, o.Type), peerwire.U(3, 0))
		}
		if lt.ownerPeer != peerID || lt.sessionID != msg.Session {
			return 8, peerwire.PB(peerwire.U(1, o.Context.Class), peerwire.U(2, o.Type), peerwire.U(3, 0))
		}
		if strings.HasPrefix(msg.LegID, "d:") {
			lt.mu.Lock()
			if !lt.stream.HasLeg("direct") || msg.PathGeneration > lt.directGen || (msg.PathGeneration == lt.directGen && msg.LegID != lt.stream.LegID("direct")) {
				lt.directGen = msg.PathGeneration
				_ = lt.stream.AttachLeg("direct", msg.LegID, hub.instance, msg.PathGeneration)
				lt.noteLegStateLocked(hub.now())
			}
			lt.mu.Unlock()
			return receipt()
		}
		// Relay leg recovery via AttachPath (pure random 16-hex leg_id — zero relay-visible prefix, L2):
		// enforce monotonic PathGeneration and per-stream concurrency cap=1 (M7), and replay missed outputs (M4).
		legID := msg.LegID
		reqGen := msg.PathGeneration
		clientRendered := msg.State.ToClient.Processed
		lt.mu.Lock()
		if reqGen <= lt.relayAttachGen || lt.relayDialInFlight >= 1 {
			lt.mu.Unlock()
			return receipt()
		}
		lt.relayAttachGen = reqGen
		lt.relayDialInFlight++
		lt.mu.Unlock()

		c.link.Run(func(runCtx context.Context) {
			defer func() {
				lt.mu.Lock()
				lt.relayDialInFlight--
				lt.mu.Unlock()
			}()
			dialCtx, dialCancel := context.WithTimeout(lt.ctx, 20*time.Second)
			defer dialCancel()
			newPath, err := c.link.Router.OpenStream(dialCtx, transport.StreamRoute{ID: legID})
			if err != nil {
				return
			}
			if lt.closed.Load() || lt.ctx.Err() != nil {
				newPath.Close()
				return
			}
			lt.mu.Lock()
			if lt.closed.Load() || lt.ctx.Err() != nil || reqGen < lt.relayAttachGen {
				lt.mu.Unlock()
				newPath.Close()
				return
			}
			oldCancel := lt.relayCancel
			oldPath := lt.relayPath
			lt.relayGen = reqGen
			lt.relayPath = newPath
			lt.relayLegID = legID
			legCtx, legCancel := context.WithCancel(lt.ctx)
			lt.relayCancel = legCancel
			_ = lt.stream.AttachLeg("relay", legID, hub.instance, reqGen)
			lt.noteLegStateLocked(hub.now())
			var replayFrames []linkcore.TermFrame
			if clientRendered < lt.outSeq {
				replayFrames, _ = lt.stream.ReplayOutput(uint64(hub.now().UnixMilli()), clientRendered+1, lt.outSeq, 1<<20, 128)
			}
			lt.mu.Unlock()
			if oldCancel != nil {
				oldCancel()
			}
			if oldPath != nil {
				oldPath.Close()
			}
			for _, f := range replayFrames {
				wctx, wcancel := context.WithTimeout(legCtx, 10*time.Second)
				werr := newPath.Write(wctx, f.RawEnvelope)
				wcancel()
				if werr != nil {
					break
				}
			}
			c.link.Run(func(context.Context) {
				lt.readRelayLeg(legCtx, newPath, reqGen)
			})
		})
		return receipt()

	case linkcore.ControlInputStatus:
		msg, err := linkcore.DecodeInputStatus(o.Payload)
		if err != nil {
			return 0, nil
		}
		lt, ts := hub.getQueryable(msg.Stream, now)
		if ts == nil || ts.PeerID != peerID {
			return linkcore.ControlInputStatusReply, linkcore.EncodeInputStatusReply(linkcore.InputStatusReplyMsg{
				Stream:    msg.Stream,
				QueryID:   msg.QueryID,
				Available: false,
				Complete:  true,
			})
		}
		qKey := msg.Stream + "/" + hex.EncodeToString(msg.QueryID)
		var snap linkcore.TermStatusSnapshot
		if len(msg.SnapshotToken) > 0 {
			cached, ok := hub.loadSnapshot(qKey, msg.Stream, peerID, now)
			if !ok || !linkcore.VerifySnapshotToken(cached.State, msg.SnapshotToken) {
				return linkcore.ControlInputStatusReply, linkcore.EncodeInputStatusReply(linkcore.InputStatusReplyMsg{
					Stream:    msg.Stream,
					QueryID:   msg.QueryID,
					Available: false,
					Complete:  true,
				})
			}
			snap = cached
		} else {
			var ready bool
			if lt != nil {
				lt.mu.Lock()
				snap, ready = ts.QueryStatus(now, hex.EncodeToString(msg.QueryID), o.Context.Deadline)
				lt.mu.Unlock()
			} else {
				snap = ts.FreezeSnapshot(now, ts.Stopped)
				ready = true
			}
			if !ready {
				return receipt()
			}
			hub.storeSnapshot(qKey, msg.Stream, peerID, snap, now)
		}
		// Filter details starting at msg.FromSeq if requested.
		filtered := snap
		if msg.FromSeq > 0 {
			var kept []linkcore.TermFrameStatus
			for _, d := range snap.State.ToDaemon.Details {
				if d.Seq >= msg.FromSeq {
					kept = append(kept, d)
				}
			}
			filtered.State.ToDaemon.Details = kept
		}
		limit := int(msg.Limit)
		if limit <= 0 || limit > linkcore.TermInputDetailMax {
			limit = linkcore.TermInputDetailMax
		}
		pages := linkcore.PaginateInputStatusReply(msg.Stream, msg.QueryID, filtered, limit)
		if len(pages) == 0 {
			return linkcore.ControlInputStatusReply, linkcore.EncodeInputStatusReply(linkcore.InputStatusReplyMsg{
				Stream:        msg.Stream,
				QueryID:       msg.QueryID,
				Available:     true,
				SnapshotToken: snap.Token[:],
				State:         snap.State,
				Complete:      true,
			})
		}
		page := pages[0]
		// Preserve the canonical frozen snapshot token across all pages.
		page.SnapshotToken = append([]byte(nil), snap.Token[:]...)
		return linkcore.ControlInputStatusReply, linkcore.EncodeInputStatusReply(page)

	case linkcore.ControlAck:
		ack, isStreamAck, err := linkcore.DecodeTermAck(o.Payload)
		if err != nil || !isStreamAck {
			return 0, nil
		}
		lt := hub.getLive(ack.Stream)
		if lt == nil || lt.ownerPeer != peerID {
			return 0, nil
		}
		lt.mu.Lock()
		if ack.Direction == linkcore.TermDirToClient {
			lt.stream.NoteClientOutputAck(ack.State.Processed)
		} else {
			lt.stream.PruneConfirmedDetails(ack.State.Processed)
		}
		lt.mu.Unlock()
		return 0, nil

	case linkcore.ControlReplay:
		msg, err := linkcore.DecodeReplay(o.Payload)
		if err != nil || msg.Direction != linkcore.TermDirToClient {
			return 0, nil
		}
		lt := hub.getLive(msg.Stream)
		if lt == nil || lt.ownerPeer != peerID {
			return 0, nil
		}
		lt.mu.Lock()
		frames, rerr := lt.stream.ReplayOutput(now, msg.FromSeq, msg.ThroughSeq, msg.MaxBytes, msg.MaxFrames)
		lt.mu.Unlock()
		if rerr != nil {
			return receipt()
		}
		for _, f := range frames {
			if !m.peerAuthorized(p) {
				break
			}
			_ = p.enqueueTerm(f.RawEnvelope, time.Now().Add(10*time.Second))
		}
		return receipt()

	case linkcore.ControlBarrier:
		msg, err := linkcore.DecodeBarrier(o.Payload)
		if err != nil {
			return 0, nil
		}
		lt, ts := hub.getQueryable(msg.Stream, now)
		if ts == nil || ts.PeerID != peerID {
			return linkcore.ControlBarrierReply, linkcore.EncodeBarrierReply(linkcore.BarrierReplyMsg{
				Stream:    msg.Stream,
				BarrierID: msg.BarrierID,
				Final:     false,
				Accepted:  false,
			})
		}
		var snap linkcore.TermStatusSnapshot
		var ready bool
		if lt != nil {
			lt.mu.Lock()
			snap, ready = ts.RequestBarrier(now, hex.EncodeToString(msg.BarrierID), msg.StopAdmission)
			lt.mu.Unlock()
		} else {
			snap = ts.FreezeSnapshot(now, true)
			ready = true
		}
		if !ready {
			return receipt()
		}
		return linkcore.ControlBarrierReply, linkcore.EncodeBarrierReply(linkcore.BarrierReplyMsg{
			Stream:    msg.Stream,
			BarrierID: msg.BarrierID,
			State:     snap.State,
			Final:     msg.StopAdmission,
			Accepted:  true,
		})

	case linkcore.ControlClosePath:
		msg, err := linkcore.DecodeClosePath(o.Payload)
		if err != nil {
			return 0, nil
		}
		lt := hub.getLive(msg.Stream)
		if lt == nil || lt.ownerPeer != peerID {
			return receipt()
		}
		nowT := hub.now()
		lt.mu.Lock()
		if strings.HasPrefix(msg.LegID, "d:") {
			if lt.stream.LegID("direct") == msg.LegID {
				lt.stream.CloseLeg("direct", msg.PathGeneration)
				lt.directGen = 0
			}
			lt.noteLegStateLocked(nowT)
			cancelNow := msg.Reason == 1 && lt.relayPath == nil && !lt.stream.HasLeg("direct")
			lt.mu.Unlock()
			if cancelNow {
				lt.cancel()
			}
			return receipt()
		}
		if lt.relayLegID == msg.LegID {
			rc := lt.relayCancel
			rp := lt.relayPath
			lt.relayPath = nil
			lt.relayCancel = nil
			lt.stream.CloseLeg("relay", lt.relayGen)
			lt.noteLegStateLocked(nowT)
			cancelNow := msg.Reason == 1 && !lt.stream.HasLeg("direct")
			lt.mu.Unlock()
			if rc != nil {
				rc()
			}
			if rp != nil {
				rp.Close()
			}
			if cancelNow {
				lt.cancel()
			}
			return receipt()
		}
		lt.mu.Unlock()
		return receipt()
	}
	return 0, nil
}
