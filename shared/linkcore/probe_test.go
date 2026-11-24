package linkcore

import "testing"

func TestProbeTrackerCadenceReplyAndTimeout(t *testing.T) {
	var tr ProbeTracker
	var seq byte
	alloc := func() ([16]byte, bool) {
		seq++
		var id [16]byte
		id[0] = seq
		return id, true
	}

	// Not ready: emits nothing, no due timer.
	if frame, timedOut := tr.Tick(1000, false, alloc); frame != nil || timedOut {
		t.Fatalf("expected idle when not ready, got frame=%v timedOut=%v", frame, timedOut)
	}
	if _, ok := tr.NextDue(false); ok {
		t.Fatal("expected no due time when not ready and no pending probes")
	}

	// First tick when ready sends immediately.
	frame1, timedOut := tr.Tick(1000, true, alloc)
	if timedOut || len(frame1) != ProbeFrameBytes || frame1[0] != 0 || frame1[1] != 1 {
		t.Fatalf("unexpected first probe: len=%d timedOut=%v", len(frame1), timedOut)
	}
	if due, ok := tr.NextDue(true); !ok || due != 2000 {
		t.Fatalf("expected due=2000, got %d (%v)", due, ok)
	}

	// Reply before timeout resets missed and returns RTT.
	reply1 := append([]byte(nil), frame1...)
	reply1[0] = 1
	rtt, ok := tr.OnReply(1042, reply1)
	if !ok || rtt != 42 {
		t.Fatalf("expected matched reply rtt=42, got ok=%v rtt=%v", ok, rtt)
	}
	if tr.PendingCount() != 0 || tr.Missed() != 0 {
		t.Fatalf("expected cleared state, pending=%d missed=%d", tr.PendingCount(), tr.Missed())
	}

	// Duplicate or malformed reply is ignored.
	if _, ok := tr.OnReply(1050, reply1); ok {
		t.Fatal("duplicate reply must not match")
	}
	if _, ok := tr.OnReply(1050, frame1); ok {
		t.Fatal("kind=0 request must not match as reply")
	}
	if _, ok := tr.OnReply(1050, []byte{1, 2, 3}); ok {
		t.Fatal("short frame must not match")
	}

	// Three consecutive unanswered probes trigger timedOut at the 3rd expiry.
	f2, timedOut := tr.Tick(2000, true, alloc)
	if timedOut || f2 == nil {
		t.Fatalf("expected probe 2 at t=2000")
	}
	f3, timedOut := tr.Tick(3000, true, alloc)
	if timedOut || f3 == nil || tr.Missed() != 1 {
		t.Fatalf("expected probe 3 and missed=1 at t=3000, got timedOut=%v missed=%d", timedOut, tr.Missed())
	}
	f4, timedOut := tr.Tick(4000, true, alloc)
	if timedOut || f4 == nil || tr.Missed() != 2 {
		t.Fatalf("expected probe 4 and missed=2 at t=4000, got timedOut=%v missed=%d", timedOut, tr.Missed())
	}
	f5, timedOut := tr.Tick(5000, true, alloc)
	if !timedOut || f5 != nil || tr.Missed() != 3 {
		t.Fatalf("expected timedOut=true at 3rd miss (t=5000), got timedOut=%v f5=%v missed=%d", timedOut, f5, tr.Missed())
	}
}

func TestProbeTrackerReplyResetsMissedStreak(t *testing.T) {
	var tr ProbeTracker
	var seq byte
	alloc := func() ([16]byte, bool) {
		seq++
		var id [16]byte
		id[0] = seq
		return id, true
	}

	_, _ = tr.Tick(1000, true, alloc)
	_, _ = tr.Tick(2000, true, alloc) // miss #1
	f3, timedOut := tr.Tick(3000, true, alloc)
	if timedOut || tr.Missed() != 2 {
		t.Fatalf("expected missed=2, got %d timedOut=%v", tr.Missed(), timedOut)
	}
	reply3 := append([]byte(nil), f3...)
	reply3[0] = 1
	if _, ok := tr.OnReply(3015, reply3); !ok {
		t.Fatal("expected probe 3 reply to match")
	}
	if tr.Missed() != 0 {
		t.Fatalf("expected missed reset to 0, got %d", tr.Missed())
	}
	// Another miss starts fresh from 1, not 3.
	_, _ = tr.Tick(4000, true, alloc)
	_, timedOut = tr.Tick(5000, true, alloc)
	if timedOut || tr.Missed() != 1 {
		t.Fatalf("expected missed=1 after reset, got %d timedOut=%v", tr.Missed(), timedOut)
	}
}
