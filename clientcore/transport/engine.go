package transport

import "github.com/Lewin671/remote-agent/shared/linkcore"

// Engine limits and timings (docs/transport.md §3, §4, §7).
const (
	MaxPeers           = 4       // computers with a direct round at once
	MaxRounds          = 128     // rounds kept, retired ones included
	MaxRoundsPerPeer   = 32      //
	RoundMs            = 10_000  // a round's own deadline
	RetainMs           = 300_000 // a round's state outlives its deadline
	FreshMs            = 20_000  // a security sync proves the head this long
	TickMs             = 1_000   // upkeep while foreground with a main leg
	AuthorizeEveryMs   = 12_000  // proactive security sync cadence
	AuthorizeNearMs    = 7_000   // freshness left that counts as "near expiry"
	AuthorizeNearGapMs = 2_000   // cadence once near expiry
	RetryFirstMs       = 1_000   // round retry: full jitter below 1 s, 2 s, 4 s …
	RetryMaxMs         = 30_000  // … up to 30 s
	StableMs           = 30_000  // a path up this long starts the sequence again
	ManualGapMs        = 2_000   // 「重新尝试」 at most this often
	WatchedPerPeer     = 4       // sessions pushed per computer (Control 15)
	entropyLow         = 4
	entropyBatch       = 16
)

type pathState struct {
	id    uint64
	ready bool
}

// round is one attempt to reach a computer, outbound or accepted.
type round struct {
	_                 [0]func()
	id                RoundID
	machine           string
	head              string
	boot              string
	deadline          int64
	retainUntil       int64
	live              bool // false once the engine let go of it
	retired, ended    bool
	established       bool // some path came up
	selected          bool // won the pairing
	paths             [3]pathState
	readyAt           int64
	hasReadyAt        bool
	redialAt          int64
	hasRedial         bool
	retryDelay        int64
	hasRetryDelay     bool
	failure           Failure
	snapshotSeq       uint64
	snapshotUntil     int64
	caps              Caps
	hasCaps           bool
	downloadSeq       uint64
	downloadUntil     int64
	eventPush         bool
	subscribed        uint64
	subscribedPresent bool
}

// Engine owns this device's direct paths to every computer of the account.
// It never owns a request or its outcome: callers ask Ready/Attempt, send on
// the path they are given and report completions with a Ticket.
type Engine struct {
	supported  bool
	foreground bool
	legReady   bool
	ticking    bool

	member      bool
	proven      bool // a security sync has installed identity and codec
	account     string
	head        string
	machines    []string
	freshUntil  int64
	authorizeAt int64
	hasAuthAt   bool
	authorizing bool
	manualAt    int64
	hasManual   bool

	lifetime  uint64
	lastRound RoundID
	lastOwner uint64

	prefs        map[string]bool
	owners       map[string]uint64
	peers        map[string]*round // the selected round per computer
	rounds       map[RoundID]*round
	routes       map[string]*Routing
	retryAfter   map[string]int64
	failures     map[string]Failure // why the last round ended without a path
	failureCount map[string]int
	watched      map[string][]string

	units        []float64
	entropyAsked bool

	signal  linkcore.Tokens
	inbound inbound
	diag    Diag

	out     []Effect
	changed bool
}

// NewEngine makes an engine. supported is whether this device can make
// direct paths at all (the browser has WebRTC, the app links it).
func NewEngine(supported bool) *Engine {
	return &Engine{
		supported:    supported,
		foreground:   true,
		prefs:        map[string]bool{},
		owners:       map[string]uint64{},
		peers:        map[string]*round{},
		rounds:       map[RoundID]*round{},
		routes:       map[string]*Routing{},
		retryAfter:   map[string]int64{},
		failures:     map[string]Failure{},
		failureCount: map[string]int{},
		watched:      map[string][]string{},
	}
}

// Handle applies one event at now (ms) and returns the effects to run in order.
func (e *Engine) Handle(now int64, ev Event) []Effect {
	e.out, e.changed = nil, false
	switch ev := ev.(type) {
	case SetSupported:
		e.setSupported(now, ev.Supported)
	case Lifecycle:
		e.setForeground(now, ev.Foreground)
	case MainLeg:
		e.mainLeg(now, ev.Ready)
	case NetworkChanged:
		e.invalidate(now)
	case Tick:
		e.tick(now)
	case Entropy:
		e.units = append(e.units, ev.Units...)
		e.entropyAsked = false
	case PreferencesLoaded:
		for m, on := range ev.Enabled {
			e.prefs[m] = on
		}
		e.markChanged()
	case SetEnabled:
		e.setEnabled(now, ev.Machine, ev.Enabled)
	case RetryNow:
		e.retryNow(now, ev.Machine)
	case SetWatched:
		e.setWatched(now, ev.Sessions)
	case SecuritySynced:
		e.synced(now, ev)
	case SecurityRejected:
		e.invalidate(now)
	case SecurityInstalled:
		if !ev.Member || ev.Account != e.account || ev.Head != e.head {
			e.invalidate(now)
			if ev.Member && e.foreground && e.legReady {
				e.refreshAndDiscover(now)
			}
		}
	case AuthorizeDone:
		e.authorizing = false
		if ev.OK {
			e.discover(now)
		}
	case RoundOffered:
		e.offered(now, ev)
	case RoundChosen:
		e.chosen(now, ev.Round)
	case PathReady:
		e.pathReady(now, ev)
	case PathClosed:
		if p := e.rounds[ev.Round]; p != nil && ev.Use != UseBoth {
			p.paths[ev.Use].ready = false
			e.closed(now, p, ev.Use)
		}
	case RoundEnded:
		e.ended(now, ev)
	case CapabilitySnapshot:
		e.snapshot(now, ev)
	case Capabilities:
		e.capabilities(now, ev)
	default:
		e.handleDiag(now, ev)
	}
	if e.supported && len(e.units) < entropyLow && !e.entropyAsked {
		e.entropyAsked = true
		e.emit(RequestEntropy{Count: entropyBatch})
	}
	if e.changed {
		e.emit(Changed{})
	}
	out := e.out
	e.out = nil
	return out
}

func (e *Engine) emit(f Effect) { e.out = append(e.out, f) }
func (e *Engine) markChanged()  { e.changed = true }

// unit draws one jitter float; without entropy it takes the whole ceiling.
func (e *Engine) unit() float64 {
	if len(e.units) == 0 {
		return 1
	}
	u := e.units[0]
	e.units = e.units[1:]
	return max(0, min(u, 1))
}

func (e *Engine) newOwner(m string) {
	e.lastOwner++
	e.owners[m] = e.lastOwner
}

// ---- gates ----

func (e *Engine) enabled(m string) bool {
	on, ok := e.prefs[m]
	return !ok || on
}

// current: foreground with a security sync that still proves the head.
func (e *Engine) current(now int64) bool {
	return e.foreground && e.member && e.proven && now < e.freshUntil
}

// SignalAuthorized is the session layer's authorization for one signal step
// (offer, answer, candidates, confirm): inside the freshness window the
// security proof already covers it and no sync is asked for; once it lapses
// the step waits for Authorize and the SecuritySynced that follows (the
// Android authorizeUnlessFresh rule). The session layer must not sync per step.
func (e *Engine) SignalAuthorized(now int64, id RoundID) bool {
	p := e.rounds[id]
	return p != nil && e.valid(p, now)
}

func (e *Engine) valid(p *round, now int64) bool {
	return p.live && e.current(now) && p.head == e.head && e.enabled(p.machine)
}

func (e *Engine) active(p *round, now int64) bool {
	return !p.ended && !p.retired && e.valid(p, now)
}

func (e *Engine) routing(m string) *Routing {
	r := e.routes[m]
	if r == nil {
		r = &Routing{}
		e.routes[m] = r
	}
	return r
}

func (e *Engine) isMachine(m string) bool {
	for _, x := range e.machines {
		if x == m {
			return true
		}
	}
	return false
}

// ---- lifetime ----

func (e *Engine) setForeground(now int64, active bool) {
	if e.foreground == active {
		return
	}
	e.foreground = active
	if !active {
		e.cancelTick()
		e.hasAuthAt = false
		e.invalidate(now)
		return
	}
	if e.legReady {
		e.ensureTick()
	}
	clear(e.retryAfter)
	e.refreshAndDiscover(now)
}

func (e *Engine) mainLeg(now int64, ready bool) {
	e.legReady = ready
	if !ready || !e.foreground {
		return
	}
	e.ensureTick()
	// The computer can be reached again: a retry waiting out its delay need not.
	clear(e.retryAfter)
	for _, p := range e.sortedRounds() {
		e.emit(ResumeRound{Round: p.id})
	}
	for _, m := range e.machines {
		if p := e.peers[m]; p != nil {
			e.sendSubscription(now, p, false)
		}
	}
	e.refreshAndDiscover(now)
}

func (e *Engine) setSupported(now int64, supported bool) {
	if e.supported == supported {
		return
	}
	e.supported = supported
	e.markChanged()
	if !supported {
		e.cancelTick()
		e.hasAuthAt = false
		e.invalidate(now)
		return
	}
	if e.foreground && e.legReady {
		e.ensureTick()
		e.refreshAndDiscover(now)
	}
}

func (e *Engine) ensureTick() {
	if !e.supported {
		return
	}
	if !e.ticking {
		e.ticking = true
		e.emit(ScheduleTick{DelayMs: TickMs})
	}
}

func (e *Engine) cancelTick() {
	if e.ticking {
		e.ticking = false
		e.emit(CancelTick{})
	}
}

func (e *Engine) tick(now int64) {
	if !e.ticking {
		return
	}
	if !e.current(now) {
		if len(e.peers) > 0 {
			e.invalidate(now)
		}
	} else {
		e.discover(now)
	}
	if e.foreground && e.legReady && (e.freshUntil-now <= AuthorizeNearMs || !e.hasAuthAt || now-e.authorizeAt >= AuthorizeEveryMs) {
		e.authorize(now, false)
	}
	e.emit(ScheduleTick{DelayMs: TickMs})
}

// authorize asks for a security sync: at most every 12 s, every 2 s when the
// proof is about to lapse, at once when forced.
func (e *Engine) authorize(now int64, force bool) {
	if !e.supported || !e.foreground || !e.legReady || e.authorizing {
		return
	}
	if !force && e.hasAuthAt {
		gap := int64(AuthorizeEveryMs)
		if e.freshUntil-now <= AuthorizeNearMs {
			gap = AuthorizeNearGapMs
		}
		if now-e.authorizeAt < gap {
			return
		}
	}
	e.authorizing = true
	e.authorizeAt, e.hasAuthAt = now, true
	e.emit(Authorize{})
}

func (e *Engine) refreshAndDiscover(now int64) {
	if e.current(now) {
		e.discover(now)
	} else if e.legReady {
		e.authorize(now, true)
	}
}

func (e *Engine) synced(now int64, ev SecuritySynced) {
	if !ev.Member || now >= ev.StartedAt+FreshMs {
		e.invalidate(now)
		return
	}
	if ev.Head != e.head {
		e.lifetime++
		clear(e.owners)
		for _, p := range e.sortedRounds() {
			e.closeRound(now, p)
		}
		clear(e.peers)
		clear(e.retryAfter)
		clear(e.failureCount)
		for _, r := range e.routes {
			r.Reset(UseBoth)
		}
	}
	e.member, e.proven = true, true
	e.account, e.head = ev.Account, ev.Head
	e.machines = append(e.machines[:0], ev.Machines...)
	e.freshUntil = min(ev.StartedAt+FreshMs, ev.LeaseNotAfter)
	if !e.hasAuthAt {
		e.authorizeAt, e.hasAuthAt = now, true
	}
	e.markChanged()
	e.discover(now)
}

// invalidate forgets the security proof and every round; nothing old may
// delay or shape the next start.
func (e *Engine) invalidate(now int64) {
	e.freshUntil = 0
	e.lifetime++
	clear(e.owners)
	for _, p := range e.sortedRounds() {
		e.closeRound(now, p)
	}
	clear(e.peers)
	clear(e.failures)
	clear(e.retryAfter)
	clear(e.failureCount)
	for _, r := range e.routes {
		r.Reset(UseBoth)
	}
	e.markChanged()
}

func (e *Engine) setEnabled(now int64, m string, on bool) {
	e.emit(PersistEnabled{Machine: m, Enabled: on})
	e.prefs[m] = on
	e.lifetime++
	e.newOwner(m)
	for _, p := range e.sortedRounds() {
		if p.machine == m {
			e.closeRound(now, p)
		}
	}
	delete(e.peers, m)
	delete(e.failures, m)
	delete(e.retryAfter, m)
	delete(e.failureCount, m)
	e.routing(m).Reset(UseBoth)
	e.markChanged()
	if on && e.foreground {
		e.refreshAndDiscover(now)
	}
}

// retryNow is 「重新尝试」: one round at once through the same discovery,
// failure count untouched, at most every ManualGapMs. With a lapsed proof it
// authorizes first and the round starts once the sync succeeds.
func (e *Engine) retryNow(now int64, m string) {
	p := e.peers[m]
	if !e.supported || !e.foreground || !e.legReady || !e.enabled(m) || p != nil && e.active(p, now) || e.hasManual && now-e.manualAt < ManualGapMs {
		e.emit(RetryNowResult{Machine: m})
		return
	}
	e.manualAt, e.hasManual = now, true
	delete(e.retryAfter, m)
	e.refreshAndDiscover(now)
	e.emit(RetryNowResult{Machine: m, Started: true})
}

// ---- rounds ----

// discover starts a round with every computer that has none and is not
// waiting out a retry, up to MaxPeers computers.
func (e *Engine) discover(now int64) {
	if !e.supported || !e.current(now) || !e.legReady {
		return
	}
	for _, m := range e.machines {
		if prior := e.peers[m]; prior != nil && prior.hasRedial && now >= prior.redialAt && e.active(prior, now) {
			// A path of this round closed: a new bounded round after its delay.
			e.retire(prior)
			delete(e.peers, m)
			delete(e.retryAfter, m)
		}
		if p := e.peers[m]; !e.enabled(m) || p != nil && e.active(p, now) || now < e.retryAfter[m] {
			continue
		}
		if len(e.peers) >= MaxPeers {
			e.blocked(now, m)
			break
		}
		if p := e.create(now, m, now+RoundMs, ""); p != nil {
			e.emit(StartRound{Round: p.id, Machine: m, DeadlineAt: p.deadline})
		}
	}
}

func (e *Engine) create(now int64, m string, deadline int64, boot string) *round {
	for _, p := range e.sortedRounds() {
		// The session extends its own retention while the computer keeps
		// talking; here only rounds that are not a computer's current one expire.
		if now > p.retainUntil && e.peers[p.machine] != p {
			e.closeRound(now, p)
		}
	}
	if !e.current(now) || !e.enabled(m) {
		return nil
	}
	n := 0
	for _, p := range e.rounds {
		if p.machine == m {
			n++
		}
	}
	if len(e.rounds) >= MaxRounds || n >= MaxRoundsPerPeer || e.peers[m] == nil && len(e.peers) >= MaxPeers {
		e.blocked(now, m)
		return nil
	}
	e.lastRound++
	d := min(deadline, now+RoundMs)
	p := &round{id: e.lastRound, machine: m, head: e.head, boot: boot, deadline: d, retainUntil: d + RetainMs, live: true}
	e.rounds[p.id] = p
	e.began(now, p)
	e.markChanged()
	if prior := e.peers[m]; prior == nil || !prior.selected || !e.active(prior, now) {
		e.selectRound(p)
	}
	return p
}

func (e *Engine) selectRound(p *round) {
	if e.peers[p.machine] == p {
		return
	}
	if e.peers[p.machine] != nil {
		e.routing(p.machine).HardFail(UseBoth)
	}
	e.newOwner(p.machine)
	e.peers[p.machine] = p
}

func (e *Engine) retire(p *round) {
	if !p.retired {
		p.retired = true
		e.emit(RetireRound{Round: p.id})
	}
}

func (e *Engine) closeRound(now int64, p *round) {
	p.live = false
	delete(e.rounds, p.id)
	e.finished(now, p, false)
	e.emit(CloseRound{Round: p.id})
}

// sortedRounds lists rounds oldest first so effects come out in a stable order.
func (e *Engine) sortedRounds() []*round {
	out := make([]*round, 0, len(e.rounds))
	for id := RoundID(1); len(out) < len(e.rounds) && id <= e.lastRound; id++ {
		if p := e.rounds[id]; p != nil {
			out = append(out, p)
		}
	}
	return out
}

func (e *Engine) offered(now int64, ev RoundOffered) {
	if !e.legReady || !e.current(now) || !e.enabled(ev.Machine) || !e.isMachine(ev.Machine) {
		return
	}
	for _, p := range e.sortedRounds() {
		if p.machine == ev.Machine && p.boot != "" && p.boot != ev.Boot {
			// A new computer process: its old rounds are gone, and so is their backoff.
			e.closeRound(now, p)
			delete(e.retryAfter, ev.Machine)
			delete(e.failureCount, ev.Machine)
		}
	}
	if p := e.create(now, ev.Machine, ev.DeadlineAt, ev.Boot); p != nil {
		e.emit(AcceptRound{Round: p.id, Machine: ev.Machine, DeadlineAt: p.deadline})
	}
}

func (e *Engine) chosen(now int64, id RoundID) {
	p := e.rounds[id]
	if p == nil || !e.active(p, now) {
		return
	}
	p.selected = true
	e.selectRound(p)
	for _, old := range e.sortedRounds() {
		if old != p && old.machine == p.machine {
			e.retire(old)
		}
	}
}

func (e *Engine) pathReady(now int64, ev PathReady) {
	p := e.rounds[ev.Round]
	if p == nil || ev.Use == UseBoth {
		e.emit(ClosePath{Round: ev.Round, Use: ev.Use})
		return
	}
	p.established = true
	e.pathUp(now, p, ev.Use)
	if !e.active(p, now) || e.peers[p.machine] != p {
		e.emit(ClosePath{Round: ev.Round, Use: ev.Use})
		return
	}
	p.paths[ev.Use] = pathState{id: ev.Path, ready: true}
	if !p.hasReadyAt {
		p.readyAt, p.hasReadyAt = now, true
	}
	delete(e.failures, p.machine)
	e.routing(p.machine).Reset(ev.Use)
	if ev.Use == UseRead {
		e.sendSubscription(now, p, true)
	}
	e.markChanged()
}

func (e *Engine) ended(now int64, ev RoundEnded) {
	p := e.rounds[ev.Round]
	if p == nil {
		return
	}
	p.ended = true
	if p.failure == FailureNone {
		p.failure = ev.Failure
	}
	p.paths = [3]pathState{}
	delete(e.rounds, p.id)
	e.finished(now, p, ev.TimedOut)
	e.closed(now, p, UseBoth)
}

// closed handles a path (or, for UseBoth, the whole round) going away.
func (e *Engine) closed(now int64, p *round, u Use) {
	m := p.machine
	if e.peers[m] != p {
		return
	}
	if u != UseBoth && e.active(p, now) && p.established {
		if !p.hasRedial {
			p.redialAt, p.hasRedial = now+e.delay(now, p), true
		}
		e.routing(m).HardFail(u)
	}
	if !p.paths[UseRead].ready && !p.paths[UseBulk].ready {
		e.failures[m] = p.failure
		e.routing(m).HardFail(UseBoth)
		delete(e.peers, m)
		e.retryAfter[m] = now + e.delay(now, p)
		if !p.ended {
			// No path is left: this round has nothing more to offer.
			p.live = false
			e.retire(p)
		}
	}
	e.markChanged()
}

// delay is this round's retry delay, drawn once however many of its paths close.
// TODO(S4): the full-jitter ceiling is linkcore.Recovery.Failed's formula
// without its token buckets; use a linkcore export once linkcore settles.
func (e *Engine) delay(now int64, p *round) int64 {
	if p.hasRetryDelay {
		return p.retryDelay
	}
	if p.hasReadyAt && now-p.readyAt >= StableMs {
		delete(e.failureCount, p.machine)
	}
	n := e.failureCount[p.machine]
	e.failureCount[p.machine] = n + 1
	ceiling := min(int64(RetryMaxMs), int64(RetryFirstMs)<<min(n, 5))
	p.retryDelay, p.hasRetryDelay = int64(e.unit()*float64(ceiling)), true
	return p.retryDelay
}

// ---- capabilities and subscriptions ----

func (e *Engine) snapshot(now int64, ev CapabilitySnapshot) {
	p := e.rounds[ev.Round]
	if p == nil || !e.valid(p, now) || ev.Seq <= p.snapshotSeq || ev.Caps == nil {
		return
	}
	p.snapshotSeq = ev.Seq
	p.caps, p.hasCaps = *ev.Caps, true
	p.snapshotUntil = ev.ExpiresAt
	e.markChanged()
}

func (e *Engine) capabilities(now int64, ev Capabilities) {
	p := e.rounds[ev.Round]
	if p == nil || !e.valid(p, now) || ev.Seq <= p.downloadSeq {
		return
	}
	p.downloadSeq = ev.Seq
	p.downloadUntil = ev.DownloadUntil
	p.eventPush = ev.EventPush
	if p.eventPush {
		e.sendSubscription(now, p, false)
	}
	e.markChanged()
}

func (e *Engine) setWatched(now int64, sessions map[string][]string) {
	next := map[string][]string{}
	for m, ids := range sessions {
		if m == "" {
			continue
		}
		var kept []string
		for _, id := range ids {
			if id != "" && len(kept) < WatchedPerPeer {
				kept = append(kept, id)
			}
		}
		if len(kept) > 0 {
			next[m] = kept
		}
	}
	if sameWatched(e.watched, next) {
		return
	}
	e.watched = next
	for _, m := range e.machines {
		if p := e.peers[m]; p != nil {
			e.sendSubscription(now, p, false)
		}
	}
}

func sameWatched(a, b map[string][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for m, x := range a {
		y, ok := b[m]
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if x[i] != y[i] {
				return false
			}
		}
	}
	return true
}

// sendSubscription sends the computer's watched sessions over a ready read
// path that advertised Control 15; onlyIfMoved skips a path already told.
func (e *Engine) sendSubscription(now int64, p *round, onlyIfMoved bool) {
	path := p.paths[UseRead]
	if !e.active(p, now) || e.peers[p.machine] != p || !p.eventPush || !path.ready {
		return
	}
	if onlyIfMoved && p.subscribedPresent && p.subscribed == path.id {
		return
	}
	p.subscribed, p.subscribedPresent = path.id, true
	e.emit(SendSubscription{Round: p.id, SessionIDs: append([]string{}, e.watched[p.machine]...)})
}

// ---- queries for request routing ----

// Ready reports whether the computer's direct path for the hint can carry a
// request now: the round is current, the use's path is up, an upload fits the
// unexpired snapshot, and a download is still offered.
func (e *Engine) Ready(now int64, m string, h Hint) bool {
	p := e.peers[m]
	u := h.Use
	if u != UseBulk {
		u = UseRead
	}
	if p == nil || !e.active(p, now) || !p.paths[u].ready {
		return false
	}
	if u == UseBulk && h.Upload != UploadNone {
		caps, ok := e.Capabilities(now, m)
		allowed := caps.ImageUpload
		if h.Upload == UploadFile {
			allowed = caps.FileUpload
		}
		return ok && h.Bytes > 0 && h.Bytes <= caps.MaxChunkBytes && allowed
	}
	return u == UseRead || now < p.downloadUntil
}

// Capabilities is the computer's snapshot while it is valid.
func (e *Engine) Capabilities(now int64, m string) (Caps, bool) {
	p := e.peers[m]
	if p == nil || !e.valid(p, now) || !p.hasCaps || now >= p.snapshotUntil {
		return Caps{}, false
	}
	return p.caps, true
}

// FileRoute is where files to the computer go now: a closed bulk path never
// shows as direct, however healthy the read path is.
func (e *Engine) FileRoute(now int64, m string) Route {
	if p := e.peers[m]; p != nil && e.valid(p, now) && p.paths[UseBulk].ready {
		return e.routing(m).FileRoute(now)
	}
	return Relay
}

// Ticket ties a request's completion to the round and path it was sent on.
// A completion that arrives after either changed is not counted.
type Ticket struct {
	Machine  string
	Hint     Hint
	Round    RoundID
	Path     uint64
	lifetime uint64
	owner    uint64
	hasPeer  bool
}

// Track captures the current round and path for a request about to be sent.
func (e *Engine) Track(m string, h Hint) Ticket {
	t := Ticket{Machine: m, Hint: h, lifetime: e.lifetime}
	if _, ok := e.owners[m]; !ok {
		e.newOwner(m)
	}
	t.owner = e.owners[m]
	if p := e.peers[m]; p != nil {
		t.Round, t.hasPeer = p.id, true
		t.Path = p.paths[useOf(h)].id
	}
	return t
}

func useOf(h Hint) Use {
	if h.Use == UseBulk {
		return UseBulk
	}
	return UseRead
}

func (e *Engine) tracked(t Ticket) bool {
	p := e.peers[t.Machine]
	if t.lifetime != e.lifetime || e.owners[t.Machine] != t.owner || (p != nil) != t.hasPeer {
		return false
	}
	return p == nil || p.id == t.Round && p.paths[useOf(t.Hint)].id == t.Path
}

// Attempt chooses the route for a read or file request. ok is true when it
// goes direct; the ticket then reports its completion.
func (e *Engine) Attempt(now int64, m string, h Hint) (Ticket, bool) {
	if e.routing(m).Choose(h, e.Ready(now, m, h), now) != Direct {
		return Ticket{}, false
	}
	return e.Track(m, h), true
}

// AttemptInteractive is the direct leg of a dual-sent interactive command:
// whenever the read path is ready, without control sampling or fallback.
func (e *Engine) AttemptInteractive(now int64, m string) (Ticket, bool) {
	h := Hint{Use: UseRead}
	if !e.Ready(now, m, h) {
		return Ticket{}, false
	}
	return e.Track(m, h), true
}

// Observe records a verified completion of a tracked request.
func (e *Engine) Observe(now int64, t Ticket, direct bool, bytes int64, ms float64) []Effect {
	if !e.tracked(t) {
		return nil
	}
	p := Relay
	if direct {
		p = Direct
	}
	e.routing(t.Machine).Observe(t.Hint, p, bytes, ms, now)
	return []Effect{Changed{}}
}

// Failed records that a tracked direct request failed after it was handed off.
func (e *Engine) Failed(now int64, t Ticket) []Effect {
	if !e.tracked(t) {
		return nil
	}
	e.routing(t.Machine).Fail(useOf(t.Hint), now)
	return []Effect{Changed{}}
}

// AttemptTimeoutMs bounds one direct attempt: at most 3 s, never past the
// request's own deadline, at least 1 ms.
func AttemptTimeoutMs(deadline, now int64) int64 { return max(1, min(3000, deadline-now)) }

// PushedEventAllowed reports whether an event envelope that arrived on a
// round's path may be handed to sync: only on the read path of the
// computer's current round, from that computer about itself.
func (e *Engine) PushedEventAllowed(now int64, id RoundID, u Use, machine, sender string) bool {
	p := e.rounds[id]
	return p != nil && e.valid(p, now) && u == UseRead && e.peers[p.machine] == p && machine == p.machine && sender == p.machine
}
