package transport

import "testing"

// Guards the status snapshot the settings row, the sheet and the session
// bubble render (docs/transport.md §7).
func TestStatus(t *testing.T) {
	type want struct {
		state      State
		reason     Reason
		read, file Route
		readSlow   bool
		fileSlow   bool
	}
	check := func(name string, got Status, w want) {
		t.Helper()
		if got.State != w.state || got.Reason != w.reason || got.Read != w.read || got.File != w.file || got.ReadSlow != w.readSlow || got.FileSlow != w.fileSlow {
			t.Errorf("%s: %+v", name, got)
		}
	}

	u := NewEngine(false)
	u.Handle(0, SetEnabled{Machine: "m1", Enabled: false})
	check("no WebRTC, whatever else is set", u.Status(0, "m1"), want{state: StateUnsupported})

	r := newRig(t, 0.5)
	id := r.started("m1")
	check("a round without a ready path", r.e.Status(r.now, "m1"), want{state: StateConnecting})
	r.up(id, UseRead, UseBulk)
	check("ready paths are used at once", r.e.Status(r.now, "m1"), want{state: StateConnected, read: Direct, file: Direct})
	r.e.routing("m1").Fail(UseBulk, r.now)
	check("file fallback", r.e.Status(r.now, "m1"), want{state: StateConnected, read: Direct, fileSlow: true})
	r.e.routing("m1").Fail(UseRead, r.now)
	check("both fall back", r.e.Status(r.now, "m1"), want{state: StateConnected, readSlow: true, fileSlow: true})
	r.do(PathClosed{Round: id, Use: UseRead})
	check("a closed read path", r.e.Status(r.now, "m1"), want{state: StateConnected, fileSlow: true})

	r.do(SetEnabled{Machine: "m1", Enabled: false})
	check("switched off: nothing claimed in use", r.e.Status(r.now, "m1"), want{reason: ReasonDisabled})
	r.do(SetEnabled{Machine: "m1", Enabled: true})
	r.advance(FreshMs)
	check("a lapsed proof", r.e.Status(r.now, "m1"), want{reason: ReasonSecurityPending})
	r.do(MainLeg{Ready: false})
	r.sync()
	r.do(SetEnabled{Machine: "m1", Enabled: false})
	r.do(SetEnabled{Machine: "m1", Enabled: true})
	check("proven, waiting for the main connection", r.e.Status(r.now, "m1"), want{reason: ReasonStarting})
}

// Guards that numbers appear only once measured, and never as zero.
func TestStatusNumbers(t *testing.T) {
	r := newRig(t, 0.5)
	if m := r.e.Status(r.now, "m1").Measured; m != (Measured{}) {
		t.Fatal("nothing measured yet")
	}
	r.e.routing("m1").Observe(bulk("f"), Relay, 4<<20, 500, r.now)
	m := r.e.Status(r.now, "m1").Measured
	if !m.HasFileMiBps[Relay] || m.FileMiBps[Relay] != 8 || m.HasFileMiBps[Direct] || m.HasReadMs != [2]bool{} {
		t.Fatalf("%+v", m)
	}
}

// Guards the reason of a round that ended without a path, its retry note,
// that it survives until a path comes up, and the starting / at-cap reasons.
func TestStatusReasons(t *testing.T) {
	for _, tc := range []struct {
		f    Failure
		want Reason
	}{
		{FailurePeerUnsupported, ReasonPeerUnsupported},
		{FailureNoResponse, ReasonNoResponse},
		{FailureNoTraversal, ReasonNoTraversal},
		{FailureLost, ReasonLost},
		{FailureNone, ReasonFailed},
	} {
		r := newRig(t, 0.5)
		r.fail(r.started("m1"), tc.f)
		s := r.e.Status(r.now, "m1")
		if s.State != StateNone || s.Reason != tc.want || !s.RetryScheduled {
			t.Errorf("failure %d: %+v", tc.f, s)
		}
		r.advance(r.retryIn("m1"))
		if r.e.Status(r.now, "m1").RetryScheduled {
			t.Error("no retry note once it is due")
		}
		r.sync()
		next := r.started("m1")
		if s := r.e.Status(r.now, "m1"); s.State != StateConnecting {
			t.Errorf("a new round shows as connecting: %+v", s)
		}
		r.up(next, UseBulk)
		if s := r.e.Status(r.now, "m1"); s.Reason != ReasonNone || s.State != StateConnected {
			t.Errorf("a path coming up clears the reason: %+v", s)
		}
	}

	r := newRig(t, 0.5, "m1", "m2", "m3", "m4", "m5")
	if s := r.e.Status(r.now, "m5"); s.Reason != ReasonAtCap {
		t.Errorf("fifth computer: %+v", s)
	}
	r.do(SetEnabled{Machine: "m1", Enabled: false})
	r.do(SetEnabled{Machine: "m1", Enabled: true})
	if s := r.e.Status(r.now, "m1"); s.State != StateConnecting {
		t.Errorf("switched back on: %+v", s)
	}
	e := NewEngine(true)
	if s := e.Status(0, "m1"); s.Reason != ReasonSecurityPending {
		t.Errorf("before any sync: %+v", s)
	}

	// An established round whose paths all close reports ReasonLost (not ReasonFailed),
	// immediately records EndLost in diagnostics, and sets RetryScheduled only while delay > 0.
	rLost := newRig(t, 0.5)
	id := rLost.started("m1")
	rLost.up(id, UseRead)
	rLost.advance(4200)
	rLost.do(PathClosed{Round: id, Use: UseRead})
	s := rLost.e.Status(rLost.now, "m1")
	if s.State != StateNone || s.Reason != ReasonLost || !s.RetryScheduled {
		t.Fatalf("established then dropped: want StateNone/ReasonLost/RetryScheduled=true, got %+v", s)
	}
	rounds, _, _ := rLost.e.Diagnostics("m1")
	if len(rounds) == 0 || rounds[0].End != EndLost || !rounds[0].HasEstablished {
		t.Fatalf("expected immediate EndLost in diagnostics after last path closes, got %+v", rounds)
	}
	rLost.advance(rLost.retryIn("m1"))
	if s2 := rLost.e.Status(rLost.now, "m1"); s2.Reason != ReasonLost || s2.RetryScheduled {
		t.Fatalf("once retry is due, RetryScheduled must be false while ReasonLost remains, got %+v", s2)
	}
}
