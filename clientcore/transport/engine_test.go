package transport

import (
	"slices"
	"testing"
)

// rig drives an Engine with a fixed clock; the test plays the session layer
// and the shell by answering effects with events.
type rig struct {
	t   *testing.T
	e   *Engine
	now int64
	out []Effect
}

const lease = int64(1) << 50

func newRig(t *testing.T, unit float64, machines ...string) *rig {
	t.Helper()
	if len(machines) == 0 {
		machines = []string{"m1"}
	}
	r := &rig{t: t, e: NewEngine(true), now: 1_000_000}
	units := make([]float64, 4096)
	for i := range units {
		units[i] = unit
	}
	r.do(Entropy{Units: units})
	if !has[Authorize](r.do(MainLeg{Ready: true})) {
		t.Fatal("the main leg without a proof authorizes")
	}
	out := r.sync(machines...)
	r.out = append(out, r.do(AuthorizeDone{OK: true})...)
	return r
}

func (r *rig) do(ev Event) []Effect {
	r.out = r.e.Handle(r.now, ev)
	return r.out
}

func (r *rig) advance(ms int64) { r.now += ms }

func (r *rig) sync(machines ...string) []Effect {
	if len(machines) == 0 {
		machines = r.e.machines
	}
	return r.do(SecuritySynced{Member: true, Account: "acct", Head: "h1", StartedAt: r.now, LeaseNotAfter: lease, Machines: slices.Clone(machines)})
}

func of[T Effect](out []Effect) []T {
	var got []T
	for _, f := range out {
		if x, ok := f.(T); ok {
			got = append(got, x)
		}
	}
	return got
}

func has[T Effect](out []Effect) bool { return len(of[T](out)) > 0 }

// started is the one round the last step started for m.
func (r *rig) started(m string) RoundID {
	r.t.Helper()
	for _, s := range of[StartRound](r.out) {
		if s.Machine == m {
			return s.Round
		}
	}
	r.t.Fatalf("no round started for %s in %#v", m, r.out)
	return 0
}

// up makes the round chosen with the given uses ready.
func (r *rig) up(id RoundID, uses ...Use) {
	r.do(RoundChosen{Round: id})
	for _, u := range uses {
		r.do(PathReady{Round: id, Use: u, Path: uint64(id)*10 + uint64(u)})
	}
}

// fail ends a round that never came up.
func (r *rig) fail(id RoundID, f Failure) { r.do(RoundEnded{Round: id, Failure: f, TimedOut: true}) }

func (r *rig) connected(m string, uses ...Use) RoundID {
	r.t.Helper()
	id := r.started(m)
	r.up(id, uses...)
	return id
}

func (r *rig) retryIn(m string) int64 { return r.e.retryAfter[m] - r.now }

func announce(id RoundID, until int64, caps Caps) []Event {
	return []Event{
		CapabilitySnapshot{Round: id, Seq: 1, Caps: &caps, ExpiresAt: until},
		Capabilities{Round: id, Seq: 1, DownloadUntil: until},
	}
}

// Guards that a direct upload needs the unexpired snapshot and its exact
// limits, and that downloads do not depend on the upload snapshot.
func TestUploadNeedsSnapshotAndLimits(t *testing.T) {
	r := newRig(t, 0.5)
	id := r.connected("m1", UseBulk)
	for _, ev := range announce(id, r.now+60_000, Caps{FileUpload: true, MaxChunkBytes: 786432}) {
		r.do(ev)
	}
	up := Hint{Use: UseBulk, File: "t", Upload: UploadFile, Bytes: 786432}
	for _, tc := range []struct {
		h    Hint
		want bool
	}{
		{up, true},
		{Hint{Use: UseBulk, File: "t", Upload: UploadFile, Bytes: 786433}, false},
		{Hint{Use: UseBulk, File: "t", Upload: UploadImage, Bytes: 1}, false},
		{Hint{Use: UseBulk, File: "t", Upload: UploadFile}, false},
		{bulk("download"), true},
	} {
		if got := r.e.Ready(r.now, "m1", tc.h); got != tc.want {
			t.Errorf("%+v: ready %v", tc.h, got)
		}
	}
	r.do(CapabilitySnapshot{Round: id, Seq: 2, Caps: &Caps{FileUpload: true, MaxChunkBytes: 786432}, ExpiresAt: r.now - 1})
	if r.e.Ready(r.now, "m1", up) || !r.e.Ready(r.now, "m1", bulk("download")) {
		t.Fatal("an expired snapshot stops uploads only")
	}
	r.do(CapabilitySnapshot{Round: id, Seq: 3, Caps: &Caps{MaxChunkBytes: 786432}, ExpiresAt: r.now + 60_000})
	if r.e.Ready(r.now, "m1", up) {
		t.Fatal("a snapshot without file upload stops it")
	}
	r.do(Capabilities{Round: id, Seq: 2, DownloadUntil: 0})
	if r.e.Ready(r.now, "m1", bulk("download")) {
		t.Fatal("downloads stop when no longer offered")
	}
}

// Guards that a delayed snapshot cannot overwrite a newer decision.
func TestSnapshotOrdering(t *testing.T) {
	r := newRig(t, 0.5)
	id := r.connected("m1", UseBulk)
	snap := func(seq uint64, upload bool) {
		r.do(CapabilitySnapshot{Round: id, Seq: seq, Caps: &Caps{FileUpload: upload, MaxChunkBytes: 1}, ExpiresAt: r.now + 60_000})
	}
	snap(2, false)
	snap(1, true)
	if c, _ := r.e.Capabilities(r.now, "m1"); c.FileUpload {
		t.Fatal("an older snapshot was applied")
	}
	snap(3, true)
	if c, _ := r.e.Capabilities(r.now, "m1"); !c.FileUpload {
		t.Fatal("a newer snapshot was not applied")
	}
}

// Guards the gates a live path cannot bypass: freshness, the security head,
// and this device's switch (which is persisted before it counts).
func TestReadyGates(t *testing.T) {
	r := newRig(t, 0.5)
	r.connected("m1", UseRead)
	if !r.e.Ready(r.now, "m1", read) {
		t.Fatal("ready")
	}
	if r.e.Ready(r.now+FreshMs, "m1", read) {
		t.Fatal("a lapsed security proof stops the path")
	}
	r.do(SecuritySynced{Member: true, Account: "acct", Head: "h2", StartedAt: r.now, LeaseNotAfter: lease, Machines: []string{"m1"}})
	if r.e.Ready(r.now, "m1", read) || len(of[CloseRound](r.out)) != 1 {
		t.Fatal("a new head closes rounds of the old one")
	}
	id := r.started("m1")
	r.up(id, UseRead)
	out := r.do(SetEnabled{Machine: "m1", Enabled: false})
	if r.e.Ready(r.now, "m1", read) || !slices.Contains(out, Effect(PersistEnabled{Machine: "m1"})) || !has[CloseRound](out) {
		t.Fatalf("switching off persists, closes and stops: %#v", out)
	}
	if out := r.do(Tick{}); has[StartRound](out) {
		t.Fatal("no round while switched off")
	}
	reload := NewEngine(true)
	reload.Handle(r.now, PreferencesLoaded{Enabled: map[string]bool{"m1": false}})
	if reload.enabled("m1") || !reload.enabled("m2") {
		t.Fatal("a stored switch is restored; absent means on")
	}
	r.do(SetEnabled{Machine: "m1", Enabled: true})
	r.started("m1")
	r.do(SecurityRejected{})
	if len(r.e.peers) != 0 || r.e.current(r.now) {
		t.Fatal("a rejected sync invalidates")
	}
}

// Guards the per-source share of the inbound authentication queue.
func TestInboundSignalBudget(t *testing.T) {
	r := newRig(t, 0.5, "m1", "m2")
	n := 0
	for range 64 {
		if r.e.AdmitSignal(r.now, "m1", 100, 1) {
			n++
		}
	}
	if n != InboundPeerFrames || !r.e.AdmitSignal(r.now, "m2", 100, 1) {
		t.Fatalf("one computer took %d frames", n)
	}
	for _, tc := range []struct {
		src         string
		size, class int
	}{{"m2", InboundFrameMax + 1, 1}, {"m2", InboundClass2Max + 1, 2}, {"m2", 1, 3}, {"stranger", 1, 1}} {
		if r.e.AdmitSignal(r.now, tc.src, tc.size, tc.class) {
			t.Errorf("admitted %+v", tc)
		}
	}
	for range n {
		r.e.SignalDone("m1", 100)
	}
	if !r.e.AdmitSignal(r.now, "m1", 100, 1) || r.e.inbound.frames != 2 {
		t.Fatal("released frames free the share")
	}
	r.do(Lifecycle{Foreground: false})
	if r.e.AdmitSignal(r.now, "m2", 1, 1) {
		t.Fatal("nothing is opened in the background")
	}
}

// Guards the outgoing signaling rate (256 frames/s, 4 MiB/s).
func TestSignalSendRate(t *testing.T) {
	r := newRig(t, 0.5)
	id := r.started("m1")
	n := 0
	for range 300 {
		if r.e.AllowSignal(r.now, id, 10) {
			n++
		}
	}
	if n != SignalFramesPerSec {
		t.Fatalf("burst %d", n)
	}
	r.advance(500)
	if !r.e.AllowSignal(r.now, id, 10) || r.e.AllowSignal(r.now, id, SignalBytesPerSec) {
		t.Fatal("frames refill over time; bytes are capped at one second")
	}
	r.do(MainLeg{Ready: false})
	if r.e.AllowSignal(r.now, id, 1) {
		t.Fatal("no signal without the main connection")
	}
}

// Guards that a closed bulk path never displays as direct.
func TestFileRouteNeedsBulkPath(t *testing.T) {
	r := newRig(t, 0.5)
	id := r.connected("m1", UseRead, UseBulk)
	r.do(PathClosed{Round: id, Use: UseBulk})
	if r.e.FileRoute(r.now, "m1") != Relay || !r.e.Ready(r.now, "m1", read) {
		t.Fatal("file route follows the bulk path only")
	}
}

// Guards that an old round expiring or closing late leaves the current
// round, its backoff and its measured routes alone.
func TestOldRoundLeavesCurrent(t *testing.T) {
	r := newRig(t, 0.5)
	old := r.started("m1")
	r.do(RoundOffered{Machine: "m1", DeadlineAt: r.now + RoundMs})
	cur := of[AcceptRound](r.out)[0].Round
	r.up(cur, UseRead, UseBulk)
	if !r.e.rounds[old].retired {
		t.Fatal("the chosen round retires the other")
	}
	route := r.e.routing("m1")
	for i := range 20 {
		route.Observe(read, Relay, 0, 150, r.now+int64(i))
		route.Observe(read, Direct, 0, 20, r.now+int64(i))
	}
	r.e.retryAfter["m1"] = 123
	before := route.Measured(r.now)

	r.advance(RoundMs + RetainMs + 1)
	r.sync()
	r.do(RoundOffered{Machine: "m1", DeadlineAt: r.now + RoundMs}) // purges the expired old round
	if !slices.Contains(r.out, Effect(CloseRound{Round: old})) || r.e.peers["m1"].id != cur {
		t.Fatalf("expiry closes the old round only: %#v", r.out)
	}
	r.do(RoundEnded{Round: old})
	r.do(PathClosed{Round: old, Use: UseRead})
	if r.e.peers["m1"].id != cur || r.e.retryAfter["m1"] != 123 || route.Measured(r.now) != before {
		t.Fatal("late events of an old round changed the current one")
	}
	r.do(RoundEnded{Round: cur})
	if r.e.peers["m1"] != nil || r.e.routing("m1").Measured(r.now).HasReadMs[Direct] {
		t.Fatal("the current round's end clears its routes")
	}
}

// Guards that a completion reported after its round, path or lifetime
// changed is not counted.
func TestLateTicketIgnored(t *testing.T) {
	for _, kind := range []string{"round", "path", "invalidation", "switch"} {
		r := newRig(t, 0.5)
		id := r.connected("m1", UseRead, UseBulk)
		h := bulk("file")
		late := r.e.Track("m1", h)
		switch kind {
		case "round":
			r.do(RoundOffered{Machine: "m1", Boot: "b", DeadlineAt: r.now + RoundMs})
			next := of[AcceptRound](r.out)[0].Round
			r.do(RoundChosen{Round: next})
		case "path":
			r.do(PathReady{Round: id, Use: UseBulk, Path: 99})
		case "invalidation":
			r.do(NetworkChanged{})
			r.sync()
		case "switch":
			r.do(SetEnabled{Machine: "m1", Enabled: false})
			r.do(SetEnabled{Machine: "m1", Enabled: true})
		}
		if r.e.Failed(r.now, late) != nil || r.e.Observe(r.now, late, true, 4<<20, 10) != nil || r.e.routing("m1").FileSlow(r.now) {
			t.Errorf("%s: a late completion was counted", kind)
		}
		if cur := r.e.Track("m1", h); r.e.Failed(r.now, cur) == nil || !r.e.routing("m1").FileSlow(r.now) {
			t.Errorf("%s: a current failure must count", kind)
		}
	}
}

// Guards that a pending round cannot replace one already chosen, and that a
// round about to send its offer takes over.
func TestChosenRoundKept(t *testing.T) {
	r := newRig(t, 0.5)
	active := r.started("m1")
	r.do(RoundChosen{Round: active})
	r.do(RoundOffered{Machine: "m1", DeadlineAt: r.now + RoundMs})
	pending := of[AcceptRound](r.out)[0].Round
	if r.e.peers["m1"].id != active {
		t.Fatal("an offered round must not displace the chosen one")
	}
	r.do(RoundChosen{Round: pending})
	if r.e.peers["m1"].id != pending || !slices.Contains(r.out, Effect(RetireRound{Round: active})) {
		t.Fatal("the round sending its offer takes over and retires the other")
	}
	r.do(RoundChosen{Round: active})
	if r.e.peers["m1"].id != pending {
		t.Fatal("a retired round cannot be chosen again")
	}
}

// Guards a probe from a new computer process: its old rounds and their
// backoff go; a late probe from the same process does not retire an
// established round.
func TestOfferedRounds(t *testing.T) {
	r := newRig(t, 0.5)
	r.do(RoundOffered{Machine: "m1", Boot: "b1", DeadlineAt: r.now + RoundMs})
	est := of[AcceptRound](r.out)[0].Round
	r.up(est, UseRead)
	r.do(RoundOffered{Machine: "m1", Boot: "b1", DeadlineAt: r.now + RoundMs})
	if r.e.peers["m1"].id != est || !r.e.Ready(r.now, "m1", read) {
		t.Fatal("a late probe from the same process must not displace the established round")
	}
	r.e.failureCount["m1"] = 3
	r.do(RoundOffered{Machine: "m1", Boot: "b2", DeadlineAt: r.now + RoundMs})
	if !slices.Contains(r.out, Effect(CloseRound{Round: est})) || r.e.failureCount["m1"] != 0 || len(of[AcceptRound](r.out)) != 1 {
		t.Fatalf("a new process replaces the old rounds: %#v", r.out)
	}
	r.do(RoundOffered{Machine: "stranger", DeadlineAt: r.now + RoundMs})
	if has[AcceptRound](r.out) {
		t.Fatal("only member computers")
	}
}

// Guards the retry delay: full jitter below 1 s doubling to 30 s, drawn from
// the injected entropy.
func TestRetryBackoff(t *testing.T) {
	for _, tc := range []struct {
		unit float64
		want []int64
	}{
		{0.5, []int64{500, 1000, 2000, 4000, 8000, 15_000, 15_000, 15_000}},
		{1, []int64{1000, 2000, 4000, 8000, 16_000, 30_000, 30_000}},
		{0, []int64{0, 0}},
		{0.999, []int64{999}},
	} {
		r := newRig(t, tc.unit)
		var seen []int64
		for range tc.want {
			id := r.started("m1")
			r.fail(id, FailureNoResponse)
			seen = append(seen, r.retryIn("m1"))
			r.advance(r.retryIn("m1"))
			r.sync()
		}
		if !slices.Equal(seen, tc.want) {
			t.Errorf("unit %v: delays %v, want %v", tc.unit, seen, tc.want)
		}
	}
}

// Guards that the next round waits out the delay and starts on the tick after.
func TestRetryWaitsForDelay(t *testing.T) {
	r := newRig(t, 1)
	id := r.started("m1")
	r.do(Tick{}) // the tick is scheduled by the main leg
	r.fail(id, FailureNoResponse)
	r.advance(999)
	r.sync()
	if has[StartRound](r.out) {
		t.Fatal("still waiting")
	}
	r.advance(1)
	if !has[StartRound](r.do(Tick{})) {
		t.Fatal("the tick after the delay starts a round")
	}
}

// Guards that a path up for 30 s starts the sequence again and one that
// dropped sooner does not.
func TestRetryStableResets(t *testing.T) {
	r := newRig(t, 0.5)
	for range 3 {
		r.fail(r.started("m1"), FailureNone)
		r.advance(r.retryIn("m1"))
		r.sync()
	}
	for _, tc := range []struct{ lived, want int64 }{{5_000, 4000}, {31_000, 500}} {
		id := r.connected("m1", UseRead)
		r.advance(tc.lived)
		r.sync() // keeps the proof fresh; the round stays
		r.do(PathClosed{Round: id, Use: UseRead})
		if got := r.retryIn("m1"); got != tc.want {
			t.Errorf("lived %d: delay %d, want %d", tc.lived, got, tc.want)
		}
		r.advance(r.retryIn("m1"))
		r.sync()
	}
}

// Guards that a new start clears a pending wait: the main connection back, a
// network change, the switch turned on again.
func TestRetryWaitCleared(t *testing.T) {
	for _, kind := range []string{"main leg", "network", "switch", "foreground"} {
		r := newRig(t, 0.5)
		for range 6 {
			r.fail(r.started("m1"), FailureNone)
			r.advance(r.retryIn("m1"))
			r.sync()
		}
		r.fail(r.started("m1"), FailureNone)
		if r.retryIn("m1") <= 10_000 {
			t.Fatal("setup")
		}
		var out []Effect
		switch kind {
		case "main leg":
			out = r.do(MainLeg{Ready: true})
		case "network":
			r.do(NetworkChanged{})
			out = r.sync()
		case "switch":
			r.do(SetEnabled{Machine: "m1", Enabled: false})
			out = r.do(SetEnabled{Machine: "m1", Enabled: true})
		case "foreground":
			r.do(Lifecycle{Foreground: false})
			r.do(Lifecycle{Foreground: true})
			out = r.sync()
		}
		if !has[StartRound](out) {
			t.Errorf("%s: a round starts at once", kind)
		}
		if kind != "main leg" && r.e.failureCount["m1"] != 0 {
			t.Errorf("%s: the failure count starts over", kind)
		}
	}
}

// Guards that a round's end counts once however many of its paths close, and
// that a lost path redials after that same delay with direct preferred at once.
func TestRoundEndCountsOnceAndRedials(t *testing.T) {
	r := newRig(t, 0.5)
	id := r.started("m1")
	r.do(Tick{})
	r.up(id, UseRead, UseBulk)
	r.do(PathClosed{Round: id, Use: UseBulk})
	p := r.e.rounds[id]
	if p.redialAt-r.now != 500 || r.e.failureCount["m1"] != 1 || !r.e.Ready(r.now, "m1", read) {
		t.Fatal("one path closing schedules a redial; the other stays usable")
	}
	r.advance(500)
	out := r.do(Tick{})
	if !slices.Contains(out, Effect(RetireRound{Round: id})) || !has[StartRound](out) {
		t.Fatalf("redial retires the round and starts a new one: %#v", out)
	}
	next := r.started("m1")
	r.up(next, UseRead)
	if _, ok := r.e.Attempt(r.now, "m1", read); !ok {
		t.Fatal("a rebuilt path is preferred at once")
	}

	r2 := newRig(t, 0.5)
	id = r2.connected("m1", UseRead, UseBulk)
	r2.do(PathClosed{Round: id, Use: UseBulk})
	r2.do(PathClosed{Round: id, Use: UseRead})
	if r2.retryIn("m1") != 500 || r2.e.failureCount["m1"] != 1 || !slices.Contains(r2.out, Effect(RetireRound{Round: id})) {
		t.Fatal("the last path closing ends the round with the same delay, counted once")
	}
}

// Guards the foreground rule: background closes every round and forgets the
// proof; foreground syncs security before negotiating again.
func TestBackgroundAndForeground(t *testing.T) {
	r := newRig(t, 0.5)
	id := r.connected("m1", UseRead)
	out := r.do(Lifecycle{Foreground: false})
	if !slices.Contains(out, Effect(CloseRound{Round: id})) || !has[CancelTick](out) || r.e.Ready(r.now, "m1", read) || r.e.Ready(r.now, "m1", bulk("f")) {
		t.Fatalf("background closes direct: %#v", out)
	}
	if _, ok := r.e.AttemptInteractive(r.now, "m1"); ok {
		t.Fatal("no interactive direct leg in the background")
	}
	out = r.do(Lifecycle{Foreground: true})
	if !has[Authorize](out) || has[StartRound](out) || !has[ScheduleTick](out) {
		t.Fatalf("foreground authorizes first: %#v", out)
	}
	if out := r.do(AuthorizeDone{OK: false}); has[StartRound](out) {
		t.Fatal("a failed sync starts nothing")
	}
	if out := r.sync(); !has[StartRound](out) {
		t.Fatal("a fresh sync starts discovery")
	}
}

// Guards that the main connection coming back authorizes a stale proof
// before discovering, and discovers at once with a fresh one.
func TestMainLegAuthorizesWhenStale(t *testing.T) {
	r := newRig(t, 0.5)
	r.advance(FreshMs)
	out := r.do(MainLeg{Ready: true})
	if !has[Authorize](out) || has[StartRound](out) {
		t.Fatalf("stale: %#v", out)
	}
	r.do(AuthorizeDone{OK: true})
	r.sync()
	if p := r.e.peers["m1"]; p == nil || !r.e.active(p, r.now) {
		t.Fatal("the renewed proof carries the round on")
	}
	r.do(MainLeg{Ready: false})
	if r.e.legReady {
		t.Fatal("detached")
	}
}

// Guards that installing a different security record or switching a
// computer on with a stale proof authorizes before discovering, and that a
// new computer in the member list gets a round without a manual sync.
func TestInstallAndEnableReauthorize(t *testing.T) {
	r := newRig(t, 0.5)
	out := r.do(SecurityInstalled{Member: true, Account: "acct", Head: "h2"})
	if !has[Authorize](out) || has[StartRound](out) {
		t.Fatalf("install: %#v", out)
	}
	r.do(AuthorizeDone{OK: true})
	if out := r.do(SecurityInstalled{Member: true, Account: "acct", Head: "h1"}); len(of[Authorize](out)) != 0 {
		t.Fatal("the same record changes nothing")
	}
	r.sync("m1", "m2")
	if len(of[StartRound](r.out)) != 2 {
		t.Fatal("a newly paired computer gets a round")
	}
	r.advance(FreshMs)
	r.do(SetEnabled{Machine: "m1", Enabled: false})
	if out := r.do(SetEnabled{Machine: "m1", Enabled: true}); !has[Authorize](out) {
		t.Fatal("switching on with a lapsed proof authorizes")
	}
}

// Guards the proactive sync cadence: every 12 s, every 2 s within 7 s of
// expiry, without touching an established path.
func TestProactiveAuthorize(t *testing.T) {
	r := newRig(t, 0.5)
	r.connected("m1", UseRead)
	var at []int64
	for range 30 {
		r.advance(1000)
		if has[Authorize](r.do(Tick{})) {
			at = append(at, r.now)
			r.do(AuthorizeDone{OK: true})
		}
	}
	start := int64(1_000_000)
	want := []int64{start + 12_000, start + 14_000, start + 16_000, start + 18_000}
	if !slices.Equal(at[:4], want) {
		t.Fatalf("authorize at %v, want %v…", at, want)
	}
	r = newRig(t, 0.5)
	r.connected("m1", UseRead)
	r.advance(13_000)
	if !has[Authorize](r.do(Tick{})) {
		t.Fatal("due")
	}
	r.advance(1000)
	r.sync()
	r.do(AuthorizeDone{OK: true})
	r.advance(6000)
	r.do(Tick{})
	if !r.e.Ready(r.now, "m1", read) {
		t.Fatal("a renewed proof keeps the established path")
	}
}

// Guards that the interactive leg is offered whenever the read path is ready,
// even after a read fallback, and never otherwise.
func TestAttemptInteractive(t *testing.T) {
	r := newRig(t, 0.5)
	id := r.connected("m1", UseBulk)
	if _, ok := r.e.AttemptInteractive(r.now, "m1"); ok {
		t.Fatal("no read path")
	}
	r.do(PathReady{Round: id, Use: UseRead, Path: 1})
	r.e.routing("m1").Fail(UseRead, r.now)
	if _, ok := r.e.Attempt(r.now, "m1", read); ok {
		t.Fatal("reads fell back")
	}
	if tk, ok := r.e.AttemptInteractive(r.now, "m1"); !ok || tk.Round != id {
		t.Fatal("interactive commands still go both ways")
	}
	if AttemptTimeoutMs(r.now+10_000, r.now) != 3000 || AttemptTimeoutMs(r.now+5, r.now) != 5 || AttemptTimeoutMs(r.now-5, r.now) != 1 {
		t.Fatal("attempt timeout")
	}
}

// Guards 「重新尝试」: one round at once, only with no round running and the
// switch on, not twice within two seconds, failure count untouched.
func TestRetryNow(t *testing.T) {
	r := newRig(t, 0.5)
	id := r.started("m1")
	result := func() RetryNowResult { return of[RetryNowResult](r.out)[0] }
	if r.do(RetryNow{Machine: "m1"}); result().Started {
		t.Fatal("a round is running")
	}
	r.fail(id, FailureNone)
	r.e.retryAfter["m1"] = r.now + 30_000
	if r.do(RetryNow{Machine: "m1"}); !result().Started || !has[StartRound](r.out) || r.e.failureCount["m1"] != 1 {
		t.Fatal("starts at once and keeps the failure count")
	}
	r.fail(r.started("m1"), FailureNone)
	if r.do(RetryNow{Machine: "m1"}); result().Started {
		t.Fatal("too soon")
	}
	r.advance(2001)
	r.sync()
	r.fail(r.e.lastRound, FailureNone)
	if r.do(RetryNow{Machine: "m1"}); !result().Started {
		t.Fatal("after two seconds")
	}
	r.fail(r.e.lastRound, FailureNone)
	r.advance(2001)
	r.do(SetEnabled{Machine: "m1", Enabled: false})
	if r.do(RetryNow{Machine: "m1"}); result().Started {
		t.Fatal("switched off")
	}
	r.do(SetEnabled{Machine: "m1", Enabled: true})
	r.fail(r.e.lastRound, FailureNone)
	r.advance(FreshMs)
	if r.do(RetryNow{Machine: "m1"}); !result().Started || !has[Authorize](r.out) || has[StartRound](r.out) {
		t.Fatalf("a lapsed proof authorizes first: %#v", r.out)
	}
	if !has[StartRound](r.sync()) {
		t.Fatal("the round starts once the sync succeeds")
	}
	r.do(AuthorizeDone{OK: true})
	r.fail(r.e.lastRound, FailureNone)
	r.advance(2001)
	r.do(MainLeg{Ready: false})
	if r.do(RetryNow{Machine: "m1"}); result().Started {
		t.Fatal("no main connection")
	}
}

// Guards the four-computer cap: the fifth gets no round, and the refusal is
// recorded in place without pushing real rounds out.
func TestAtCap(t *testing.T) {
	r := newRig(t, 0.5, "m1", "m2", "m3", "m4", "m5")
	if n := len(of[StartRound](r.out)); n != MaxPeers {
		t.Fatalf("%d rounds", n)
	}
	if rounds, _, blocked := r.e.Diagnostics("m5"); len(rounds) != 0 || !blocked {
		t.Fatal("no round for the fifth, and the refusal is recorded")
	}
	r.fail(r.started("m1"), FailureNone)
	r.advance(1)
	r.do(Tick{})
	if !slices.ContainsFunc(of[StartRound](r.out), func(s StartRound) bool { return s.Machine == "m5" }) {
		t.Fatal("a freed slot goes to the waiting computer")
	}
	if _, _, blocked := r.e.Diagnostics("m5"); blocked {
		t.Fatal("a newer round hides the refusal")
	}
}

// Guards Control 15: sent only on a ready read path that advertised it, with
// at most four sessions, on every change, on main-leg return and capability
// refresh, and once per path however open/ready interleave.
func TestSubscription(t *testing.T) {
	r := newRig(t, 0.5)
	id := r.connected("m1", UseRead)
	subs := func() [][]string {
		var got [][]string
		for _, s := range of[SendSubscription](r.out) {
			got = append(got, s.SessionIDs)
		}
		return got
	}
	r.do(SetWatched{Sessions: map[string][]string{"m1": {"s1", "s2", "s3", "s4", "s5"}}})
	if len(subs()) != 0 {
		t.Fatal("not before Control 1 lists type 15")
	}
	r.do(Capabilities{Round: id, Seq: 1, EventPush: true})
	if got := subs(); len(got) != 1 || !slices.Equal(got[0], []string{"s1", "s2", "s3", "s4"}) {
		t.Fatalf("capped set %v", got)
	}
	for _, set := range [][]string{{"s2"}, {"s1", "s2", "s3", "s4"}, nil} {
		w := map[string][]string{}
		if set != nil {
			w["m1"] = set
		}
		r.do(SetWatched{Sessions: w})
		if got := subs(); len(got) != 1 || !slices.Equal(got[0], set) {
			t.Fatalf("change to %v sent %v", set, got)
		}
	}
	r.do(SetWatched{Sessions: map[string][]string{"m1": {"s1"}}})
	r.do(SetWatched{Sessions: map[string][]string{"m1": {"s1"}}})
	if len(subs()) != 0 {
		t.Fatal("the same set is not sent again")
	}
	r.do(MainLeg{Ready: true})
	if len(subs()) != 1 {
		t.Fatal("the main connection's return resends")
	}
	r.do(Capabilities{Round: id, Seq: 2, EventPush: true})
	if got := subs(); len(got) != 1 || !slices.Equal(got[0], []string{"s1"}) {
		t.Fatal("a capability refresh resends")
	}
	r.do(PathReady{Round: id, Use: UseRead, Path: uint64(id)*10 + 1})
	if len(subs()) != 0 {
		t.Fatal("the same path is not told twice")
	}
	r.do(PathReady{Round: id, Use: UseRead, Path: 77})
	if len(subs()) != 1 {
		t.Fatal("a replacement path is told")
	}

	r2 := newRig(t, 0.5)
	id = r2.started("m1")
	r2.do(RoundChosen{Round: id})
	r2.do(SetWatched{Sessions: map[string][]string{"m1": {"s-delayed"}}})
	r2.do(Capabilities{Round: id, Seq: 1, EventPush: true})
	if has[SendSubscription](r2.out) {
		t.Fatal("not before the read path is ready")
	}
	r2.do(PathReady{Round: id, Use: UseRead, Path: 5})
	if got := of[SendSubscription](r2.out); len(got) != 1 || got[0].SessionIDs[0] != "s-delayed" {
		t.Fatal("sent once the path is ready")
	}
}

// Guards which pushed events reach sync: read path of the current round, from
// the computer about itself.
func TestPushedEventAllowed(t *testing.T) {
	r := newRig(t, 0.5)
	id := r.connected("m1", UseRead)
	for _, tc := range []struct {
		u               Use
		machine, sender string
		want            bool
	}{
		{UseRead, "wrong", "m1", false}, {UseRead, "m1", "wrong", false}, {UseBulk, "m1", "m1", false}, {UseRead, "m1", "m1", true},
	} {
		if got := r.e.PushedEventAllowed(r.now, id, tc.u, tc.machine, tc.sender); got != tc.want {
			t.Errorf("%+v: %v", tc, got)
		}
	}
}

func TestStunServers(t *testing.T) {
	for _, tc := range []struct {
		host, conf string
		want       []string
	}{
		{"ra.example.com", "", []string{"stun:ra.example.com:3478"}},
		{"2001:db8::1", "", []string{"stun:[2001:db8::1]:3478"}},
		{"ra.example.com", "stun:stun.example.com:3478, stun:[2001:db8::2]:3478", []string{"stun:stun.example.com:3478", "stun:[2001:db8::2]:3478"}},
		{"ra.example.com", " , ", []string{"stun:ra.example.com:3478"}},
		{"ra.example.com", "stun:a:3478,stun:b:3478,stun:c:3478", []string{"stun:a:3478", "stun:b:3478"}},
	} {
		if got := StunServers(tc.host, tc.conf); !slices.Equal(got, tc.want) {
			t.Errorf("%q %q: %v", tc.host, tc.conf, got)
		}
	}
}

// Guards how the record ends a round: the session's cause at its deadline,
// cancelled when the engine closes it first, delivery refusals counted.
func TestDiagRoundEnds(t *testing.T) {
	for _, tc := range []struct {
		failure Failure
		boot    bool
		end     EndReason
		verdict Verdict
	}{
		{FailureNoResponse, false, EndNoResponse, VerdictNoReply},
		{FailureNoTraversal, true, EndNoTraversal, VerdictNoSDP},
	} {
		r := newRig(t, 0.5)
		id := r.started("m1")
		if tc.boot {
			r.do(DiagMark{Round: id, Use: UseRead, Mark: MarkBoot})
			r.do(DiagAttempt{Round: id, Use: UseRead})
		}
		r.advance(RoundMs + 1)
		r.fail(id, tc.failure)
		d := r.e.diag.round(id)
		if d.End != tc.end || VerdictOf(d, false) != tc.verdict || d.Pcs[0].HasMark[MarkBoot] != tc.boot {
			t.Errorf("failure %d: end %d verdict %d", tc.failure, d.End, VerdictOf(d, false))
		}
	}
	r := newRig(t, 0.5)
	id := r.started("m1")
	r.do(DiagSignal{Round: id, Note: NoteOffline})
	r.do(DiagSignal{Round: id, Note: NoteLimited})
	r.do(DiagSignal{Round: id, Note: NoteUnsupported})
	r.do(SetEnabled{Machine: "m1", Enabled: false})
	d := r.e.diag.round(id)
	if d.End != EndCancelled || d.Limited != 1 || d.Offline != 1 || !d.Unsupported {
		t.Fatalf("record %+v", d)
	}
	r.do(RoundEnded{Round: id, TimedOut: true})
	if d.End != EndCancelled {
		t.Fatal("the first end stays")
	}
}

// Guards the stats budget: one read at a time, DiagSamples per attempt, a new
// attempt starting its own count; the sheet asks for active rounds only.
func TestStatsReadBudget(t *testing.T) {
	r := newRig(t, 0.5)
	id := r.started("m1")
	if has[ReadStats](r.do(DiagSampleWanted{Round: id, Use: UseRead})) {
		t.Fatal("no attempt yet")
	}
	r.do(DiagAttempt{Round: id, Use: UseRead})
	n := 0
	for range 20 {
		n += len(of[ReadStats](r.do(DiagSampleWanted{Round: id, Use: UseRead})))
		n += len(of[ReadStats](r.do(DiagSampleWanted{Round: id, Use: UseRead})))
		r.do(DiagStats{Round: id, Use: UseRead, Report: &StatsReport{DC: "open"}})
	}
	if n != DiagSamples {
		t.Fatalf("%d reads", n)
	}
	r.do(DiagAttempt{Round: id, Use: UseRead})
	if out := r.do(RefreshDiagnostics{Machine: "m1"}); len(of[ReadStats](out)) != 1 {
		t.Fatalf("the sheet reads the new attempt: %#v", out)
	}
	r.do(DiagCandidate{Round: id, Use: UseRead, Line: cline(L4, "host", 1)})
	r.do(DiagState{Round: id, Use: UseRead, Kind: StateICE, Value: "checking"})
	r.do(DiagStunError{Round: id, Use: UseRead, Code: 701})
	r.do(DiagReject{Round: id, Use: UseRead, Kind: "browser", Reason: "OperationError"})
	r.do(DiagBlocked{Round: id, Use: UseBulk})
	d := r.e.diag.round(id)
	a := d.Pcs[0].current()
	if a.Local.Count(nil) != 1 || len(a.States) != 1 || len(a.Stun) != 1 || d.Pcs[0].Rejects[0].Key != "browser:OperationError" || !d.Pcs[1].Blocked {
		t.Fatal("events reach the record")
	}
	r.do(SetEnabled{Machine: "m1", Enabled: false})
	if has[ReadStats](r.do(RefreshDiagnostics{Machine: "m1"})) {
		t.Fatal("closed rounds are not read")
	}
}
