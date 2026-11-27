package transport

import (
	"bytes"
	"strings"

	"github.com/Lewin671/remote-agent/clientcore/domain/entropy"
	"github.com/Lewin671/remote-agent/shared/linkcore"
	"github.com/Lewin671/remote-agent/shared/peerwire"
)

const (
	maxPendingSignalsTotal   = 64
	maxPendingSignalsPerPeer = 8
	maxTransfersTracked      = 128
	lateStatsSampleLeadMs    = 1500
	// CoordEntropyRefillBytes is the chunk requested from the shell's OS CSPRNG.
	CoordEntropyRefillBytes = entropy.RefillBytes
	// CoordEntropyLowWater triggers proactive background refill before exhaustion.
	CoordEntropyLowWater = entropy.LowWaterBytes
	coordEntropyMaxBytes = entropy.MaxBytes
	// MemberKindMachine matches DeviceKind.DEVICE_KIND_MACHINE (1) in security heads.
	MemberKindMachine uint32 = 1
	// MemberKindClient matches DeviceKind.DEVICE_KIND_CLIENT (2) in security heads.
	MemberKindClient   uint32 = 2
	defaultTicketTTLMs int64  = 120_000
	ticketPruneGraceMs int64  = 60_000
)

// CoordConfig initializes a transport Coordinator.
type CoordConfig struct {
	Supported       bool
	Account         string
	ProtocolVersion uint64
	ServerHost      string
	ConfiguredStun  string
	Entropy         *entropy.Pool
}

// CoordMember describes one device member in the verified directory.
type CoordMember struct {
	ID    string
	EdPub [32]byte
	XPub  [32]byte
	Self  bool
	Kind  uint32
}

func (m CoordMember) isMachine() bool {
	return !m.Self && m.Kind == MemberKindMachine
}

// CoordSecuritySnapshot is the verified member/lease/codec snapshot supplied by the shell.
type CoordSecuritySnapshot struct {
	Member        bool
	Account       string
	Domain        [32]byte
	Head          string
	HeadHash      [32]byte
	Epoch         uint64
	StartedAt     int64
	LeaseNotAfter int64
	SelfID        string
	SelfEdPub     [32]byte
	SelfXPub      [32]byte
	Members       []CoordMember
}

// CoordEffect is an action emitted by Coordinator for clientcore/sync or the shell.
type CoordEffect interface{ coordEffect() }

type (
	// CoordNeedEntropy asks the shell for fresh OS CSPRNG random bytes.
	CoordNeedEntropy struct{ MinRandomBytes int }
	// CoordKeySign asks the shell to Ed25519-sign Message using its local device key.
	CoordKeySign struct {
		OpID    uint64
		Message []byte
	}
	// CoordKeyAgree asks the shell to compute X25519(self_x, PeerXPub).
	CoordKeyAgree struct {
		OpID     uint64
		PeerID   string
		PeerXPub [32]byte
	}
	// CoordAuthorize asks the shell to perform a security sync.
	CoordAuthorize struct{}
	// CoordPersistEnabled asks the shell to persist a machine direct-path preference.
	CoordPersistEnabled struct {
		MachineID string
		Enabled   bool
	}
	// CoordRetryNowResult answers RetryNow.
	CoordRetryNowResult struct {
		MachineID string
		Started   bool
	}
	// CoordSignalSend sends a sealed DirectedPeerFrame over the main WS stream.
	CoordSignalSend struct {
		ToDevice   []byte
		FrameClass uint32
		FrameID    []byte
		Sealed     []byte
		DeadlineMs int64
		Critical   bool
	}
	// CoordPcCreate asks the shell executor to create a PeerConnection and DataChannels.
	CoordPcCreate struct {
		RoundID    RoundID
		MachineID  string
		PCID       uint64
		Use        Use
		Generation uint64
		STUN       []string
	}
	// CoordPcDescribe asks the shell executor to create & set local SDP offer or answer.
	CoordPcDescribe struct {
		PCID       uint64
		Offer      bool
		ICERestart bool
	}
	// CoordPcSetRemote asks the shell executor to apply remote SDP offer or answer.
	CoordPcSetRemote struct {
		PCID  uint64
		SDP   []byte
		Offer bool
	}
	// CoordPcAddCandidates asks the shell executor to add remote ICE candidates or end-of-candidates.
	CoordPcAddCandidates struct {
		PCID       uint64
		Candidates []peerwire.Candidate
		End        bool
	}
	// CoordPcConfirm marks a PeerConnection verified for business traffic and delivers queued reads.
	CoordPcConfirm struct {
		PCID      uint64
		MachineID string
		Use       Use
	}
	// CoordPcClose asks the shell executor to close a PeerConnection.
	CoordPcClose struct {
		PCID   uint64
		Reason string
	}
	// CoordDcReadStats asks the shell executor to read WebRTC getStats once.
	CoordDcReadStats struct {
		PCID    uint64
		RoundID RoundID
		Use     Use
	}
	// CoordPcSendProbe asks the shell executor to transmit a 17-byte liveness probe on PCID's probe channel.
	CoordPcSendProbe struct {
		PCID uint64
		Raw  []byte
	}
	// CoordChanged notifies sync that direct status or diagnostics changed.
	CoordChanged struct{}
	// CoordTerminalOpen asks the shell to reopen a closed relay terminal stream.
	CoordTerminalOpen struct{ TerminalID string }
)

func (CoordNeedEntropy) coordEffect()     {}
func (CoordKeySign) coordEffect()         {}
func (CoordKeyAgree) coordEffect()        {}
func (CoordAuthorize) coordEffect()       {}
func (CoordPersistEnabled) coordEffect()  {}
func (CoordRetryNowResult) coordEffect()  {}
func (CoordSignalSend) coordEffect()      {}
func (CoordPcCreate) coordEffect()        {}
func (CoordPcDescribe) coordEffect()      {}
func (CoordPcSetRemote) coordEffect()     {}
func (CoordPcAddCandidates) coordEffect() {}
func (CoordPcConfirm) coordEffect()       {}
func (CoordPcClose) coordEffect()         {}
func (CoordDcReadStats) coordEffect()     {}
func (CoordPcSendProbe) coordEffect()     {}
func (CoordChanged) coordEffect()         {}
func (CoordTerminalOpen) coordEffect()    {}

// RouteDecision describes how an attempt for a command/transfer should be routed.
type RouteDecision struct {
	UseDirect bool
	PCID      uint64
	TicketID  uint64
	TimeoutMs int64
}

type dhKey struct {
	peerID string
	xPub   [32]byte
}

type agreeOp struct {
	peerID string
	xPub   [32]byte
}

type signOp struct {
	roundID RoundID
	sessOp  uint64
}

type pendingRoundStart struct {
	round      RoundID
	machine    string
	deadlineAt int64
	xPub       [32]byte
}

type pendingInboundSignal struct {
	source   string
	xPub     [32]byte
	raw      []byte
	deadline int64
}

type pcBinding struct {
	roundID        RoundID
	localID        uint64
	use            Use
	confirmed      bool
	closedNotified bool
	probe          linkcore.ProbeTracker
	probeSamples   []ProbeSample
}

func (b *pcBinding) recordProbeSample(now int64, rttMs float64) {
	if rttMs < 0 {
		rttMs = 0
	}
	kept := b.probeSamples[:0]
	for _, s := range b.probeSamples {
		if now-s.AtMs <= DirectProbeWindowMs {
			kept = append(kept, s)
		}
	}
	kept = append(kept, ProbeSample{AtMs: now, RttMs: rttMs})
	if len(kept) > DirectProbeKeepSamples {
		kept = append([]ProbeSample(nil), kept[len(kept)-DirectProbeKeepSamples:]...)
	}
	b.probeSamples = kept
}

type coordRound struct {
	id             RoundID
	machine        string
	head           string
	conn           []byte
	peer           peerwire.Public
	shared         []byte
	curNow         int64
	session        *linkcore.Session
	sentFrames     map[string]struct{}
	sentFrameRing  []string
	sentFrameHead  int
	prevMarks      [2][5]bool
	localToGlobal  map[uint64]uint64
	globalPC       [3]uint64 // indexed by UseRead (1), UseBulk (2)
	dcOpen         [3]bool
	confirmed      [3]bool
	srflxSeen      bool
	anyPC          bool
	sampledLate    bool
	everReady      bool
	roundUnsupp    bool
	useUnsupp      [3]bool
	attemptedUse   [3]bool
	clientCapsSent bool
	bootNotified   bool
	deadlineTicked bool
	ended          bool
}

func (r *coordRound) recordSentFrame(id string) {
	if _, exists := r.sentFrames[id]; exists {
		return
	}
	const maxSentFrames = 256
	if len(r.sentFrameRing) < maxSentFrames {
		r.sentFrameRing = append(r.sentFrameRing, id)
	} else {
		delete(r.sentFrames, r.sentFrameRing[r.sentFrameHead])
		r.sentFrameRing[r.sentFrameHead] = id
		r.sentFrameHead = (r.sentFrameHead + 1) % maxSentFrames
	}
	r.sentFrames[id] = struct{}{}
}

type trackedTicket struct {
	ticket     Ticket
	useDirect  bool
	deadlineMs int64
}

type termState struct {
	terminalID string
	reopen     Reopen
	nextAt     int64
	hasTimer   bool
}

// coordEntropyReader adapts the shared *entropy.Pool to linkcore.Session's io.Reader,
// mapping entropy.ErrNeedEntropy to linkcore.ErrNeedEntropy.
type coordEntropyReader struct {
	pool *entropy.Pool
}

func (r coordEntropyReader) Read(dst []byte) (int, error) {
	n, err := r.pool.Read(dst)
	if err != nil {
		return n, linkcore.ErrNeedEntropy
	}
	return n, nil
}

// Coordinator bridges the client transport Engine with shared/linkcore.Session,
// shared/peerwire.Codec, OS CSPRNG entropy pool, control messages, and terminal
// stream reopen budgets.
type Coordinator struct {
	cfg         CoordConfig
	stunServers []string
	engine      *Engine
	randPool    *entropy.Pool

	pendingEngineUnits int
	entropyAsked       bool
	hasInstance        bool
	instance           []byte // 16 bytes
	boot               []byte // 32 bytes
	mainOnline         bool
	foreground         bool
	snap               CoordSecuritySnapshot
	hasSnap            bool
	members            map[string]CoordMember
	codec              *peerwire.Codec
	selfPub            peerwire.Public

	dhCache       map[dhKey][]byte
	agreeInFlight map[dhKey]uint64
	agreeOps      map[uint64]agreeOp
	nextAgreeOp   uint64

	signOps        map[uint64]signOp
	nextSignOp     uint64
	nextPCID       uint64
	pcs            map[uint64]*pcBinding
	rounds         map[RoundID]*coordRound
	roundsByConn   map[string]RoundID
	pendingStarts  map[RoundID]*pendingRoundStart
	pendingSignals []*pendingInboundSignal

	hasEngineTick bool
	engineTickAt  int64

	tickets      map[uint64]trackedTicket
	nextTicketID uint64

	transfers     map[string]Upload
	transferOrder []string

	termBudget       linkcore.Budget
	terms            map[string]*termState
	termStreams      map[string]*clientTermStream
	earlyOutputs     map[string][]earlyTermOutput
	earlyOutputBytes int

	out []CoordEffect
}

// NewCoordinator constructs a Coordinator wrapped around transport.Engine.
func NewCoordinator(cfg CoordConfig) *Coordinator {
	pool := cfg.Entropy
	if pool == nil {
		pool = &entropy.Pool{}
	}
	return &Coordinator{
		cfg:           cfg,
		randPool:      pool,
		stunServers:   StunServers(cfg.ServerHost, cfg.ConfiguredStun),
		engine:        NewEngine(cfg.Supported),
		foreground:    true,
		members:       map[string]CoordMember{},
		dhCache:       map[dhKey][]byte{},
		agreeInFlight: map[dhKey]uint64{},
		agreeOps:      map[uint64]agreeOp{},
		signOps:       map[uint64]signOp{},
		pcs:           map[uint64]*pcBinding{},
		rounds:        map[RoundID]*coordRound{},
		roundsByConn:  map[string]RoundID{},
		pendingStarts: map[RoundID]*pendingRoundStart{},
		tickets:       map[uint64]trackedTicket{},
		transfers:     map[string]Upload{},
		terms:         map[string]*termState{},
	}
}

func (c *Coordinator) emit(e CoordEffect) { c.out = append(c.out, e) }
func (c *Coordinator) take() []CoordEffect {
	out := c.out
	c.out = nil
	return out
}

// Engine exposes the underlying Engine for testing and status inspection.
func (c *Coordinator) Engine() *Engine { return c.engine }

// HasInstance reports whether initial instance/boot keys have been drawn from OS entropy.
func (c *Coordinator) HasInstance() bool { return c.hasInstance }

// EntropyAvailable returns the number of buffered OS random bytes currently available.
func (c *Coordinator) EntropyAvailable() int { return c.randPool.Avail() }

// ProvideRandomBytes adds raw OS CSPRNG bytes to Coordinator's entropy pool and
// resumes any queued handshake starts, frame seals, or Engine jitter requests.
func (c *Coordinator) ProvideRandomBytes(now int64, raw []byte) []CoordEffect {
	return c.ProvideRandomBytesWithShared(now, raw, true)
}

// DrainRandomWaiters appends raw (if appendBytes is true) and resumes any
// queued Coordinator operations using the shared entropy pool.
func (c *Coordinator) ProvideRandomBytesWithShared(now int64, raw []byte, appendBytes bool) []CoordEffect {
	wasHasInstance := c.hasInstance
	c.entropyAsked = false
	if appendBytes && len(raw) > 0 {
		c.randPool.Append(raw)
	}
	if !c.hasInstance && c.randPool.Avail() >= 48 {
		c.instance = make([]byte, 16)
		c.boot = make([]byte, 32)
		_, _ = c.randPool.Read(c.instance)
		_, _ = c.randPool.Read(c.boot)
		c.hasInstance = true
		if len(c.selfPub.ID) > 0 {
			c.selfPub.Instance = c.instance
			c.selfPub.Boot = c.boot
		}
	}
	if c.pendingEngineUnits > 0 && c.randPool.Avail() >= c.pendingEngineUnits*8 {
		n := c.pendingEngineUnits
		c.pendingEngineUnits = 0
		units := make([]float64, n)
		for i := range units {
			u, _ := c.randPool.TakeUnitFloat()
			units[i] = u
		}
		more := c.engine.Handle(now, Entropy{Units: units})
		c.applyEngineEffects(now, more, nil)
	}
	for _, id := range c.sortedRoundIDs() {
		r := c.rounds[id]
		if r == nil || r.ended || !r.session.NeedsEntropy() {
			continue
		}
		_ = c.stepSession(now, r, linkcore.EntropyReady{})
	}
	if c.hasInstance {
		c.drainPendingStarts(now)
		c.drainPendingSignalsReady(now)
	}
	if !wasHasInstance && c.hasInstance && c.hasSnap && c.mainOnline && c.foreground && len(c.rounds) == 0 && len(c.pendingStarts) == 0 {
		c.handleEngine(now, DiscoverNow{})
	}
	c.maybeNeedPendingEntropy()
	return c.take()
}

// Configure updates static transport config (such as STUN host/override or support flag).
func (c *Coordinator) Configure(now int64, cfg CoordConfig) []CoordEffect {
	newStun := StunServers(cfg.ServerHost, cfg.ConfiguredStun)
	changed := cfg.Supported != c.cfg.Supported || cfg.Account != c.cfg.Account || cfg.ProtocolVersion != c.cfg.ProtocolVersion || !slicesEqual(newStun, c.stunServers)
	c.cfg = cfg
	c.stunServers = newStun
	if changed {
		c.handleEngine(now, SetSupported{Supported: cfg.Supported})
		c.rebuildCodec()
		c.emit(CoordChanged{})
	}
	if cfg.Supported {
		c.maybeLowWater()
	}
	return c.take()
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// SetLifecycle forwards foreground/background state changes.
func (c *Coordinator) SetLifecycle(now int64, foreground bool) []CoordEffect {
	c.foreground = foreground
	c.handleEngine(now, Lifecycle{Foreground: foreground})
	return c.take()
}

// SetMainLeg forwards main WebSocket stream readiness and notifies terminal reopeners.
func (c *Coordinator) SetMainLeg(now int64, ready bool) []CoordEffect {
	prev := c.mainOnline
	c.mainOnline = ready
	c.handleEngine(now, MainLeg{Ready: ready})
	if ready && !prev {
		for _, ts := range c.terms {
			w := ts.reopen.MainOnline(now, true, &c.termBudget)
			c.applyTermWake(ts, w)
		}
	}
	return c.take()
}

// SetNetworkChanged forwards network transitions.
func (c *Coordinator) SetNetworkChanged(now int64) []CoordEffect {
	c.handleEngine(now, NetworkChanged{})
	return c.take()
}

// LoadPreferences sets stored direct-path per-machine switches.
func (c *Coordinator) LoadPreferences(now int64, prefs map[string]bool) []CoordEffect {
	c.handleEngine(now, PreferencesLoaded{Enabled: prefs})
	return c.take()
}

// SetEnabled sets a computer's direct-path switch.
func (c *Coordinator) SetEnabled(now int64, machineID string, enabled bool) []CoordEffect {
	c.handleEngine(now, SetEnabled{Machine: machineID, Enabled: enabled})
	return c.take()
}

// RetryNow attempts to immediately start a direct round for machineID.
func (c *Coordinator) RetryNow(now int64, machineID string) []CoordEffect {
	c.handleEngine(now, RetryNow{Machine: machineID})
	return c.take()
}

// SetWatched forwards per-machine watched session IDs (Control 15).
func (c *Coordinator) SetWatched(now int64, sessions map[string][]string) []CoordEffect {
	c.handleEngine(now, SetWatched{Sessions: sessions})
	return c.take()
}

// RefreshDiagnostics triggers WebRTC stats reads for active rounds of machineID.
func (c *Coordinator) RefreshDiagnostics(now int64, machineID string) []CoordEffect {
	c.handleEngine(now, RefreshDiagnostics{Machine: machineID})
	return c.take()
}

// OnEngineTimer runs when TIMER_KIND_DIRECT_ENGINE fires.
func (c *Coordinator) OnEngineTimer(now int64) []CoordEffect {
	c.hasEngineTick = false
	c.PruneTickets(now)
	c.handleEngine(now, Tick{})
	return c.take()
}

// OnSessionTimer runs when TIMER_KIND_DIRECT_SESSION fires.
func (c *Coordinator) OnSessionTimer(now int64) []CoordEffect {
	c.prunePendingSignals(now)
	c.PruneTickets(now)
	for id := range c.pendingStarts {
		if ps := c.pendingStarts[id]; now >= ps.deadlineAt {
			delete(c.pendingStarts, id)
			c.handleEngine(now, RoundEnded{Round: id, TimedOut: true})
		}
	}
	ids := c.sortedRoundIDs()
	for _, id := range ids {
		r := c.rounds[id]
		if r == nil {
			continue
		}
		if !r.sampledLate && !r.session.View().Established && uint64(now)+lateStatsSampleLeadMs >= r.session.Deadline() {
			r.sampledLate = true
			c.handleEngine(now, DiagSampleWanted{Round: r.id, Use: UseRead})
			c.handleEngine(now, DiagSampleWanted{Round: r.id, Use: UseBulk})
		}
		if now >= int64(r.session.Deadline()) {
			r.deadlineTicked = true
		}
		if err := c.stepSession(now, r, linkcore.Tick{}); err != nil {
			continue
		}
	}
	c.stepProbeTrackers(now)
	c.stepClientTerminals(now)
	return c.take()
}

func (c *Coordinator) stepProbeTrackers(now int64) {
	if len(c.pcs) == 0 {
		return
	}
	pcIDs := make([]uint64, 0, len(c.pcs))
	for id := range c.pcs {
		pcIDs = append(pcIDs, id)
	}
	for i := 1; i < len(pcIDs); i++ {
		for j := i; j > 0 && pcIDs[j] < pcIDs[j-1]; j-- {
			pcIDs[j], pcIDs[j-1] = pcIDs[j-1], pcIDs[j]
		}
	}
	for _, pcID := range pcIDs {
		b, r := c.lookupPC(pcID)
		if b == nil || !b.confirmed || r.ended {
			continue
		}
		ready := r.dcOpen[b.use]
		needEntropy := false
		raw, timedOut := b.probe.Tick(now, ready, func() ([16]byte, bool) {
			var nonce [16]byte
			if _, err := c.randPool.Read(nonce[:]); err != nil {
				needEntropy = true
				return nonce, false
			}
			return nonce, true
		})
		if needEntropy {
			c.maybeNeedEntropy(CoordEntropyRefillBytes)
		}
		if len(raw) == linkcore.ProbeFrameBytes {
			c.emit(CoordPcSendProbe{PCID: pcID, Raw: raw})
			c.maybeLowWater()
		}
		if timedOut {
			b.probeSamples = nil
			delete(r.localToGlobal, b.localID)
			if r.globalPC[b.use] == pcID {
				r.globalPC[b.use] = 0
			}
			c.markPCClosed(now, r, b, pcID)
			delete(c.pcs, pcID)
			_ = c.stepSession(now, r, linkcore.PCClosed{ID: b.localID})
			c.emit(CoordPcClose{PCID: pcID, Reason: "probe_timeout"})
		}
	}
}

// OnSecuritySnapshot installs a verified directory/lease/codec snapshot.
func (c *Coordinator) OnSecuritySnapshot(now int64, snap CoordSecuritySnapshot) []CoordEffect {
	prevSelfX := c.snap.SelfXPub
	c.snap = snap
	c.hasSnap = true
	if prevSelfX != snap.SelfXPub {
		clear(c.dhCache)
		clear(c.agreeInFlight)
	}
	clear(c.members)
	var machines []string
	for _, m := range snap.Members {
		c.members[m.ID] = m
		if m.isMachine() {
			machines = append(machines, m.ID)
		}
	}
	if snap.SelfID != "" {
		c.selfPub = peerwire.Public{
			ID:       []byte(snap.SelfID),
			Role:     1,
			Instance: c.instance,
			Boot:     c.boot,
			Sign:     append([]byte(nil), snap.SelfEdPub[:]...),
			Box:      append([]byte(nil), snap.SelfXPub[:]...),
		}
	}
	c.rebuildCodec()

	c.handleEngine(now, SecuritySynced{
		Member:        snap.Member,
		Account:       snap.Account,
		Head:          snap.Head,
		StartedAt:     snap.StartedAt,
		LeaseNotAfter: snap.LeaseNotAfter,
		Machines:      machines,
	})
	c.handleEngine(now, AuthorizeDone{OK: snap.Member})

	if snap.Member {
		for _, m := range snap.Members {
			if !m.isMachine() {
				continue
			}
			c.ensureAgree(m.ID, m.XPub)
		}
	}
	return c.take()
}

// OnSecurityInstalled invalidates active rounds if the installed record changed heads or ended membership.
func (c *Coordinator) OnSecurityInstalled(now int64, member bool, account, head string) []CoordEffect {
	c.handleEngine(now, SecurityInstalled{
		Member:  member,
		Account: account,
		Head:    head,
	})
	return c.take()
}

// OnAuthorizeDone closes the Engine's authorizing gate after a security sync attempt completes or skips.
func (c *Coordinator) OnAuthorizeDone(now int64, ok bool) []CoordEffect {
	c.handleEngine(now, AuthorizeDone{OK: ok})
	return c.take()
}

// OnSecurityRejected invalidates active rounds after a failed security sync.
func (c *Coordinator) OnSecurityRejected(now int64) []CoordEffect {
	c.handleEngine(now, SecurityRejected{})
	c.handleEngine(now, AuthorizeDone{OK: false})
	return c.take()
}

func (c *Coordinator) rebuildCodec() {
	if !c.hasSnap || c.snap.Account == "" || c.snap.Epoch == 0 {
		c.codec = nil
		return
	}
	codec, err := peerwire.NewCodec(c.snap.Account, c.snap.Domain, c.snap.HeadHash, c.snap.Epoch, c.cfg.ProtocolVersion)
	if err != nil {
		c.codec = nil
		return
	}
	c.codec = codec
}

func (c *Coordinator) ensureAgree(peerID string, xPub [32]byte) ([]byte, bool) {
	key := dhKey{peerID: peerID, xPub: xPub}
	if sec, ok := c.dhCache[key]; ok {
		return sec, true
	}
	if !c.hasSnap || !c.snap.Member {
		return nil, false
	}
	if _, inflight := c.agreeInFlight[key]; !inflight {
		c.nextAgreeOp++
		opID := c.nextAgreeOp
		c.agreeInFlight[key] = opID
		c.agreeOps[opID] = agreeOp{peerID: peerID, xPub: xPub}
		c.emit(CoordKeyAgree{OpID: opID, PeerID: peerID, PeerXPub: xPub})
	}
	return nil, false
}

// OnKeyAgreed delivers the computed X25519 shared secret for opID.
func (c *Coordinator) OnKeyAgreed(now int64, opID uint64, shared []byte, ok bool) []CoordEffect {
	op, exists := c.agreeOps[opID]
	if !exists {
		return nil
	}
	delete(c.agreeOps, opID)
	key := dhKey{peerID: op.peerID, xPub: op.xPub}
	if c.agreeInFlight[key] == opID {
		delete(c.agreeInFlight, key)
	}
	if !ok || len(shared) != 32 {
		c.dropPendingForPeer(op.peerID, op.xPub)
		return c.take()
	}
	c.dhCache[key] = append([]byte(nil), shared...)
	c.flushPendingStarts(now, op.peerID, op.xPub, shared)
	c.flushPendingSignals(now, op.peerID, op.xPub, shared)
	return c.take()
}

// OnKeySigned delivers the Ed25519 signature for SignFrame OpID.
func (c *Coordinator) OnKeySigned(now int64, opID uint64, sig []byte) []CoordEffect {
	so, ok := c.signOps[opID]
	if !ok {
		return nil
	}
	delete(c.signOps, opID)
	r := c.rounds[so.roundID]
	if r == nil || r.ended {
		return nil
	}
	_ = c.stepSession(now, r, linkcore.FrameSigned{Op: so.sessOp, Signature: sig})
	return c.take()
}

// OnSignalReceived admits and processes a PeerFrameDelivery sealed outer frame from the main WS.
func (c *Coordinator) OnSignalReceived(now int64, raw []byte) []CoordEffect {
	if c.codec == nil || len(raw) == 0 || len(raw) > InboundFrameMax {
		return nil
	}
	f, err := peerwire.Parse(raw, 5)
	if err != nil || len(f) != 5 {
		return nil
	}
	fromB, ok1 := f[1]
	toB, ok2 := f[2]
	clsF, ok3 := f[3]
	idB, ok4 := f[4]
	if !ok1 || !ok2 || !ok3 || !ok4 || !fromB.Bytes || !toB.Bytes || clsF.Bytes || !idB.Bytes {
		return nil
	}
	if len(fromB.B) != 32 || !bytes.Equal(toB.B, c.selfPub.ID) || len(idB.B) != 16 {
		return nil
	}
	source := string(fromB.B)
	cls := int(clsF.U)
	if !c.engine.AdmitSignal(now, source, len(raw), cls) {
		return nil
	}
	member, ok := c.members[source]
	if !ok || !member.isMachine() {
		c.engine.SignalDone(source, len(raw))
		return nil
	}
	frozen := append([]byte(nil), raw...)
	shared, ready := c.ensureAgree(source, member.XPub)
	if !c.hasInstance {
		c.maybeNeedEntropy(CoordEntropyRefillBytes)
	}
	if !ready || !c.hasInstance {
		if len(c.pendingSignals) >= maxPendingSignalsTotal || c.countPendingSignals(source) >= maxPendingSignalsPerPeer {
			c.engine.SignalDone(source, len(frozen))
			return c.take()
		}
		c.pendingSignals = append(c.pendingSignals, &pendingInboundSignal{
			source:   source,
			xPub:     member.XPub,
			raw:      frozen,
			deadline: now + int64(RoundMs),
		})
		return c.take()
	}
	c.processSignalFrame(now, source, member, shared, frozen)
	c.engine.SignalDone(source, len(frozen))
	return c.take()
}

func (c *Coordinator) countPendingSignals(source string) int {
	n := 0
	for _, s := range c.pendingSignals {
		if s.source == source {
			n++
		}
	}
	return n
}

func (c *Coordinator) dropPendingForPeer(peerID string, xPub [32]byte) {
	kept := c.pendingSignals[:0]
	for _, s := range c.pendingSignals {
		if s.source == peerID && s.xPub == xPub {
			c.engine.SignalDone(s.source, len(s.raw))
			continue
		}
		kept = append(kept, s)
	}
	c.pendingSignals = kept
}

func (c *Coordinator) prunePendingSignals(now int64) {
	kept := c.pendingSignals[:0]
	for _, s := range c.pendingSignals {
		if now >= s.deadline {
			c.engine.SignalDone(s.source, len(s.raw))
			continue
		}
		kept = append(kept, s)
	}
	c.pendingSignals = kept
}

func (c *Coordinator) flushPendingStarts(now int64, peerID string, xPub [32]byte, shared []byte) {
	if !c.hasInstance {
		return
	}
	c.drainPendingStarts(now)
}

func (c *Coordinator) drainPendingStarts(now int64) {
	if !c.hasInstance || len(c.pendingStarts) == 0 {
		return
	}
	ids := make([]RoundID, 0, len(c.pendingStarts))
	for id := range c.pendingStarts {
		ids = append(ids, id)
	}
	for i := 1; i < len(ids); i++ {
		for j := i; j > 0 && ids[j] < ids[j-1]; j-- {
			ids[j], ids[j-1] = ids[j-1], ids[j]
		}
	}
	for _, id := range ids {
		ps := c.pendingStarts[id]
		if ps == nil {
			continue
		}
		if now >= ps.deadlineAt || c.engine.rounds[id] == nil {
			delete(c.pendingStarts, id)
			if c.engine.rounds[id] != nil {
				c.handleEngine(now, RoundEnded{Round: id, TimedOut: true})
			}
			continue
		}
		m, ok := c.members[ps.machine]
		if !ok || !m.isMachine() || m.XPub != ps.xPub {
			delete(c.pendingStarts, id)
			c.handleEngine(now, RoundEnded{Round: id})
			continue
		}
		shared, ready := c.dhCache[dhKey{peerID: m.ID, xPub: m.XPub}]
		if !ready {
			continue
		}
		if c.randPool.Avail() < 16+peerwire.SealEntropy {
			c.maybeNeedEntropy(CoordEntropyRefillBytes)
			continue
		}
		delete(c.pendingStarts, id)
		c.startOutboundRound(now, id, m, shared, ps.deadlineAt)
	}
}

func (c *Coordinator) flushPendingSignals(now int64, peerID string, xPub [32]byte, shared []byte) {
	if !c.hasInstance {
		return
	}
	c.drainPendingSignalsReady(now)
}

func (c *Coordinator) drainPendingSignalsReady(now int64) {
	if !c.hasInstance || len(c.pendingSignals) == 0 {
		return
	}
	kept := c.pendingSignals[:0]
	var toProcess []*pendingInboundSignal
	for _, s := range c.pendingSignals {
		if now >= s.deadline {
			c.engine.SignalDone(s.source, len(s.raw))
			continue
		}
		if _, ready := c.dhCache[dhKey{peerID: s.source, xPub: s.xPub}]; ready {
			toProcess = append(toProcess, s)
			continue
		}
		kept = append(kept, s)
	}
	c.pendingSignals = kept
	for _, s := range toProcess {
		member, hasMember := c.members[s.source]
		shared := c.dhCache[dhKey{peerID: s.source, xPub: s.xPub}]
		if hasMember && member.isMachine() && member.XPub == s.xPub && len(shared) == 32 && now < s.deadline {
			c.processSignalFrame(now, s.source, member, shared, s.raw)
		}
		c.engine.SignalDone(s.source, len(s.raw))
	}
}

func (c *Coordinator) processSignalFrame(now int64, source string, member CoordMember, shared []byte, raw []byte) {
	if c.codec == nil || !c.hasInstance {
		return
	}
	if c.engine.rounds == nil {
		return
	}
	barePeer := peerwire.Public{
		ID:   []byte(source),
		Role: 2,
		Sign: append([]byte(nil), member.EdPub[:]...),
		Box:  append([]byte(nil), member.XPub[:]...),
	}
	if o, err := c.codec.Open(raw, barePeer, c.selfPub, shared, uint64(now)); err == nil && o.Context.Class == 1 && o.Type == 1 {
		connKey := string(o.Context.Connection)
		if rid, exists := c.roundsByConn[connKey]; exists {
			if r := c.rounds[rid]; r != nil && !r.ended {
				_ = c.stepSession(now, r, linkcore.FrameReceived{Raw: raw})
			}
			return
		}
		// Ask Engine to accept a new incoming offered round.
		offeredEffects := c.engine.Handle(now, RoundOffered{
			Machine:    source,
			Boot:       string(o.Context.SenderBoot),
			DeadlineAt: int64(o.Context.Deadline),
		})
		acceptedID := RoundID(0)
		acceptedDeadline := int64(o.Context.Deadline)
		c.applyEngineEffects(now, offeredEffects, func(eff Effect) {
			if acc, ok := eff.(AcceptRound); ok {
				acceptedID = acc.Round
				acceptedDeadline = acc.DeadlineAt
			}
		})
		if acceptedID == 0 || !c.engine.SignalAuthorized(now, acceptedID) {
			if acceptedID != 0 {
				c.handleEngine(now, RoundEnded{Round: acceptedID})
			}
			return
		}
		peer := peerwire.Public{
			ID:       []byte(source),
			Role:     2,
			Instance: append([]byte(nil), o.Context.SenderInstance...),
			Boot:     append([]byte(nil), o.Context.SenderBoot...),
			Sign:     append([]byte(nil), member.EdPub[:]...),
			Box:      append([]byte(nil), member.XPub[:]...),
		}
		r := c.createRound(now, acceptedID, source, peer, shared, o.Context.Connection, uint64(acceptedDeadline), false)
		if r != nil {
			_ = c.stepSession(now, r, linkcore.FrameReceived{Raw: raw})
		}
		return
	}

	// Route bound signaling/control frame to matching active/retained round.
	var firstMatch *coordRound
	var matchErr bool
	for _, id := range c.sortedRoundIDs() {
		r := c.rounds[id]
		if r == nil || r.ended || r.machine != source || r.head != c.snap.Head {
			continue
		}
		if o, err := c.codec.Open(raw, r.session.Peer(), c.selfPub, r.shared, uint64(now)); err == nil && bytes.Equal(o.Context.Connection, r.conn) {
			firstMatch = r
			break
		} else if err != nil && r.session.Booted() {
			matchErr = true
		}
	}
	if firstMatch != nil {
		if !c.engine.SignalAuthorized(now, firstMatch.id) {
			return
		}
		_ = c.stepSession(now, firstMatch, linkcore.FrameReceived{Raw: raw})
		return
	}
	if matchErr {
		// Frame failed authentication against existing sessions; recorded implicitly at Signal layer when connection matches.
		_ = matchErr
	}
}

// OnSignalDelivery forwards PeerFrameResult (2 offline, 3 limited) to the round owning frameID.
func (c *Coordinator) OnSignalDelivery(now int64, frameID []byte, status uint32, retryAfterMs uint64) []CoordEffect {
	key := string(frameID)
	for _, id := range c.sortedRoundIDs() {
		r := c.rounds[id]
		if r == nil || r.ended {
			continue
		}
		if _, owned := r.sentFrames[key]; !owned {
			continue
		}
		if status == 2 {
			c.handleEngine(now, DiagSignal{Round: r.id, Note: NoteOffline})
		} else if status == 3 {
			c.handleEngine(now, DiagSignal{Round: r.id, Note: NoteLimited})
		}
		_ = c.stepSession(now, r, linkcore.DeliveryResult{ID: frameID, Status: status, Retry: retryAfterMs})
	}
	return c.take()
}

// OnPcCreated answers CoordPcCreate.
func (c *Coordinator) OnPcCreated(now int64, pcID uint64, ok bool) []CoordEffect {
	b, r := c.lookupPC(pcID)
	if b == nil {
		return nil
	}
	if ok {
		c.handleEngine(now, DiagAttempt{Round: r.id, Use: b.use})
	}
	_ = c.stepSession(now, r, linkcore.PCCreated{ID: b.localID, OK: ok})
	if !ok {
		c.handleEngine(now, DiagBlocked{Round: r.id, Use: b.use})
		delete(r.localToGlobal, b.localID)
		if r.globalPC[b.use] == pcID {
			r.globalPC[b.use] = 0
		}
		delete(c.pcs, pcID)
	}
	return c.take()
}

// OnPcDescription answers CoordPcDescribe.
func (c *Coordinator) OnPcDescription(now int64, pcID uint64, sdp []byte, ok bool) []CoordEffect {
	b, r := c.lookupPC(pcID)
	if b == nil {
		return nil
	}
	if !ok {
		c.handleEngine(now, DiagReject{Round: r.id, Use: b.use, Kind: "browser", Reason: "describe"})
	}
	_ = c.stepSession(now, r, linkcore.PCDescribed{ID: b.localID, SDP: sdp, OK: ok})
	return c.take()
}

// OnPcRemoteSet answers CoordPcSetRemote.
func (c *Coordinator) OnPcRemoteSet(now int64, pcID uint64, ok bool, errReason string) []CoordEffect {
	b, r := c.lookupPC(pcID)
	if b == nil {
		return nil
	}
	if !ok {
		c.handleEngine(now, DiagReject{Round: r.id, Use: b.use, Kind: "browser", Reason: nonEmpty(errReason, "remote_sdp")})
	}
	_ = c.stepSession(now, r, linkcore.PCRemoteSet{ID: b.localID, OK: ok})
	return c.take()
}

// OnPcCandidatesAdded answers CoordPcAddCandidates.
func (c *Coordinator) OnPcCandidatesAdded(now int64, pcID uint64, ok bool, errReason string) []CoordEffect {
	b, r := c.lookupPC(pcID)
	if b == nil {
		return nil
	}
	if !ok {
		c.handleEngine(now, DiagReject{Round: r.id, Use: b.use, Kind: "browser", Reason: nonEmpty(errReason, "candidate")})
	}
	_ = c.stepSession(now, r, linkcore.PCCandidatesAdded{ID: b.localID, OK: ok})
	return c.take()
}

// OnPcLocalCandidate reports a gathered local ICE candidate or gathering end.
func (c *Coordinator) OnPcLocalCandidate(now int64, pcID uint64, candidate string, end bool) []CoordEffect {
	b, r := c.lookupPC(pcID)
	if b == nil {
		return nil
	}
	if !end && candidate != "" {
		if strings.Contains(candidate, " typ srflx") {
			r.srflxSeen = true
		}
		c.handleEngine(now, DiagCandidate{Round: r.id, Use: b.use, Remote: false, Line: candidate})
	}
	_ = c.stepSession(now, r, linkcore.PCLocalCandidate{
		ID:        b.localID,
		Candidate: []byte(candidate),
		End:       end,
		Valid:     !r.ended && c.engine.SignalAuthorized(now, r.id),
	})
	return c.take()
}

// OnPcState updates ICE/Gather/Conn/DTLS/SCTP states and transitions linkcore PCState.
func (c *Coordinator) OnPcState(now int64, pcID uint64, kind StateKind, value string) []CoordEffect {
	b, r := c.lookupPC(pcID)
	if b == nil {
		return nil
	}
	c.handleEngine(now, DiagState{Round: r.id, Use: b.use, Kind: kind, Value: value})
	if kind == StateConn && (value == "connected" || value == "failed" || value == "disconnected") ||
		kind == StateICE && (value == "connected" || value == "completed" || value == "failed") {
		c.handleEngine(now, DiagSampleWanted{Round: r.id, Use: b.use})
	}
	if kind == StateConn {
		st := linkcore.PCOther
		switch value {
		case "connected":
			st = linkcore.PCConnected
		case "disconnected":
			st = linkcore.PCDisconnected
		case "failed":
			st = linkcore.PCFailed
		}
		_ = c.stepSession(now, r, linkcore.PCStateChanged{
			ID:    b.localID,
			State: st,
			Valid: !r.ended && c.engine.SignalAuthorized(now, r.id),
		})
	}
	return c.take()
}

// OnPcStunError records an ICE server error without exposing raw IP/credentials outside diag classification.
func (c *Coordinator) OnPcStunError(now int64, pcID uint64, code int, address string) []CoordEffect {
	b, r := c.lookupPC(pcID)
	if b == nil {
		return nil
	}
	c.handleEngine(now, DiagStunError{Round: r.id, Use: b.use, Code: code, Address: address})
	return c.take()
}

// OnDcState reports DataChannel open/closed state for pcID.
func (c *Coordinator) OnDcState(now int64, pcID uint64, open bool) []CoordEffect {
	b, r := c.lookupPC(pcID)
	if b == nil {
		return nil
	}
	val := "closed"
	if open {
		val = "open"
	}
	c.handleEngine(now, DiagState{Round: r.id, Use: b.use, Kind: StateDC, Value: val})
	r.dcOpen[b.use] = open
	if !open {
		b.probe.Reset()
		b.probeSamples = nil
		c.markPCClosed(now, r, b, pcID)
	}
	return c.take()
}

// OnPcProbeReceived processes an inbound 17-byte probe echo reply on pcID.
func (c *Coordinator) OnPcProbeReceived(now int64, pcID uint64, raw []byte) []CoordEffect {
	b, r := c.lookupPC(pcID)
	if b == nil || !b.confirmed || r.ended {
		return nil
	}
	if rttMs, ok := b.probe.OnReply(now, raw); ok && b.use == UseRead {
		b.recordProbeSample(now, rttMs)
	}
	return c.take()
}

// OnPcClosed reports that the native WebRTC PeerConnection closed.
func (c *Coordinator) OnPcClosed(now int64, pcID uint64) []CoordEffect {
	b, r := c.lookupPC(pcID)
	if b == nil {
		return nil
	}
	b.probeSamples = nil
	r.dcOpen[b.use] = false
	if r.globalPC[b.use] == pcID {
		r.globalPC[b.use] = 0
	}
	_ = c.stepSession(now, r, linkcore.PCClosed{ID: b.localID})
	c.markPCClosed(now, r, b, pcID)
	delete(r.localToGlobal, b.localID)
	delete(c.pcs, pcID)
	if !r.ended && r.deadlineTicked && r.globalPC[UseRead] == 0 && r.globalPC[UseBulk] == 0 {
		_ = c.stepSession(now, r, linkcore.Tick{})
	}
	return c.take()
}

func (c *Coordinator) markPCClosed(now int64, r *coordRound, b *pcBinding, pcID uint64) {
	if b.confirmed && !b.closedNotified {
		b.closedNotified = true
		r.confirmed[b.use] = false
		if b.use == UseRead && pcID != 0 {
			c.detachDirectTerminalsForPC(now, pcID)
		}
		c.handleEngine(now, PathClosed{Round: r.id, Use: b.use})
	}
}

// OnDcStats delivers a WebRTC getStats report for pcID.
func (c *Coordinator) OnDcStats(now int64, pcID uint64, report *StatsReport) []CoordEffect {
	b, r := c.lookupPC(pcID)
	if b == nil {
		return nil
	}
	c.handleEngine(now, DiagStats{Round: r.id, Use: b.use, Report: report})
	return c.take()
}

// InboundDCContext returns routing metadata for an arriving DataChannel message on pcID.
func (c *Coordinator) InboundDCContext(now int64, pcID uint64) (roundID RoundID, machineID string, use Use, ok bool) {
	b, r := c.lookupPC(pcID)
	if b == nil || !b.confirmed || r.ended || !c.engine.SignalAuthorized(now, r.id) {
		return 0, "", UseBoth, false
	}
	return r.id, r.machine, b.use, true
}

// PushedEventAllowed checks whether a DataChannel pushed Event envelope is authorized.
func (c *Coordinator) PushedEventAllowed(now int64, id RoundID, use Use, machine, sender string) bool {
	return c.engine.PushedEventAllowed(now, id, use, machine, sender)
}

// PrepareAttempt chooses Direct vs Relay for a read or bulk command/transfer and returns a tracked ticket.
func (c *Coordinator) PrepareAttempt(now int64, machineID string, h Hint, deadlineMs int64, forceRelay bool) RouteDecision {
	ticketDeadline := deadlineMs
	if ticketDeadline <= 0 {
		ticketDeadline = now + defaultTicketTTLMs
	}
	if !forceRelay {
		if ticket, ok := c.engine.Attempt(now, machineID, h); ok {
			if r := c.rounds[ticket.Round]; r != nil {
				u := useOf(h)
				if pcID := r.globalPC[u]; pcID != 0 {
					c.nextTicketID++
					tid := c.nextTicketID
					c.tickets[tid] = trackedTicket{ticket: ticket, useDirect: true, deadlineMs: ticketDeadline}
					return RouteDecision{
						UseDirect: true,
						PCID:      pcID,
						TicketID:  tid,
						TimeoutMs: AttemptTimeoutMs(deadlineMs, now),
					}
				}
			}
		}
	}
	t := c.engine.Track(machineID, h)
	c.nextTicketID++
	tid := c.nextTicketID
	c.tickets[tid] = trackedTicket{ticket: t, useDirect: false, deadlineMs: ticketDeadline}
	return RouteDecision{UseDirect: false, TicketID: tid}
}

// PrepareInteractive returns a direct AttemptInteractive ticket if the read path is ready.
func (c *Coordinator) PrepareInteractive(now int64, machineID string, deadlineMs int64) (RouteDecision, bool) {
	ticket, ok := c.engine.AttemptInteractive(now, machineID)
	if !ok {
		return RouteDecision{}, false
	}
	r := c.rounds[ticket.Round]
	if r == nil {
		return RouteDecision{}, false
	}
	pcID := r.globalPC[UseRead]
	if pcID == 0 {
		return RouteDecision{}, false
	}
	ticketDeadline := deadlineMs
	if ticketDeadline <= 0 {
		ticketDeadline = now + defaultTicketTTLMs
	}
	c.nextTicketID++
	tid := c.nextTicketID
	c.tickets[tid] = trackedTicket{ticket: ticket, useDirect: true, deadlineMs: ticketDeadline}
	return RouteDecision{
		UseDirect: true,
		PCID:      pcID,
		TicketID:  tid,
		TimeoutMs: AttemptTimeoutMs(deadlineMs, now),
	}, true
}

// ObserveTicket records a verified Daemon completion on the tracked ticket.
func (c *Coordinator) ObserveTicket(now int64, ticketID uint64, direct bool, bytes int64, ms float64) []CoordEffect {
	t, ok := c.tickets[ticketID]
	if !ok {
		return nil
	}
	delete(c.tickets, ticketID)
	if effs := c.engine.Observe(now, t.ticket, direct, bytes, ms); len(effs) > 0 {
		c.applyEngineEffects(now, effs, nil)
	}
	return c.take()
}

// FailTicket records a transport failure on a tracked direct ticket.
func (c *Coordinator) FailTicket(now int64, ticketID uint64) []CoordEffect {
	t, ok := c.tickets[ticketID]
	if !ok {
		return nil
	}
	delete(c.tickets, ticketID)
	if effs := c.engine.Failed(now, t.ticket); len(effs) > 0 {
		c.applyEngineEffects(now, effs, nil)
	}
	return c.take()
}

// ReleaseTicket drops an uncompleted ticket without reporting a direct failure or observation.
func (c *Coordinator) ReleaseTicket(ticketID uint64) {
	if ticketID != 0 {
		delete(c.tickets, ticketID)
	}
}

// PruneTickets sweeps abandoned tickets whose deadline + grace window has passed.
func (c *Coordinator) PruneTickets(now int64) {
	for id, t := range c.tickets {
		if now >= t.deadlineMs+ticketPruneGraceMs {
			delete(c.tickets, id)
		}
	}
}

// ClearTickets drops all tracked route tickets (e.g., on sync stop).
func (c *Coordinator) ClearTickets() {
	clear(c.tickets)
}

// PendingTicketCount returns the number of currently tracked tickets (for diagnostics/tests).
func (c *Coordinator) PendingTicketCount() int {
	return len(c.tickets)
}

// BeginTransfer records upload kind metadata (UploadImage vs UploadFile) for PutImageChunk routing.
func (c *Coordinator) BeginTransfer(machineID, transferID string, hasFileName bool) {
	if machineID == "" || transferID == "" {
		return
	}
	key := machineID + "/" + transferID
	if _, exists := c.transfers[key]; !exists {
		if len(c.transferOrder) >= maxTransfersTracked {
			evict := c.transferOrder[0]
			c.transferOrder = c.transferOrder[1:]
			delete(c.transfers, evict)
		}
		c.transferOrder = append(c.transferOrder, key)
	}
	u := UploadImage
	if hasFileName {
		u = UploadFile
	}
	c.transfers[key] = u
}

// EndTransfer removes tracked upload metadata when CompleteImageUpload/AbortImageUpload runs.
func (c *Coordinator) EndTransfer(machineID, transferID string) {
	key := machineID + "/" + transferID
	delete(c.transfers, key)
}

// TransferUploadKind returns the upload kind registered for transferID, if any.
func (c *Coordinator) TransferUploadKind(machineID, transferID string) (Upload, bool) {
	u, ok := c.transfers[machineID+"/"+transferID]
	return u, ok
}

// TerminalLive notifies Coordinator that a relay terminal WebSocket became live.
func (c *Coordinator) TerminalLive(now int64, id string) []CoordEffect {
	ts := c.termFor(id)
	w := ts.reopen.Live(now)
	c.applyTermWake(ts, w)
	return c.take()
}

// TerminalClosed notifies Coordinator that a relay terminal WebSocket closed or failed to open.
func (c *Coordinator) TerminalClosed(now int64, id string, opened, online bool) []CoordEffect {
	if id == "" {
		return nil
	}
	ts := c.termFor(id)
	if !online && !opened {
		ts.reopen.OfflineClosed()
		ts.hasTimer = false
		return nil
	}
	unit := 1.0
	if u, ok := c.randPool.TakeUnitFloat(); ok {
		unit = u
		c.maybeLowWater()
	} else {
		c.maybeNeedEntropy(CoordEntropyRefillBytes)
	}
	w := ts.reopen.Closed(now, opened, online, unit, &c.termBudget)
	c.applyTermWake(ts, w)
	return c.take()
}

// TerminalManual handles a user-triggered reconnect for a relay terminal stream.
func (c *Coordinator) TerminalManual(id string) []CoordEffect {
	ts := c.termFor(id)
	w := ts.reopen.Manual()
	c.applyTermWake(ts, w)
	return c.take()
}

// OnTerminalTimer evaluates due terminal reopen timers.
func (c *Coordinator) OnTerminalTimer(now int64) []CoordEffect {
	for _, ts := range c.terms {
		if ts.hasTimer && now >= ts.nextAt {
			ts.hasTimer = false
			w := ts.reopen.Timer(now, c.mainOnline, &c.termBudget)
			c.applyTermWake(ts, w)
		}
	}
	return c.take()
}

// ClearTerminal removes terminal state when a terminal UI disconnects/disposes.
func (c *Coordinator) ClearTerminal(id string) {
	delete(c.terms, id)
}

func (c *Coordinator) termFor(id string) *termState {
	ts := c.terms[id]
	if ts == nil {
		ts = &termState{terminalID: id}
		c.terms[id] = ts
	}
	return ts
}

func (c *Coordinator) applyTermWake(ts *termState, w Wake) {
	ts.hasTimer = w.Set
	ts.nextAt = w.At
	if w.Open {
		c.emit(CoordTerminalOpen{TerminalID: ts.terminalID})
	}
}

// NextTimers returns the next due wall/mono timestamps for engine tick, direct session tick,
// and terminal reopeners.
func (c *Coordinator) NextTimers(now int64) (engineAt int64, engineOK bool, sessionAt int64, sessionOK bool, termAt int64, termOK bool) {
	if c.hasEngineTick {
		engineAt, engineOK = c.engineTickAt, true
	}
	addSess := func(at int64) {
		if !sessionOK || at < sessionAt {
			sessionAt, sessionOK = at, true
		}
	}
	for _, r := range c.rounds {
		if r.ended {
			continue
		}
		v := r.session.View()
		if due, ok := r.session.NextTimer(); ok {
			addSess(int64(due))
		}
		if !v.Retired && !r.sampledLate && !v.Established && r.session.Deadline() > lateStatsSampleLeadMs {
			addSess(int64(r.session.Deadline()) - lateStatsSampleLeadMs)
		}
		if !v.Retired && !r.deadlineTicked {
			addSess(int64(r.session.Deadline()))
		}
		if v.Retired {
			addSess(int64(r.session.RetainUntil()))
		}
	}
	for _, ps := range c.pendingStarts {
		addSess(ps.deadlineAt)
	}
	for _, s := range c.pendingSignals {
		addSess(s.deadline)
	}
	for _, b := range c.pcs {
		if b == nil || !b.confirmed {
			continue
		}
		r := c.rounds[b.roundID]
		ready := r != nil && !r.ended && r.dcOpen[b.use]
		if due, ok := b.probe.NextDue(ready); ok {
			if due < now {
				due = now
			}
			addSess(due)
		}
	}
	for _, ct := range c.termStreams {
		if ct == nil {
			continue
		}
		if due, ok := ct.stream.NextDue(); ok {
			d := int64(due)
			if d < now {
				d = now
			}
			addSess(d)
		}
		if ct.disconnectedAll {
			d := ct.disconnectedAt + termDisconnectTimeoutMs
			if d < now {
				d = now
			}
			addSess(d)
		}
		if ct.relayRetryAt > 0 {
			d := ct.relayRetryAt
			if d < now {
				d = now
			}
			addSess(d)
		}
		if ct.queryInFlight && ct.querySentAt > 0 {
			d := ct.querySentAt + termQueryRetryMs
			if d < now {
				d = now
			}
			addSess(d)
		}
		if ct.barrierInFlight && ct.barrierSentAt > 0 {
			d := ct.barrierSentAt + int64(linkcore.TermBarrierTimeoutMs)
			if d < now {
				d = now
			}
			addSess(d)
		}
		if ct.stream.OutputGapDue() > 0 && ct.replayDueAt > 0 {
			d := ct.replayDueAt
			if d < now {
				d = now
			}
			addSess(d)
		}
	}
	for _, ts := range c.terms {
		if ts.hasTimer && (!termOK || ts.nextAt < termAt) {
			termAt, termOK = ts.nextAt, true
		}
	}
	return
}

// Query helpers delegated to Engine:
func (c *Coordinator) Ready(now int64, machineID string, h Hint) bool {
	return c.engine.Ready(now, machineID, h)
}
func (c *Coordinator) Status(now int64, machineID string) Status {
	st := c.engine.Status(now, machineID)
	c.fillReadProbeSamples(now, machineID, &st.Measured)
	return st
}

func (c *Coordinator) fillReadProbeSamples(now int64, machineID string, m *Measured) {
	if c == nil || m == nil || machineID == "" {
		return
	}
	p := c.engine.peers[machineID]
	if p == nil {
		return
	}
	r := c.rounds[p.id]
	if r == nil || r.ended || !r.confirmed[UseRead] || !r.dcOpen[UseRead] {
		return
	}
	pcID := r.globalPC[UseRead]
	if pcID == 0 {
		return
	}
	b := c.pcs[pcID]
	if b == nil || !b.confirmed || b.closedNotified || len(b.probeSamples) == 0 {
		return
	}
	n := 0
	for _, s := range b.probeSamples {
		if now-s.AtMs <= DirectProbeWindowMs && n < DirectProbeKeepSamples {
			m.ProbeSamples[n] = s
			n++
		}
	}
	m.ProbeSampleCount = n
}

// ReadProbeSamples returns recent direct liveness probe RTT samples for machineID's active read path.
func (c *Coordinator) ReadProbeSamples(now int64, machineID string) []ProbeSample {
	m := c.Status(now, machineID).Measured
	if m.ProbeSampleCount == 0 {
		return nil
	}
	out := make([]ProbeSample, m.ProbeSampleCount)
	copy(out, m.ProbeSamples[:m.ProbeSampleCount])
	return out
}
func (c *Coordinator) FileRoute(now int64, machineID string) Route {
	return c.engine.FileRoute(now, machineID)
}
func (c *Coordinator) Capabilities(now int64, machineID string) (Caps, bool) {
	return c.engine.Capabilities(now, machineID)
}
func (c *Coordinator) Phase2CommandsReady(now int64, machineID string) bool {
	return c.engine.Phase2CommandsReady(now, machineID)
}
func (c *Coordinator) TerminalReady(now int64, machineID string) bool {
	return c.engine.TerminalReady(now, machineID)
}
func (c *Coordinator) Enabled(machineID string) bool { return c.engine.enabled(machineID) }
func (c *Coordinator) Diagnostics(machineID string) ([]*RoundData, int64, bool) {
	return c.engine.Diagnostics(machineID)
}

func (c *Coordinator) handleEngine(now int64, ev Event) {
	if ev == nil {
		return
	}
	effs := c.engine.Handle(now, ev)
	c.applyEngineEffects(now, effs, nil)
}

func (c *Coordinator) applyEngineEffects(now int64, effs []Effect, observe func(Effect)) {
	for _, eff := range effs {
		if observe != nil {
			observe(eff)
		}
		switch eff := eff.(type) {
		case RequestEntropy:
			if eff.Count > 0 && c.randPool.Avail() >= eff.Count*8 {
				units := make([]float64, eff.Count)
				for i := range units {
					u, _ := c.randPool.TakeUnitFloat()
					units[i] = u
				}
				c.maybeLowWater()
				more := c.engine.Handle(now, Entropy{Units: units})
				c.applyEngineEffects(now, more, observe)
			} else if eff.Count > 0 {
				if eff.Count > c.pendingEngineUnits {
					c.pendingEngineUnits = eff.Count
				}
				req := max(CoordEntropyRefillBytes, c.pendingEngineUnits*8)
				c.maybeNeedEntropy(req)
			}
		case Authorize:
			c.emit(CoordAuthorize{})
		case ScheduleTick:
			c.hasEngineTick = true
			c.engineTickAt = now + eff.DelayMs
		case CancelTick:
			c.hasEngineTick = false
		case PersistEnabled:
			c.emit(CoordPersistEnabled{MachineID: eff.Machine, Enabled: eff.Enabled})
		case RetryNowResult:
			c.emit(CoordRetryNowResult{MachineID: eff.Machine, Started: eff.Started})
		case Changed:
			c.emit(CoordChanged{})
		case ReadStats:
			if r := c.rounds[eff.Round]; r != nil && eff.Use != UseBoth {
				if pcID := r.globalPC[eff.Use]; pcID != 0 {
					c.emit(CoordDcReadStats{PCID: pcID, RoundID: r.id, Use: eff.Use})
				} else {
					c.engine.Handle(now, DiagStats{Round: eff.Round, Use: eff.Use, Report: nil})
				}
			} else {
				c.engine.Handle(now, DiagStats{Round: eff.Round, Use: eff.Use, Report: nil})
			}
		case StartRound:
			c.onStartRound(now, eff)
		case AcceptRound:
			// Handled synchronously inside processSignalFrame observer callback.
		case RetireRound:
			if r := c.rounds[eff.Round]; r != nil && !r.ended {
				_ = c.stepSession(now, r, linkcore.RetireRound{})
			}
			delete(c.pendingStarts, eff.Round)
		case CloseRound:
			delete(c.pendingStarts, eff.Round)
			if r := c.rounds[eff.Round]; r != nil {
				c.closeRoundNow(now, r)
			}
		case ClosePath:
			if r := c.rounds[eff.Round]; r != nil && eff.Use != UseBoth {
				if pcID := r.globalPC[eff.Use]; pcID != 0 {
					c.emit(CoordPcClose{PCID: pcID, Reason: "engine_close_path"})
				}
			}
		case ResumeRound:
			if r := c.rounds[eff.Round]; r != nil && !r.ended {
				_ = c.stepSession(now, r, linkcore.PeerDiscovered{})
			}
		case SendSubscription:
			if r := c.rounds[eff.Round]; r != nil && !r.ended {
				_ = c.stepSession(now, r, linkcore.QueueControl{Type: 15, Body: encodeSubscription(eff.SessionIDs)})
			}
		}
	}
}

func (c *Coordinator) maybeNeedEntropy(minBytes int) {
	if minBytes <= 0 {
		minBytes = CoordEntropyRefillBytes
	}
	if !c.entropyAsked {
		c.entropyAsked = true
		c.emit(CoordNeedEntropy{MinRandomBytes: minBytes})
	}
}

func (c *Coordinator) maybeLowWater() {
	if !c.cfg.Supported {
		return
	}
	if !c.hasInstance || c.randPool.Avail() < CoordEntropyLowWater {
		c.maybeNeedEntropy(CoordEntropyRefillBytes)
	}
}

func (c *Coordinator) maybeNeedPendingEntropy() {
	need := 0
	if !c.hasInstance && c.randPool.Avail() < 48 {
		need = CoordEntropyRefillBytes
	}
	if c.pendingEngineUnits > 0 && c.randPool.Avail() < c.pendingEngineUnits*8 {
		need = max(need, max(CoordEntropyRefillBytes, c.pendingEngineUnits*8))
	}
	if len(c.pendingStarts) > 0 && (!c.hasInstance || c.randPool.Avail() < 16+peerwire.SealEntropy) {
		need = max(need, CoordEntropyRefillBytes)
	}
	for _, r := range c.rounds {
		if r != nil && !r.ended && r.session.NeedsEntropy() {
			need = max(need, CoordEntropyRefillBytes)
			break
		}
	}
	if c.cfg.Supported && c.randPool.Avail() < CoordEntropyLowWater {
		need = max(need, CoordEntropyRefillBytes)
	}
	if need > 0 {
		c.maybeNeedEntropy(need)
	}
}

func (c *Coordinator) onStartRound(now int64, eff StartRound) {
	if c.codec == nil {
		c.handleEngine(now, RoundEnded{Round: eff.Round})
		return
	}
	m, ok := c.members[eff.Machine]
	if !ok || !m.isMachine() {
		c.handleEngine(now, RoundEnded{Round: eff.Round})
		return
	}
	shared, dhOK := c.ensureAgree(eff.Machine, m.XPub)
	if !c.hasInstance || c.randPool.Avail() < 16+peerwire.SealEntropy {
		c.pendingStarts[eff.Round] = &pendingRoundStart{
			round:      eff.Round,
			machine:    eff.Machine,
			deadlineAt: eff.DeadlineAt,
			xPub:       m.XPub,
		}
		c.maybeNeedEntropy(CoordEntropyRefillBytes)
		return
	}
	if dhOK {
		c.startOutboundRound(now, eff.Round, m, shared, eff.DeadlineAt)
	} else {
		c.pendingStarts[eff.Round] = &pendingRoundStart{
			round:      eff.Round,
			machine:    eff.Machine,
			deadlineAt: eff.DeadlineAt,
			xPub:       m.XPub,
		}
	}
}

func (c *Coordinator) startOutboundRound(now int64, id RoundID, member CoordMember, shared []byte, deadlineAt int64) {
	conn := make([]byte, 16)
	if _, err := c.randPool.Read(conn); err != nil {
		c.pendingStarts[id] = &pendingRoundStart{
			round:      id,
			machine:    member.ID,
			deadlineAt: deadlineAt,
			xPub:       member.XPub,
		}
		c.maybeNeedEntropy(CoordEntropyRefillBytes)
		return
	}
	c.maybeLowWater()
	peer := peerwire.Public{
		ID:   []byte(member.ID),
		Role: 2,
		Sign: append([]byte(nil), member.EdPub[:]...),
		Box:  append([]byte(nil), member.XPub[:]...),
	}
	c.createRound(now, id, member.ID, peer, shared, conn, uint64(deadlineAt), true)
}

func (c *Coordinator) createRound(now int64, id RoundID, machine string, peer peerwire.Public, shared, conn []byte, deadline uint64, initiate bool) *coordRound {
	if c.codec == nil || len(shared) != 32 || len(conn) != 16 || !c.hasInstance {
		c.handleEngine(now, RoundEnded{Round: id})
		return nil
	}
	if deadline <= uint64(now) {
		c.handleEngine(now, RoundEnded{Round: id, TimedOut: true})
		return nil
	}
	r := &coordRound{
		id:            id,
		machine:       machine,
		head:          c.snap.Head,
		curNow:        now,
		conn:          append([]byte(nil), conn...),
		peer:          peer,
		shared:        append([]byte(nil), shared...),
		sentFrames:    map[string]struct{}{},
		localToGlobal: map[uint64]uint64{},
	}
	cfg := linkcore.SessionConfig{
		Self:       c.selfPub,
		Peer:       peer,
		Shared:     shared,
		Codec:      c.codec,
		Connection: append([]byte(nil), conn...),
		Deadline:   deadline,
		Entropy:    coordEntropyReader{pool: c.randPool},
		Allow: func() bool {
			return c.engine.SignalAuthorized(r.curNow, id)
		},
		Policy: func() bool {
			return c.engine.SignalAuthorized(r.curNow, id)
		},
	}
	sess, initial, err := linkcore.NewSession(cfg, initiate, uint64(now))
	if err != nil {
		c.handleEngine(now, RoundEnded{Round: id})
		return nil
	}
	r.session = sess
	c.rounds[id] = r
	c.roundsByConn[string(conn)] = id
	c.applySessionEffects(now, r, initial)
	c.syncMarks(now, r)
	c.maybeLowWater()
	return r
}

func (c *Coordinator) stepSession(now int64, r *coordRound, ev linkcore.Event) error {
	if r == nil || r.ended {
		return nil
	}
	r.curNow = now
	effects := r.session.Handle(ev, uint64(now))
	c.applySessionEffects(now, r, effects)
	if !r.ended && !r.bootNotified {
		if boot := r.session.Peer().Boot; len(boot) == 32 {
			r.bootNotified = true
			c.handleEngine(now, RoundBooted{Round: r.id, Boot: string(boot)})
		}
	}
	c.syncMarks(now, r)
	c.maybeLowWater()
	return nil
}

func (c *Coordinator) applySessionEffects(now int64, r *coordRound, effects []linkcore.Effect) {
	for _, eff := range effects {
		switch eff := eff.(type) {
		case linkcore.SignFrame:
			c.nextSignOp++
			opID := c.nextSignOp
			c.signOps[opID] = signOp{roundID: r.id, sessOp: eff.Op}
			c.emit(CoordKeySign{OpID: opID, Message: eff.Message})

		case linkcore.TransmitFrame:
			if !eff.Current(r.session.View()) {
				continue
			}
			if !c.engine.AllowSignal(now, r.id, len(eff.Raw)) {
				continue
			}
			fields, err := peerwire.Parse(eff.Raw, 5)
			if err != nil {
				continue
			}
			r.recordSentFrame(string(fields[4].B))
			c.emit(CoordSignalSend{
				ToDevice:   fields[2].B,
				FrameClass: uint32(fields[3].U),
				FrameID:    fields[4].B,
				Sealed:     fields[5].B,
				DeadlineMs: int64(eff.Deadline),
				Critical:   eff.Critical,
			})

		case linkcore.HandleControl:
			replyType, replyBody := c.decodeControl(now, r, eff)
			_ = c.stepSession(now, r, linkcore.ControlHandled{
				Op:   eff.Op,
				Type: replyType,
				Body: replyBody,
			})

		case linkcore.BeforeOffer, linkcore.PCChosen:
			c.handleEngine(now, RoundChosen{Round: r.id})

		case linkcore.CreatePC:
			u := Use(eff.Use)
			if u != UseRead && u != UseBulk {
				continue
			}
			c.nextPCID++
			pcID := c.nextPCID
			r.localToGlobal[eff.ID] = pcID
			r.globalPC[u] = pcID
			c.pcs[pcID] = &pcBinding{roundID: r.id, localID: eff.ID, use: u}
			r.anyPC = true
			r.attemptedUse[u] = true
			c.emit(CoordPcCreate{
				RoundID:    r.id,
				MachineID:  r.machine,
				PCID:       pcID,
				Use:        u,
				Generation: eff.Generation,
				STUN:       append([]string(nil), c.stunServers...),
			})

		case linkcore.DescribePC:
			pcID := r.localToGlobal[eff.ID]
			if pcID == 0 {
				continue
			}
			b := c.pcs[pcID]
			iceRestart := b != nil && b.use != 0 && r.session.View().Generation[b.use-1] > 1
			c.emit(CoordPcDescribe{PCID: pcID, Offer: eff.Offer, ICERestart: iceRestart})

		case linkcore.SetRemotePC:
			pcID := r.localToGlobal[eff.ID]
			if pcID == 0 {
				continue
			}
			c.emit(CoordPcSetRemote{PCID: pcID, SDP: eff.SDP, Offer: eff.Offer})

		case linkcore.AddCandidatesPC:
			pcID := r.localToGlobal[eff.ID]
			if pcID == 0 {
				continue
			}
			b := c.pcs[pcID]
			if b != nil {
				for _, cand := range eff.Candidates {
					c.handleEngine(now, DiagCandidate{Round: r.id, Use: b.use, Remote: true, Line: string(cand.Original)})
				}
			}
			c.emit(CoordPcAddCandidates{PCID: pcID, Candidates: eff.Candidates, End: eff.End})

		case linkcore.ConfirmPC:
			pcID := r.localToGlobal[eff.ID]
			if pcID == 0 {
				continue
			}
			u := Use(eff.Use)
			b := c.pcs[pcID]
			if b != nil {
				b.confirmed = true
			}
			r.confirmed[u] = true
			r.everReady = true
			c.emit(CoordPcConfirm{PCID: pcID, MachineID: r.machine, Use: u})
			c.handleEngine(now, PathReady{Round: r.id, Use: u, Path: pcID})
			if u == UseRead {
				c.maybeSendClientCapabilities(now, r)
				c.attachDirectTerminalsForMachine(now, r.machine)
			}

		case linkcore.ClosePC:
			pcID := r.localToGlobal[eff.ID]
			if pcID == 0 {
				continue
			}
			delete(r.localToGlobal, eff.ID)
			b := c.pcs[pcID]
			if b != nil {
				if r.globalPC[b.use] == pcID {
					r.globalPC[b.use] = 0
				}
				c.markPCClosed(now, r, b, pcID)
				delete(c.pcs, pcID)
			}
			c.emit(CoordPcClose{PCID: pcID, Reason: eff.Reason})

		case linkcore.PeerUnsupported:
			c.handleEngine(now, DiagSignal{Round: r.id, Note: NoteUnsupported})
			// Optional Class=2 control frames may legally receive Unsupported from older peers
			// without tearing down the direct round; only Class=1 signaling Unsupported retires.
			if eff.Class == 1 && eff.Use == 0 {
				r.roundUnsupp = true
				c.retireAndFailUnsupported(now, r)
			} else if eff.Class == 1 && (eff.Use == uint64(UseRead) || eff.Use == uint64(UseBulk)) {
				r.useUnsupp[eff.Use] = true
			} else if eff.Class == 2 && (eff.Type == linkcore.ControlAttachPath || eff.Type == linkcore.ControlInputStatus || eff.Type == linkcore.ControlBarrier) && eff.Stream != "" {
				c.onTerminalAttachUnsupported(now, r, eff.Stream)
			}

		case linkcore.SignalRejected:
			u := Use(eff.Use)
			if u != UseRead && u != UseBulk {
				u = UseRead
			}
			kind := SignalKind(int(eff.Type))
			if kind == "" {
				kind = "signal"
			}
			reason := eff.Reason
			if reason == "" {
				reason = "rejected"
			}
			c.handleEngine(now, DiagReject{Round: r.id, Use: u, Kind: kind, Reason: reason})

		case linkcore.RoundDone:
			// Readiness & retirement notifications are handled individually via ConfirmPC / SessionEnd.

		case linkcore.NeedEntropy:
			c.maybeNeedEntropy(max(CoordEntropyRefillBytes, eff.Bytes))

		case linkcore.SessionEnd:
			c.finishSession(now, r, eff.Reason)
		}
	}
}

func (c *Coordinator) retireAndFailUnsupported(now int64, r *coordRound) {
	if r.ended {
		return
	}
	more := r.session.Handle(linkcore.RetireRound{}, uint64(now))
	c.applySessionEffects(now, r, more)
	if !r.ended {
		c.handleEngine(now, RoundEnded{Round: r.id, Failure: FailurePeerUnsupported})
	}
}

func (c *Coordinator) finishSession(now int64, r *coordRound, reason string) {
	if r.ended {
		return
	}
	var failure Failure
	timedOut := reason == "round_deadline"
	allUsesUnsupp := false
	if !r.roundUnsupp && !r.everReady && (r.useUnsupp[UseRead] || r.useUnsupp[UseBulk]) {
		allAttempted := true
		anyAttempted := false
		for _, u := range []Use{UseRead, UseBulk} {
			if r.attemptedUse[u] {
				anyAttempted = true
				if !r.useUnsupp[u] {
					allAttempted = false
					break
				}
			}
		}
		allUsesUnsupp = allAttempted && (anyAttempted || r.useUnsupp[UseRead] || r.useUnsupp[UseBulk])
	}
	if r.roundUnsupp || allUsesUnsupp {
		failure = FailurePeerUnsupported
	} else if timedOut && !r.everReady {
		if !r.session.Booted() {
			failure = FailureNoResponse
		} else if r.anyPC && !r.srflxSeen {
			failure = FailureNoTraversal
		}
	}
	c.closeRoundNow(now, r)
	c.handleEngine(now, RoundEnded{Round: r.id, Failure: failure, TimedOut: timedOut})
}

func (c *Coordinator) closeRoundNow(now int64, r *coordRound) {
	if r.ended {
		return
	}
	r.ended = true
	delete(c.rounds, r.id)
	if c.roundsByConn[string(r.conn)] == r.id {
		delete(c.roundsByConn, string(r.conn))
	}
	for opID, so := range c.signOps {
		if so.roundID == r.id {
			delete(c.signOps, opID)
		}
	}
	for localID, pcID := range r.localToGlobal {
		delete(r.localToGlobal, localID)
		if b := c.pcs[pcID]; b != nil {
			c.markPCClosed(now, r, b, pcID)
			delete(c.pcs, pcID)
		}
		c.emit(CoordPcClose{PCID: pcID, Reason: "round_ended"})
	}
	r.globalPC = [3]uint64{}
}

func (c *Coordinator) syncMarks(now int64, r *coordRound) {
	if r.session == nil {
		return
	}
	cur := r.session.Marks()
	markKinds := [5]Mark{MarkBoot, MarkSDPLocal, MarkSDPRemote, MarkPeerConfirm, MarkReady}
	for i := 0; i < 2; i++ {
		u := Use(i + 1)
		for mIdx := 0; mIdx < 5; mIdx++ {
			if cur[i][mIdx] && !r.prevMarks[i][mIdx] {
				r.prevMarks[i][mIdx] = true
				c.handleEngine(now, DiagMark{Round: r.id, Use: u, Mark: markKinds[mIdx]})
			}
		}
	}
}

func (c *Coordinator) lookupPC(pcID uint64) (*pcBinding, *coordRound) {
	b := c.pcs[pcID]
	if b == nil {
		return nil, nil
	}
	r := c.rounds[b.roundID]
	if r == nil {
		return nil, nil
	}
	return b, r
}

func (c *Coordinator) allocPCID() uint64 {
	c.nextPCID++
	return c.nextPCID
}

func (c *Coordinator) sortedRoundIDs() []RoundID {
	out := make([]RoundID, 0, len(c.rounds))
	for id := range c.rounds {
		out = append(out, id)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func nonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
