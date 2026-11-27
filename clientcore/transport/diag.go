package transport

import (
	"strconv"
	"strings"
)

// Why a computer's direct path did not come up (docs/transport.md §6, §7),
// kept per computer for its last three rounds, in memory only.
//
// Privacy: an address is looked at once, to learn its type, family and range,
// and then dropped. No address, port, ICE credential, SDP or SDK error text is
// stored; everything kept is a small count, a state word or a time since the
// round began, in tables of bounded size.

// RoundID names one bounded attempt (a "round") to reach a computer directly,
// outbound or accepted from the computer's probe. The session layer maps it to
// its connection ID.
type RoundID uint64

// Family is an address family; FamilyOther covers mDNS names and garbage.
type Family uint8

const (
	FamilyOther Family = iota
	Family4
	Family6
)

// CandType is an ICE candidate type.
type CandType uint8

const (
	CandOther CandType = iota
	CandHost
	CandSrflx
	CandPrflx
	CandRelay
)

// AddrClass is an address range. v4: private, public, cgnat, link-local,
// loopback; v6: global, ula, link-local, loopback; other: mdns, other.
type AddrClass uint8

const (
	AddrOther AddrClass = iota
	AddrPrivate
	AddrPublic
	AddrCGNAT
	AddrLinkLocal
	AddrLoopback
	AddrGlobal
	AddrULA
	AddrMDNS
)

// CandKind is what is kept of a candidate.
type CandKind struct {
	Type   CandType
	Family Family
	Class  AddrClass
}

// Tally counts candidates by kind, at most diagMaxKeys kinds.
type Tally map[CandKind]int

// StateKind names a WebRTC state machine.
type StateKind uint8

const (
	StateGather StateKind = iota
	StateICE
	StateConn
	StateDTLS
	StateSCTP
	StateDC
	stateKinds
)

// Mark is a step of a peer connection, recorded once at its first moment.
type Mark uint8

const (
	MarkBoot Mark = iota
	MarkSDPLocal
	MarkSDPRemote
	MarkPeerConfirm
	MarkReady
	marks
)

// Stage is how far a peer connection got.
type Stage uint8

const (
	StageHandshake Stage = iota
	StageSDP
	StageICE
	StageDTLS
	StageChannel
	StageConfirm
	StageReady
)

// EndReason is why a round ended, as recorded.
type EndReason uint8

const (
	EndNone EndReason = iota
	EndTimeout
	EndNoResponse
	EndNoTraversal
	EndPeerUnsupported
	EndRetired
	EndCancelled
	EndLost
)

// CloseCause is a privacy-safe enum describing who/why a path or round closed.
type CloseCause uint8

const (
	CloseNone CloseCause = iota
	CloseProbeTimeout
	ClosePCFailed
	ClosePCDisconnected
	CloseDCClosed
	ClosePeerClosed
	CloseNetworkChanged
	CloseBackground
	CloseDisabled
	CloseEngine
	CloseTimeout
)

// ParseCloseCause maps a fixed shell or session close reason word into a CloseCause.
// Unknown or free-form strings return CloseNone so no raw error text is retained.
func ParseCloseCause(reason string) CloseCause {
	switch reason {
	case "probe_timeout":
		return CloseProbeTimeout
	case "pc_failed", "failed", "ice_failed":
		return ClosePCFailed
	case "pc_disconnected", "disconnected", "ice_disconnected":
		return ClosePCDisconnected
	case "dc_closed", "dc0_closed", "dc1_closed", "dc_send_failed", "probe_send_failed",
		"receive_error", "frame_oversize", "frame_overflow", "frame_conflict", "unexpected_dc":
		return CloseDCClosed
	case "peer_closed", "peer_closed_round", "closed_by_peer", "signal_close_action", "signal_closed":
		return ClosePeerClosed
	case "network_changed":
		return CloseNetworkChanged
	case "background":
		return CloseBackground
	case "disabled":
		return CloseDisabled
	case "round_deadline", "timeout":
		return CloseTimeout
	// pc_closed: the native PeerConnection reached CLOSED, which only a local close() does.
	case "engine", "engine_close_path", "round_ended", "core_close", "replaced", "retired", "pc_closed":
		return CloseEngine
	default:
		return CloseNone
	}
}

// ProbeStats holds privacy-safe liveness probe counters for one path.
type ProbeStats struct {
	Sent   int
	Echoed int
	Lost   int
	Missed int
}

// Verdict is the one sentence a round comes down to.
type Verdict uint8

const (
	VerdictNone Verdict = iota
	VerdictTrying
	VerdictConnected
	VerdictLost
	VerdictCancelled
	VerdictNoReply
	VerdictPeerRefused
	VerdictSignalLimited
	VerdictSignalOffline
	VerdictNoLocal
	VerdictNoSTUN
	VerdictNoRemote
	VerdictNoSDP
	VerdictIPv6Silent
	VerdictAllFailed
	VerdictUnselected
	VerdictDTLS
	VerdictChannel
	VerdictConfirm
	VerdictBudget
	VerdictCreateFailed
	VerdictUnknown
)

// Bounds of what is kept.
const (
	DiagRounds    = 3 // per computer
	DiagAttempts  = 3 // peer connections per use and round
	DiagSamples   = 6 // stats reads per attempt
	diagMaxStates = 40
	diagMaxKeys   = 24
	diagMaxPairs  = 12
	diagMaxErrors = 8
	diagMaxReject = 8
)

// StateEvent is a state change, timed from the round's start.
type StateEvent struct {
	T     int64
	Kind  StateKind
	Value string
}

// PairGroup sums candidate pairs of one local/remote kind.
type PairGroup struct {
	LocalType, RemoteType     CandType
	LocalFamily, RemoteFamily Family
	OK, Failed, Waiting       int
	Sent, Got                 int
	RTTMs                     int64
	HasRTT, Nominated         bool
}

// Rejection counts signals or candidates refused, by "kind:reason".
type Rejection struct {
	Key string
	N   int
}

// StunError counts a STUN/ICE server error by code and local family.
type StunError struct {
	Code   int
	Family Family
	N      int
}

// AttemptData is one peer connection of a use; a restart makes a new one.
type AttemptData struct {
	At       int64
	Local    Tally
	Remote   Tally
	Stun     []StunError
	States   []StateEvent
	Pairs    []PairGroup
	Samples  int
	Sampled  bool
	sampling bool
	last     [stateKinds]string
}

// PcData is one use over one round.
type PcData struct {
	Use        Use
	Marks      [marks]int64
	HasMark    [marks]bool
	Attempts   []*AttemptData
	Rejects    []Rejection
	Blocked    bool // the device could not make the connection
	ClosedAt   int64
	HasClosed  bool
	LivedMs    int64
	CloseCause CloseCause
	Probes     ProbeStats
}

// RoundData is one round with a computer.
type RoundData struct {
	N              int
	StartedAt      int64 // wall clock, only to say how long ago
	Established    int64
	HasEstablished bool
	EndedAt        int64
	End            EndReason
	CloseCause     CloseCause
	Unsupported    bool
	Limited        int
	Offline        int
	Pcs            [2]PcData
}

func (r *RoundData) since(now int64) int64 { return max(0, now-r.StartedAt) }

// Pc is the record of a use (1 loading, 2 files).
func (r *RoundData) Pc(u Use) *PcData {
	if u == UseBulk {
		return &r.Pcs[1]
	}
	return &r.Pcs[0]
}

// ---- masking ----

// ClassifyAddress gives an address's family and range; the address goes no further.
func ClassifyAddress(address string) (Family, AddrClass) {
	a := strings.ToLower(strings.TrimSpace(address))
	if i := strings.IndexByte(a, '%'); i >= 0 {
		a = a[:i]
	}
	if strings.HasSuffix(a, ".local") {
		return FamilyOther, AddrMDNS
	}
	if c, ok := classify4(a); ok {
		return Family4, c
	}
	if !strings.Contains(a, ":") {
		return FamilyOther, AddrOther
	}
	if v4, ok := strings.CutPrefix(a, "::ffff:"); ok && strings.Contains(v4, ".") {
		if c, ok := classify4(v4); ok {
			return Family4, c
		}
		return FamilyOther, AddrOther
	}
	if a == "::1" {
		return Family6, AddrLoopback
	}
	head := 0
	if !strings.HasPrefix(a, "::") {
		h, err := strconv.ParseUint(leadingHex(a), 16, 32)
		if err != nil {
			return FamilyOther, AddrOther
		}
		head = int(h)
	}
	switch {
	case head >= 0xfe80 && head <= 0xfebf:
		return Family6, AddrLinkLocal
	case head >= 0xfc00 && head <= 0xfdff:
		return Family6, AddrULA
	case head >= 0x2000 && head <= 0x3fff:
		return Family6, AddrGlobal
	}
	return Family6, AddrOther
}

// leadingHex is the hex digits a JS parseInt(x, 16) would read from the first group.
func leadingHex(a string) string {
	g, _, _ := strings.Cut(a, ":")
	n := 0
	for n < len(g) && strings.IndexByte("0123456789abcdef", g[n]) >= 0 {
		n++
	}
	return g[:n]
}

func classify4(a string) (AddrClass, bool) {
	parts := strings.Split(a, ".")
	if len(parts) != 4 {
		return 0, false
	}
	var o [4]int
	for i, p := range parts {
		if len(p) < 1 || len(p) > 3 {
			return 0, false
		}
		n, err := strconv.Atoi(p)
		if err != nil || n > 255 || p[0] == '+' || p[0] == '-' {
			return 0, false
		}
		o[i] = n
	}
	x, y := o[0], o[1]
	switch {
	case x == 10 || x == 172 && y >= 16 && y <= 31 || x == 192 && y == 168:
		return AddrPrivate, true
	case x == 100 && y >= 64 && y <= 127:
		return AddrCGNAT, true
	case x == 127:
		return AddrLoopback, true
	case x == 169 && y == 254:
		return AddrLinkLocal, true
	}
	return AddrPublic, true
}

func candType(s string) CandType {
	switch s {
	case "host":
		return CandHost
	case "srflx":
		return CandSrflx
	case "prflx":
		return CandPrflx
	case "relay":
		return CandRelay
	}
	return CandOther
}

// ClassifyCandidate reads an ICE candidate line (`candidate:… addr port typ type …`).
func ClassifyCandidate(line string) (CandKind, bool) {
	tokens := strings.Fields(strings.TrimPrefix(line, "a="))
	if len(tokens) < 8 || !strings.HasPrefix(tokens[0], "candidate:") {
		return CandKind{}, false
	}
	at := -1
	for i, t := range tokens {
		if t == "typ" {
			at = i
			break
		}
	}
	if at < 0 {
		return CandKind{}, false
	}
	k := CandKind{}
	if at+1 < len(tokens) {
		k.Type = candType(tokens[at+1])
	}
	k.Family, k.Class = ClassifyAddress(tokens[4])
	return k, true
}

func (t Tally) add(k CandKind) {
	if _, ok := t[k]; !ok && len(t) >= diagMaxKeys {
		k = CandKind{}
	}
	t[k]++
}

// Count is the number of candidates matching f (all when f is nil).
func (t Tally) Count(f func(CandKind) bool) int {
	n := 0
	for k, c := range t {
		if f == nil || f(k) {
			n += c
		}
	}
	return n
}

// CountClass returns the number of candidates whose address class matches cls.
func (t Tally) CountClass(cls AddrClass) int {
	n := 0
	for k, c := range t {
		if k.Class == cls {
			n += c
		}
	}
	return n
}

func diagWord(v string) string {
	if len(v) < 1 || len(v) > 32 {
		return "other"
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return "other"
		}
	}
	return v
}

// ---- recording ----

func (a *AttemptData) candidate(remote bool, line string) {
	if k, ok := ClassifyCandidate(line); ok {
		if remote {
			a.Remote.add(k)
		} else {
			a.Local.add(k)
		}
	}
}

func (a *AttemptData) stunError(code int, address string) {
	f := FamilyOther
	if address != "" {
		f, _ = ClassifyAddress(address)
	}
	code = max(0, code)
	for i := range a.Stun {
		if a.Stun[i].Code == code && a.Stun[i].Family == f {
			a.Stun[i].N++
			return
		}
	}
	if len(a.Stun) < diagMaxErrors {
		a.Stun = append(a.Stun, StunError{Code: code, Family: f, N: 1})
	}
}

func (a *AttemptData) state(t int64, k StateKind, v string) {
	if v == "" || k >= stateKinds || a.last[k] == v {
		return
	}
	a.last[k] = v
	if len(a.States) < diagMaxStates {
		a.States = append(a.States, StateEvent{T: t, Kind: k, Value: diagWord(v)})
	}
}

// StatsCandidate is a candidate as a stats report lists it.
type StatsCandidate struct {
	ID      string
	Type    string
	Address string
}

// StatsPair is a candidate pair as a stats report lists it.
type StatsPair struct {
	Local, Remote string
	State         string // succeeded, failed, or anything in progress
	Nominated     bool
	RTTSeconds    float64
	HasRTT        bool
	Sent, Got     int
}

// StatsReport is what the executor read from one getStats.
type StatsReport struct {
	Locals, Remotes []StatsCandidate
	Pairs           []StatsPair
	DTLS, DC        string // transport dtlsState and data channel 0's state, if seen
}

func (a *AttemptData) take(t int64, s StatsReport) {
	kinds := func(list []StatsCandidate) map[string]CandKind {
		m := map[string]CandKind{}
		for _, c := range list {
			k := CandKind{Type: candType(c.Type)}
			if c.Address != "" {
				k.Family, k.Class = ClassifyAddress(c.Address)
			}
			m[c.ID] = k
		}
		return m
	}
	locals, remotes := kinds(s.Locals), kinds(s.Remotes)
	a.state(t, StateDTLS, s.DTLS)
	a.state(t, StateDC, s.DC)
	var groups []PairGroup
	for _, p := range s.Pairs {
		l, r := locals[p.Local], remotes[p.Remote]
		i := 0
		for i < len(groups) && !(groups[i].LocalType == l.Type && groups[i].LocalFamily == l.Family && groups[i].RemoteType == r.Type && groups[i].RemoteFamily == r.Family) {
			i++
		}
		if i == len(groups) {
			if len(groups) >= diagMaxPairs {
				continue
			}
			groups = append(groups, PairGroup{LocalType: l.Type, LocalFamily: l.Family, RemoteType: r.Type, RemoteFamily: r.Family})
		}
		g := &groups[i]
		switch p.State {
		case "succeeded":
			g.OK++
			if p.Nominated {
				g.Nominated = true
			}
			if p.HasRTT {
				ms := int64(p.RTTSeconds*1000 + 0.5)
				if !g.HasRTT || ms < g.RTTMs {
					g.RTTMs, g.HasRTT = ms, true
				}
			}
		case "failed":
			g.Failed++
		default:
			g.Waiting++
		}
		g.Sent += max(0, p.Sent)
		g.Got += max(0, p.Got)
	}
	a.Pairs = groups
	a.Sampled = true
}

func (pc *PcData) begin(at int64) *AttemptData {
	a := &AttemptData{At: at, Local: Tally{}, Remote: Tally{}}
	pc.Attempts = append(pc.Attempts, a)
	if len(pc.Attempts) > DiagAttempts {
		pc.Attempts = pc.Attempts[1:]
	}
	return a
}

func (pc *PcData) current() *AttemptData {
	if len(pc.Attempts) == 0 {
		return nil
	}
	return pc.Attempts[len(pc.Attempts)-1]
}

func (pc *PcData) mark(t int64, m Mark) {
	if m < marks && !pc.HasMark[m] {
		pc.Marks[m], pc.HasMark[m] = t, true
	}
}

func (pc *PcData) recordClose(t int64, cause CloseCause, probes ProbeStats) {
	if probes.Sent > pc.Probes.Sent || (probes.Sent == pc.Probes.Sent &&
		(probes.Echoed > pc.Probes.Echoed || probes.Lost > pc.Probes.Lost || probes.Missed > pc.Probes.Missed)) {
		pc.Probes = probes
	}
	if !pc.HasClosed {
		pc.ClosedAt, pc.HasClosed = t, true
		if pc.HasMark[MarkReady] {
			pc.LivedMs = max(0, t-pc.Marks[MarkReady])
		}
	}
	if cause != CloseNone && (pc.CloseCause == CloseNone || (pc.CloseCause == CloseEngine && cause != CloseEngine)) {
		pc.CloseCause = cause
	}
}

func (r *RoundData) recordCloseCause(cause CloseCause, readyPath bool) {
	if cause == CloseNone {
		return
	}
	if r.CloseCause == CloseNone || (r.CloseCause == CloseEngine && cause != CloseEngine) {
		r.CloseCause = cause
		return
	}
	if readyPath && cause != CloseEngine {
		r.CloseCause = cause
	}
}

func rejectReason(r string) string {
	if len(r) < 1 || len(r) > 32 {
		return "other"
	}
	for i := 0; i < len(r); i++ {
		c := r[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == ' ') {
			return "other"
		}
	}
	return r
}

func (pc *PcData) reject(kind, reason string) {
	k := kind + ":" + rejectReason(reason)
	for i := range pc.Rejects {
		if pc.Rejects[i].Key == k {
			pc.Rejects[i].N++
			return
		}
	}
	if len(pc.Rejects) < diagMaxReject {
		pc.Rejects = append(pc.Rejects, Rejection{Key: k, N: 1})
	}
}

// SignalKind returns the diagnostics kind of a refused signal type.
func SignalKind(signalType int) string {
	switch signalType {
	case 3, 4:
		return "sdp"
	case 5:
		return "candidates"
	case 6:
		return "end"
	case 7:
		return "confirm"
	}
	return "signal"
}

func (r *RoundData) establish(now int64) {
	if !r.HasEstablished {
		r.Established, r.HasEstablished = r.since(now), true
	}
}

func (r *RoundData) finish(now int64, end EndReason) {
	if r.End == EndNone {
		r.End, r.EndedAt = end, r.since(now)
	}
}

// Diag is every computer's last rounds, and why a round was not even started.
type Diag struct {
	rings   map[string]*ring
	byRound map[RoundID]*RoundData
}

type ring struct {
	rounds    []*RoundData // newest first
	ids       []RoundID
	blockedAt int64
	blockedN  int
	count     int
}

func (d *Diag) ring(m string) *ring {
	if d.rings == nil {
		d.rings = map[string]*ring{}
		d.byRound = map[RoundID]*RoundData{}
	}
	r := d.rings[m]
	if r == nil {
		r = &ring{}
		d.rings[m] = r
	}
	return r
}

func (d *Diag) begin(m string, id RoundID, now int64) *RoundData {
	g := d.ring(m)
	g.count++
	r := &RoundData{N: g.count, StartedAt: now, Pcs: [2]PcData{{Use: UseRead}, {Use: UseBulk}}}
	g.rounds = append([]*RoundData{r}, g.rounds...)
	g.ids = append([]RoundID{id}, g.ids...)
	if len(g.rounds) > DiagRounds {
		delete(d.byRound, g.ids[DiagRounds])
		g.rounds, g.ids = g.rounds[:DiagRounds], g.ids[:DiagRounds]
	}
	d.byRound[id] = r
	return r
}

// blocked counts a round refused for lack of slots, in place, so refusals do
// not push real rounds out.
func (d *Diag) blocked(m string, now int64) {
	g := d.ring(m)
	g.blockedAt = now
	g.blockedN++
}

func (d *Diag) round(id RoundID) *RoundData { return d.byRound[id] }

// Rounds is a computer's last rounds, newest first.
func (d *Diag) Rounds(m string) []*RoundData {
	if g := d.rings[m]; g != nil {
		return g.rounds
	}
	return nil
}

// BlockedAt is when a round was last refused for lack of slots, if that is
// newer than the newest round.
func (d *Diag) BlockedAt(m string) (int64, bool) {
	g := d.rings[m]
	if g == nil || g.blockedN == 0 || len(g.rounds) > 0 && g.blockedAt <= g.rounds[0].StartedAt {
		return 0, false
	}
	return g.blockedAt, true
}

// ---- reading a round ----

func seen(a *AttemptData, k StateKind, values ...string) bool {
	if a == nil {
		return false
	}
	for _, s := range a.States {
		if s.Kind == k {
			for _, v := range values {
				if s.Value == v {
					return true
				}
			}
		}
	}
	return false
}

// StageOf is how far a use got in an attempt: the first step that did not happen.
func StageOf(pc *PcData, a *AttemptData) Stage {
	switch {
	case pc.HasMark[MarkReady]:
		return StageReady
	case !pc.HasMark[MarkBoot]:
		return StageHandshake
	case !pc.HasMark[MarkSDPLocal] || !pc.HasMark[MarkSDPRemote]:
		return StageSDP
	}
	encrypted := seen(a, StateConn, "connected") || seen(a, StateDTLS, "connected")
	// A pair that answered is not enough: until one is nominated the
	// connectivity checks have not chosen a path.
	if !encrypted && !seen(a, StateICE, "connected", "completed") && !anyPairNominated(a) {
		return StageICE
	}
	if !encrypted {
		return StageDTLS
	}
	if !seen(a, StateDC, "open") {
		return StageChannel
	}
	if !pc.HasMark[MarkPeerConfirm] {
		return StageConfirm
	}
	return StageReady
}

func anyPairOK(a *AttemptData) bool {
	if a == nil {
		return false
	}
	for _, p := range a.Pairs {
		if p.OK > 0 {
			return true
		}
	}
	return false
}

func anyPairNominated(a *AttemptData) bool {
	if a == nil {
		return false
	}
	for _, p := range a.Pairs {
		if p.Nominated {
			return true
		}
	}
	return false
}

// BestAttempt is the attempt that got furthest, then the one that saw the most
// addresses and checks; the latest of equals.
func BestAttempt(pc *PcData) *AttemptData {
	var best *AttemptData
	score := int64(-1)
	for _, a := range pc.Attempts {
		s := int64(StageOf(pc, a))*1e6 + int64(a.Local.Count(nil)+a.Remote.Count(nil)+len(a.Pairs))*1e3 + int64(len(a.States))
		if s >= score {
			best, score = a, s
		}
	}
	return best
}

// LeadPc is the use that got furthest (loading first when equal), with its
// best attempt and stage.
func LeadPc(r *RoundData) (*PcData, *AttemptData, Stage) {
	a, b := &r.Pcs[0], &r.Pcs[1]
	aa, ba := BestAttempt(a), BestAttempt(b)
	as, bs := StageOf(a, aa), StageOf(b, ba)
	if bs > as {
		return b, ba, bs
	}
	return a, aa, as
}

// VerdictOf is the sentence a round comes down to, from what this side saw.
// blocked: a newer round was refused for lack of slots.
func VerdictOf(r *RoundData, blocked bool) Verdict {
	switch {
	case blocked:
		return VerdictBudget
	case r == nil:
		return VerdictNone
	case r.HasEstablished:
		if r.End != EndNone {
			return VerdictLost
		}
		return VerdictConnected
	case r.End == EndNone:
		return VerdictTrying
	case r.End == EndCancelled:
		return VerdictCancelled
	case r.Unsupported || r.End == EndPeerUnsupported:
		return VerdictPeerRefused
	}
	trouble := VerdictNone
	if r.Limited > 0 {
		trouble = VerdictSignalLimited
	} else if r.Offline > 0 {
		trouble = VerdictSignalOffline
	}
	or := func(v Verdict) Verdict {
		if trouble != VerdictNone {
			return trouble
		}
		return v
	}
	_, a, stage := LeadPc(r)
	if stage == StageHandshake {
		return or(VerdictNoReply)
	}
	if a == nil && (r.Pcs[0].Blocked || r.Pcs[1].Blocked) {
		return VerdictCreateFailed
	}
	if stage == StageSDP {
		return or(VerdictNoSDP)
	}
	if a == nil || a.Local.Count(nil) == 0 {
		return VerdictNoLocal
	}
	// A pair that answered shows the addresses reach each other, so missing
	// mapped addresses are not what stopped the round.
	if !anyPairOK(a) && (r.End == EndNoTraversal || a.Local.Count(func(k CandKind) bool { return k.Type == CandSrflx }) == 0) {
		return VerdictNoSTUN
	}
	if a.Remote.Count(nil) == 0 {
		return or(VerdictNoRemote)
	}
	switch stage {
	case StageICE:
		if anyPairOK(a) {
			return VerdictUnselected
		}
		global6 := func(k CandKind) bool { return k.Family == Family6 && k.Class == AddrGlobal }
		sent, silent, any6 := false, true, false
		for _, p := range a.Pairs {
			if p.LocalFamily == Family6 && p.RemoteFamily == Family6 {
				any6 = true
				sent = sent || p.Sent > 0
				silent = silent && p.Got == 0 && p.OK == 0
			}
		}
		if a.Local.Count(global6) > 0 && a.Remote.Count(global6) > 0 && any6 && sent && silent {
			return VerdictIPv6Silent
		}
		return VerdictAllFailed
	case StageDTLS:
		return VerdictDTLS
	case StageChannel:
		return VerdictChannel
	case StageConfirm:
		return VerdictConfirm
	}
	return VerdictUnknown
}
