package transport

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Documentation addresses and a port nothing else uses, so a leak is visible.
const (
	L6, L4, LS = "2001:db8:1::5", "192.168.1.20", "203.0.113.7"
	R6, R4, RS = "2001:db8:2::9", "192.168.50.3", "198.51.100.4"
	port       = "54321"
)

func cline(ip, typ string, n int) string {
	return "candidate:" + strconv.Itoa(n) + " 1 udp 2113937151 " + ip + " " + port + " typ " + typ
}

func spair(l, r, state string, sent, got int, rtt float64) StatsPair {
	return StatsPair{Local: l, Remote: r, State: state, Sent: sent, Got: got, Nominated: state == "succeeded", RTTSeconds: rtt, HasRTT: rtt > 0}
}

func knownReport(pairs ...StatsPair) StatsReport {
	return StatsReport{
		Locals:  []StatsCandidate{{"L6", "host", L6}, {"L4", "host", L4}, {"LS", "srflx", LS}},
		Remotes: []StatsCandidate{{"R6", "host", R6}, {"R4", "host", R4}, {"RS", "srflx", RS}},
		Pairs:   pairs,
	}
}

// Guards the privacy rule: only type, family and range survive.
func TestClassifyAddress(t *testing.T) {
	for _, tc := range []struct {
		in string
		f  Family
		c  AddrClass
	}{
		{"10.1.2.3", Family4, AddrPrivate}, {"172.16.0.1", Family4, AddrPrivate}, {"172.31.255.1", Family4, AddrPrivate},
		{"172.32.0.1", Family4, AddrPublic}, {"192.168.0.9", Family4, AddrPrivate}, {"100.64.0.1", Family4, AddrCGNAT},
		{"100.127.255.254", Family4, AddrCGNAT}, {"100.128.0.1", Family4, AddrPublic}, {"203.0.113.7", Family4, AddrPublic},
		{"127.0.0.1", Family4, AddrLoopback}, {"169.254.3.4", Family4, AddrLinkLocal}, {"2001:db8::1", Family6, AddrGlobal},
		{"2a00:1450::1", Family6, AddrGlobal}, {"fd12:3456::1", Family6, AddrULA}, {"fc00::1", Family6, AddrULA},
		{"fe80::1%en0", Family6, AddrLinkLocal}, {"febf::1", Family6, AddrLinkLocal}, {"::1", Family6, AddrLoopback},
		{"::ffff:10.0.0.1", Family4, AddrPrivate}, {"abcd-1234.local", FamilyOther, AddrMDNS}, {"not an address", FamilyOther, AddrOther},
	} {
		if f, c := ClassifyAddress(tc.in); f != tc.f || c != tc.c {
			t.Errorf("%s: got %d/%d, want %d/%d", tc.in, f, c, tc.f, tc.c)
		}
	}
}

func TestClassifyCandidate(t *testing.T) {
	for _, tc := range []struct {
		line string
		want CandKind
		ok   bool
	}{
		{cline(L6, "host", 1), CandKind{CandHost, Family6, AddrGlobal}, true},
		{"a=" + cline(LS, "srflx", 1) + " raddr 0.0.0.0 rport 0", CandKind{CandSrflx, Family4, AddrPublic}, true},
		{cline("abcd.local", "host", 1), CandKind{CandHost, FamilyOther, AddrMDNS}, true},
		{cline(L4, "weird", 1), CandKind{CandOther, Family4, AddrPrivate}, true},
		{"garbage", CandKind{}, false},
		{"", CandKind{}, false},
	} {
		if got, ok := ClassifyCandidate(tc.line); got != tc.want || ok != tc.ok {
			t.Errorf("%q: got %+v %v", tc.line, got, ok)
		}
	}
}

// Guards that no address, port or error text is kept anywhere.
func TestDiagKeepsNoAddresses(t *testing.T) {
	var d Diag
	r := d.begin("mac", 1, 0)
	a := r.Pc(UseRead).begin(0)
	for _, x := range [][2]string{{L6, "host"}, {L4, "host"}, {LS, "srflx"}, {"abcd-1234.local", "host"}} {
		a.candidate(false, cline(x[0], x[1], 1))
	}
	for _, x := range [][2]string{{R6, "host"}, {R4, "host"}, {RS, "srflx"}} {
		a.candidate(true, cline(x[0], x[1], 1))
	}
	a.stunError(701, L4)
	r.Pc(UseRead).reject("browser", "bad candidate "+R6)
	r.Pc(UseRead).reject(SignalKind(5), "candidate cap "+R4)
	a.take(0, knownReport(spair("L6", "R6", "failed", 5, 0, 0), spair("L4", "R4", "succeeded", 5, 5, 0.02)))
	text := fmt.Sprint(a.Local, a.Remote, a.Stun, a.States, a.Pairs, r.Pcs[0].Rejects, r.Pcs[0].Marks, r.N, r.End)
	for _, secret := range []string{L6, L4, LS, R6, R4, RS, "abcd", port, "2001", "db8", "192.168", "203.0", "bad candidate"} {
		if strings.Contains(text, secret) {
			t.Errorf("kept %q", secret)
		}
	}
	want := Tally{{CandHost, Family6, AddrGlobal}: 1, {CandHost, Family4, AddrPrivate}: 1, {CandSrflx, Family4, AddrPublic}: 1, {CandHost, FamilyOther, AddrMDNS}: 1}
	if len(a.Local) != len(want) || a.Local.Count(nil) != 4 {
		t.Fatalf("local tally %v", a.Local)
	}
	for k, n := range want {
		if a.Local[k] != n {
			t.Errorf("local %+v = %d", k, a.Local[k])
		}
	}
	if len(a.Stun) != 1 || a.Stun[0] != (StunError{Code: 701, Family: Family4, N: 1}) {
		t.Errorf("stun %v", a.Stun)
	}
	if a.Pairs[1].RTTMs != 20 || !a.Pairs[1].Nominated {
		t.Errorf("pairs %+v", a.Pairs)
	}
}

// Guards the per-computer ring: three rounds, refusals counted in place.
func TestDiagRing(t *testing.T) {
	var d Diag
	for i := range 5 {
		d.begin("mac", RoundID(i+1), 1000)
	}
	var ns []int
	for _, r := range d.Rounds("mac") {
		ns = append(ns, r.N)
	}
	if !slices.Equal(ns, []int{5, 4, 3}) {
		t.Fatalf("rounds %v", ns)
	}
	if d.round(1) != nil || d.round(5) == nil {
		t.Fatal("a round pushed out of the ring is forgotten")
	}
	for range 50 {
		d.blocked("mac", 2000)
	}
	if len(d.Rounds("mac")) != DiagRounds || d.Rounds("other") != nil {
		t.Fatal("refusals must not push rounds out")
	}
	if at, ok := d.BlockedAt("mac"); !ok || at != 2000 {
		t.Fatal("a refusal newer than the newest round is reported")
	}
	d.begin("mac", 6, 3000)
	if _, ok := d.BlockedAt("mac"); ok {
		t.Fatal("a newer round hides the refusal")
	}
}

// Guards the size bounds of everything kept.
func TestDiagBounds(t *testing.T) {
	var d Diag
	pc := d.begin("mac", 1, 0).Pc(UseRead)
	var a *AttemptData
	for range 11 {
		a = pc.begin(0)
	}
	if len(pc.Attempts) != DiagAttempts {
		t.Fatal("attempts")
	}
	for i := range 200 {
		a.state(0, StateICE, []string{"failed", "checking"}[i%2])
	}
	if len(a.States) > diagMaxStates {
		t.Fatal("states")
	}
	types := []string{"host", "srflx", "prflx", "relay", "x"}
	for i := range 100 {
		a.candidate(false, "candidate:1 1 udp 1 10.0.0."+strconv.Itoa(i)+" 1 typ "+types[i%5])
		a.candidate(false, "candidate:1 1 udp 1 2001:db8::"+strconv.Itoa(i)+" 1 typ "+types[i%5])
	}
	if len(a.Local) > diagMaxKeys+1 || a.Local.Count(nil) != 200 {
		t.Fatalf("tally keys %d count %d", len(a.Local), a.Local.Count(nil))
	}
	for i := range 100 {
		a.stunError(300+i, L4)
		pc.reject("candidates", strings.Repeat("x", i%20+1))
	}
	if len(a.Stun) != diagMaxErrors || len(pc.Rejects) > diagMaxReject {
		t.Fatal("errors or rejections")
	}
	var s StatsReport
	for i := range 40 {
		id := "l" + strconv.Itoa(i)
		addr := "10.0.0.1"
		if i%2 == 0 {
			addr = "2001:db8::1"
		}
		s.Locals = append(s.Locals, StatsCandidate{id, types[i%4], addr})
		s.Remotes = append(s.Remotes, StatsCandidate{id, types[(i+1)%4], addr})
		s.Pairs = append(s.Pairs, spair(id, "l"+strconv.Itoa(i*7%40), "failed", 1, 0, 0))
	}
	a.take(0, s)
	if len(a.Pairs) > diagMaxPairs {
		t.Fatal("pairs")
	}
}

func TestDiagStateTiming(t *testing.T) {
	var d Diag
	r := d.begin("mac", 1, 1000)
	a := r.Pc(UseRead).begin(0)
	a.state(r.since(1400), StateICE, "checking")
	a.state(r.since(1400), StateICE, "checking")
	a.state(r.since(9000), StateICE, "failed")
	if len(a.States) != 2 || a.States[0] != (StateEvent{400, StateICE, "checking"}) || a.States[1].T != 8000 {
		t.Fatalf("states %+v", a.States)
	}
}

// shape is a round played through the recorder.
type shape struct {
	local, remote []string
	checks        []StatsPair
	ice           []string
	dtls, dc      string
	noBoot, noSDP bool
	confirm       bool
	stun          []int
	noSample      bool
}

func playRound(s shape, end EndReason, extra func(*RoundData)) *RoundData {
	var d Diag
	t := int64(0)
	r := d.begin("mac", 1, t)
	pc := r.Pc(UseRead)
	if !s.noBoot {
		pc.mark(0, MarkBoot)
		if !s.noSDP {
			pc.mark(0, MarkSDPLocal)
			pc.mark(0, MarkSDPRemote)
		}
	}
	if s.confirm {
		pc.mark(0, MarkPeerConfirm)
	}
	if !s.noBoot {
		a := pc.begin(0)
		for _, l := range s.local {
			a.candidate(false, l)
		}
		for _, l := range s.remote {
			a.candidate(true, l)
		}
		for _, c := range s.stun {
			a.stunError(c, L4)
		}
		for _, v := range s.ice {
			t += 100
			a.state(t, StateICE, v)
		}
		a.state(t, StateDTLS, s.dtls)
		a.state(t, StateDC, s.dc)
		if !s.noSample {
			a.take(t, knownReport(s.checks...))
		}
	}
	if extra != nil {
		extra(r)
	}
	if end != EndNone {
		r.finish(t+9900, end)
	}
	return r
}

var (
	v6local  = []string{cline(L6, "host", 1), cline(L4, "host", 2), cline(LS, "srflx", 3)}
	v6remote = []string{cline(R6, "host", 1), cline(R4, "host", 2), cline(RS, "srflx", 3)}
	v4local  = []string{cline(L4, "host", 1), cline(LS, "srflx", 2)}
	v4remote = []string{cline(R4, "host", 1), cline(RS, "srflx", 2)}
	failed   = []string{"checking", "failed"}
)

func ready(r *RoundData) { r.Pc(UseRead).mark(0, MarkReady); r.establish(0) }

// Guards the verdict a round comes down to (the sentence on the sheet).
func TestVerdictOf(t *testing.T) {
	for _, tc := range []struct {
		name string
		want Verdict
		r    *RoundData
	}{
		{"nothing recorded", VerdictNone, nil},
		{"a round still going", VerdictTrying, playRound(shape{local: v4local}, EndNone, nil)},
		{"connected", VerdictConnected, playRound(shape{local: v6local, remote: v6remote, ice: []string{"connected"}, dtls: "connected", dc: "open", confirm: true}, EndNone, ready)},
		{"connected then dropped", VerdictLost, playRound(shape{local: v6local, remote: v6remote}, EndLost, ready)},
		{"cancelled", VerdictCancelled, playRound(shape{local: v6local}, EndCancelled, nil)},
		{"never answered", VerdictNoReply, playRound(shape{noBoot: true}, EndNoResponse, nil)},
		{"said Unsupported", VerdictPeerRefused, playRound(shape{noBoot: true}, EndPeerUnsupported, func(r *RoundData) { r.Unsupported = true })},
		{"relay limited the handshake", VerdictSignalLimited, playRound(shape{noBoot: true}, EndTimeout, func(r *RoundData) { r.Limited++ })},
		{"computer offline", VerdictSignalOffline, playRound(shape{noBoot: true}, EndTimeout, func(r *RoundData) { r.Offline++ })},
		{"no description exchanged", VerdictNoSDP, playRound(shape{noSDP: true}, EndTimeout, nil)},
		{"no address of ours", VerdictNoLocal, playRound(shape{remote: v4remote, ice: failed}, EndTimeout, nil)},
		{"STUN gave no mapped address", VerdictNoSTUN, playRound(shape{local: []string{cline(L4, "host", 1), cline("x.local", "host", 2)}, remote: v4remote, ice: failed, stun: []int{701}}, EndNoTraversal, nil)},
		{"no mapped address without the session's verdict", VerdictNoSTUN, playRound(shape{local: []string{cline(L4, "host", 1)}, remote: v4remote, ice: failed}, EndTimeout, nil)},
		{"none of the computer's addresses", VerdictNoRemote, playRound(shape{local: v6local, ice: failed}, EndTimeout, nil)},
		{"IPv6 checks never answered", VerdictIPv6Silent, playRound(shape{local: v6local, remote: v6remote, ice: failed, checks: []StatsPair{spair("L6", "R6", "failed", 14, 0, 0), spair("L4", "R4", "failed", 12, 0, 0), spair("LS", "RS", "failed", 12, 0, 0)}}, EndTimeout, nil)},
		{"IPv6 answered but all failed", VerdictAllFailed, playRound(shape{local: v6local, remote: v6remote, ice: failed, checks: []StatsPair{spair("L6", "R6", "failed", 14, 3, 0), spair("L4", "R4", "failed", 12, 0, 0)}}, EndTimeout, nil)},
		{"IPv6 present, no pair sampled", VerdictAllFailed, playRound(shape{local: v6local, remote: v6remote, ice: failed, noSample: true}, EndTimeout, nil)},
		{"IPv4 only, all failed", VerdictAllFailed, playRound(shape{local: v4local, remote: v4remote, ice: failed, checks: []StatsPair{spair("L4", "R4", "failed", 12, 0, 0)}}, EndTimeout, nil)},
		{"pairs answered but none nominated, no mapped address either", VerdictUnselected, playRound(shape{local: []string{cline(L6, "host", 1)}, remote: v6remote, ice: []string{"checking"}, stun: []int{701}, checks: []StatsPair{{Local: "L6", Remote: "R6", State: "succeeded", Sent: 74, Got: 50}, spair("L4", "R4", "in-progress", 13, 0, 0)}}, EndNoTraversal, nil)},
		{"encryption never finished", VerdictDTLS, playRound(shape{local: v4local, remote: v4remote, ice: []string{"checking", "connected"}, dtls: "failed", checks: []StatsPair{spair("L4", "R4", "succeeded", 6, 6, 0.04)}}, EndTimeout, nil)},
		{"channel never opened", VerdictChannel, playRound(shape{local: v4local, remote: v4remote, ice: []string{"connected"}, dtls: "connected", dc: "connecting"}, EndTimeout, nil)},
		{"computer never confirmed", VerdictConfirm, playRound(shape{local: v4local, remote: v4remote, ice: []string{"connected"}, dtls: "connected", dc: "open"}, EndTimeout, nil)},
		{"device could not make a connection", VerdictCreateFailed, playRound(shape{}, EndTimeout, func(r *RoundData) { r.Pcs[0].Attempts = nil; r.Pcs[0].Blocked = true })},
	} {
		if got := VerdictOf(tc.r, false); got != tc.want {
			t.Errorf("%s: verdict %d, want %d", tc.name, got, tc.want)
		}
	}
	if VerdictOf(nil, true) != VerdictBudget || VerdictOf(playRound(shape{local: v4local}, EndTimeout, nil), true) != VerdictBudget {
		t.Error("a newer refusal says the slots were full")
	}
}

func TestLeadPcAndBestAttempt(t *testing.T) {
	var d Diag
	r := d.begin("mac", 1, 0)
	readPc, bulkPc := r.Pc(UseRead), r.Pc(UseBulk)
	readPc.mark(0, MarkBoot)
	bulkPc.mark(0, MarkBoot)
	bulkPc.mark(0, MarkSDPLocal)
	bulkPc.mark(0, MarkSDPRemote)
	if pc, _, _ := LeadPc(r); pc.Use != UseBulk {
		t.Fatal("the use that got furthest leads")
	}
	readPc.mark(0, MarkSDPLocal)
	readPc.mark(0, MarkSDPRemote)
	if pc, _, _ := LeadPc(r); pc.Use != UseRead {
		t.Fatal("loading leads on a tie")
	}

	first := readPc.begin(0)
	for _, l := range v6local {
		first.candidate(false, l)
	}
	for _, l := range v6remote {
		first.candidate(true, l)
	}
	first.state(0, StateICE, "checking")
	first.state(0, StateICE, "failed")
	readPc.begin(0).state(0, StateGather, "gathering")
	r.finish(0, EndTimeout)
	if _, a, stage := LeadPc(r); a != first || stage != StageICE || VerdictOf(r, false) != VerdictAllFailed {
		t.Fatal("judged by the attempt that got furthest, not the newest")
	}
}

func TestDiagPathEndAndPrivacy(t *testing.T) {
	// Free-form strings with IP addresses must map to CloseNone (never stored).
	if got := ParseCloseCause("error 192.168.1.20:54321 failed"); got != CloseNone {
		t.Fatalf("free-form reason must map to CloseNone, got %v", got)
	}
	for _, tc := range []struct {
		in   string
		want CloseCause
	}{
		{"probe_timeout", CloseProbeTimeout},
		{"pc_failed", ClosePCFailed},
		{"pc_disconnected", ClosePCDisconnected},
		{"dc0_closed", CloseDCClosed},
		{"peer_closed_round", ClosePeerClosed},
		{"network_changed", CloseNetworkChanged},
		{"background", CloseBackground},
		{"disabled", CloseDisabled},
		{"round_ended", CloseEngine},
		{"round_deadline", CloseTimeout},
	} {
		if got := ParseCloseCause(tc.in); got != tc.want {
			t.Errorf("ParseCloseCause(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}

	var d Diag
	r := d.begin("mac", 1, 1000)
	pc := r.Pc(UseRead)
	pc.mark(1000, MarkReady)
	r.establish(2000)
	pc.recordClose(5200, CloseProbeTimeout, ProbeStats{Sent: 5, Echoed: 2, Lost: 3, Missed: 3})
	// Subsequent generic engine close must not overwrite specific cause or survival time.
	pc.recordClose(9999, CloseEngine, ProbeStats{})
	if !pc.HasClosed || pc.LivedMs != 4200 || pc.CloseCause != CloseProbeTimeout {
		t.Fatalf("unexpected pc close record: %+v", pc)
	}
	if pc.Probes != (ProbeStats{Sent: 5, Echoed: 2, Lost: 3, Missed: 3}) {
		t.Fatalf("unexpected probe stats: %+v", pc.Probes)
	}
}
