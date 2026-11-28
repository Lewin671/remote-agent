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
	if st := tr.Stats(); st.Sent != 1 || st.Echoed != 1 || st.EchoMaxMs != 42 || st.Late != 0 || st.Unmatched != 0 || st.Lost != 0 || st.Missed != 0 || !st.HasLastEcho || st.LastEchoAt != 1042 {
		t.Fatalf("expected stats 1/1/0/0 max=42, got %+v", st)
	}

	// Duplicate reply counts as unmatched; malformed/kind=0 frames are ignored completely.
	if _, ok := tr.OnReply(1050, reply1); ok {
		t.Fatal("duplicate reply must not match")
	}
	if _, ok := tr.OnReply(1060, frame1); ok {
		t.Fatal("kind=0 request must not match as reply")
	}
	if _, ok := tr.OnReply(1070, []byte{1, 2, 3}); ok {
		t.Fatal("short frame must not match")
	}
	if st := tr.Stats(); st.Unmatched != 1 || st.LastEchoAt != 1050 {
		t.Fatalf("expected 1 unmatched reply at 1050, got %+v", st)
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
	if st := tr.Stats(); st.Sent != 4 || st.Echoed != 1 || st.Late != 0 || st.Unmatched != 1 || st.Lost != 3 || st.Missed != 3 {
		t.Fatalf("expected stats 4/1/0/1/3/3 after 3 misses, got %+v", st)
	}
	tr.Reset()
	if st := tr.Stats(); st != (ProbeStats{}) {
		t.Fatalf("expected zeroed stats after Reset, got %+v", st)
	}
}

func TestProbeTrackerLateAndUnmatchedDoNotAlterTimeout(t *testing.T) {
	var tr ProbeTracker
	var seq byte
	alloc := func() ([16]byte, bool) {
		seq++
		var id [16]byte
		id[0] = seq
		return id, true
	}
	mkReply := func(req []byte) []byte {
		r := append([]byte(nil), req...)
		r[0] = 1
		return r
	}

	// Probe 1 at t=1000: on-time echo in 13ms.
	f1, _ := tr.Tick(1000, true, alloc)
	if rtt, ok := tr.OnReply(1013, mkReply(f1)); !ok || rtt != 13 {
		t.Fatalf("expected on-time reply rtt=13, got ok=%v rtt=%v", ok, rtt)
	}
	// Probe 2 at t=2000, Probe 3 at t=3000 (expires #2 -> missed=1), Probe 4 at t=4000 (expires #3 -> missed=2).
	f2, _ := tr.Tick(2000, true, alloc)
	f3, _ := tr.Tick(3000, true, alloc)
	_, timedOut := tr.Tick(4000, true, alloc)
	if timedOut || tr.Missed() != 2 {
		t.Fatalf("expected missed=2, got %d timedOut=%v", tr.Missed(), timedOut)
	}

	// Late replies for #2 (1800ms) and #3 (1200ms) arrive before #4 expires.
	// Neither resets missed nor returns ok=true.
	if rtt, ok := tr.OnReply(3800, mkReply(f2)); ok || rtt != 0 {
		t.Fatalf("late reply must return ok=false, got ok=%v rtt=%v", ok, rtt)
	}
	if rtt, ok := tr.OnReply(4200, mkReply(f3)); ok || rtt != 0 {
		t.Fatalf("second late reply must return ok=false, got ok=%v rtt=%v", ok, rtt)
	}
	if tr.Missed() != 2 {
		t.Fatalf("late replies must NOT reset missed streak, got %d", tr.Missed())
	}

	// Duplicate of already-consumed late reply #2 counts as unmatched.
	if _, ok := tr.OnReply(4300, mkReply(f2)); ok {
		t.Fatal("duplicate late reply must not match")
	}

	// Probe 4 expires at t=5000 -> 3rd consecutive miss still triggers timedOut!
	if _, timedOut := tr.Tick(5000, true, alloc); !timedOut {
		t.Fatal("expected 3rd miss to trigger timedOut despite late replies")
	}
	st := tr.Stats()
	if st.Sent != 4 || st.Echoed != 1 || st.EchoMaxMs != 13 ||
		st.Late != 2 || st.LateMaxMs != 1800 || st.Unmatched != 1 ||
		st.Lost != 1 || st.Missed != 3 || !st.HasLastEcho || st.LastEchoAt != 4300 {
		t.Fatalf("unexpected stats: %+v", st)
	}
}

func TestProbeTrackerExpiredHistoryBoundedByCountAndAge(t *testing.T) {
	var tr ProbeTracker
	var seq byte
	alloc := func() ([16]byte, bool) {
		seq++
		var id [16]byte
		id[0] = seq
		return id, true
	}
	mkReply := func(req []byte) []byte {
		r := append([]byte(nil), req...)
		r[0] = 1
		return r
	}

	// Create 9 timed-out probes without hitting 3 consecutive misses (interleave on-time replies).
	var expiredFrames [][]byte
	now := int64(1000)
	for i := 0; i < 9; i++ {
		missFrame, _ := tr.Tick(now, true, alloc)
		expiredFrames = append(expiredFrames, missFrame)
		now += ProbeIntervalMs
		hitFrame, _ := tr.Tick(now, true, alloc) // expires missFrame
		if _, ok := tr.OnReply(now+10, mkReply(hitFrame)); !ok {
			t.Fatalf("expected hitFrame %d to match", i)
		}
		now += ProbeIntervalMs
	}
	// Because keep limit is 8 (and >10s entries are also pruned), the oldest frame (index 0, sent at t=1000)
	// must NOT match as late, while the newest expired frame (index 8) MUST match as late.
	if _, ok := tr.OnReply(now, mkReply(expiredFrames[0])); ok {
		t.Fatal("evicted expired probe must not match on-time")
	}
	if st := tr.Stats(); st.Late != 0 || st.Unmatched != 1 {
		t.Fatalf("expected oldest evicted probe to count as unmatched, got %+v", st)
	}
	if _, ok := tr.OnReply(now, mkReply(expiredFrames[8])); ok {
		t.Fatal("recent expired probe must not match on-time")
	}
	if st := tr.Stats(); st.Late != 1 || st.Unmatched != 1 {
		t.Fatalf("expected newest expired probe to count as late, got %+v", st)
	}

	// Entry older than 10s TTL is pruned and treated as unmatched.
	if _, ok := tr.OnReply(now+probeExpiredTTLMs+1, mkReply(expiredFrames[7])); ok {
		t.Fatal("TTL-expired probe must not match on-time")
	}
	if st := tr.Stats(); st.Late != 1 || st.Unmatched != 2 {
		t.Fatalf("expected >10s expired probe to count as unmatched, got %+v", st)
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
