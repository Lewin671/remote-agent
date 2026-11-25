package linkcore

import "testing"

func TestRecoveryBudgetsSurviveHintsAndReady(t *testing.T) {
	now := int64(1000)
	global := &Budget{}
	var a, b, c Recovery
	if !a.Take(now, global) || !a.Take(now, global) || a.Take(now, global) {
		t.Fatal("target burst")
	}
	a.Ready()
	if a.Take(now, global) {
		t.Fatal("Ready refilled target")
	}
	if !b.Take(now, global) || !b.Take(now, global) || c.Take(now, global) {
		t.Fatal("process burst")
	}
	if c.DelayMs(now, global) != 500 {
		t.Fatal("process refill rate")
	}
	if !c.Take(now+500, global) || a.Take(now+500, global) {
		t.Fatal("atomic budgets")
	}
	if !a.Take(now+1000, global) {
		t.Fatal("target refill")
	}
}
func TestRecoveryFullJitterDoesNotMoveForNotifications(t *testing.T) {
	now := int64(1000)
	var r Recovery
	b := &Budget{}
	for _, cap := range []int64{1, 2, 4, 8, 16, 30, 30} {
		r.Failed(now, .5)
		want := cap * 1000 / 2
		for range 1000 {
			if got := r.DelayMs(now, b); got != want {
				t.Fatalf("delay %v want %v", got, want)
			}
		}
		now += want
	}
	r.Ready()
	r.Failed(now, .5)
	if r.DelayMs(now, b) != 500 {
		t.Fatal("authenticated recovery did not reset failure sequence")
	}
}

func TestLivenessThresholds(t *testing.T) {
	l := NewLiveness(0)
	if got := l.ThresholdMs(); got != 60000 {
		t.Fatalf("initial threshold = %v, want 60s", got)
	}
	if l.Suspect(59000) {
		t.Fatal("suspected before 60s with 0 heartbeats")
	}
	l.OnFrame(1000, true)
	if got := l.ThresholdMs(); got != 60000 {
		t.Fatalf("after 1 heartbeat threshold = %v, want 60s", got)
	}
	if l.Suspect(59000) {
		t.Fatal("suspected before 60s with 1 heartbeat")
	}
	if !l.Suspect(61000) {
		t.Fatal("did not suspect at 60s silence with <2 heartbeats")
	}

	// 1s heartbeat cadence -> floor SuspectAfterMs = 15s (tolerates high-RTT/lossy stalls).
	l.OnFrame(2000, true)
	if got := l.ThresholdMs(); got != SuspectAfterMs {
		t.Fatalf("1s cadence threshold = %v, want %v", got, SuspectAfterMs)
	}
	if l.Suspect(2000 + SuspectAfterMs - 1) {
		t.Fatal("suspected too early before 15s floor")
	}
	if !l.Suspect(2000 + SuspectAfterMs) {
		t.Fatal("did not suspect at 15s floor")
	}

	// 20s legacy server cadence -> max(15s, 3*20s) = 60s
	legacy := NewLiveness(0)
	legacy.OnFrame(20000, true)
	legacy.OnFrame(40000, true)
	if got := legacy.ThresholdMs(); got != 60000 {
		t.Fatalf("20s cadence threshold = %v, want 60s", got)
	}
	if legacy.Suspect(99000) {
		t.Fatal("20s legacy server falsely suspected before 60s")
	}
	if !legacy.Suspect(100000) {
		t.Fatal("20s legacy server not suspected at 60s")
	}

	// Sub-second bursts still respect 15s minimum floor.
	burst := NewLiveness(0)
	burst.OnFrame(100, true)
	burst.OnFrame(200, true)
	if got := burst.ThresholdMs(); got != SuspectAfterMs {
		t.Fatalf("burst floor threshold = %v, want %v", got, SuspectAfterMs)
	}
}

func TestLivenessAdaptsToLossyStallsAndDecaysOnCalm(t *testing.T) {
	l := NewLiveness(0)
	l.OnFrame(1000, true)
	l.OnFrame(2000, true)
	if got := l.ThresholdMs(); got != SuspectAfterMs {
		t.Fatalf("baseline threshold = %d, want %d", got, SuspectAfterMs)
	}

	// Simulate a 6s TCP retransmission stall followed by burst-drained heartbeats:
	// threshold must widen above the 15s floor and must not collapse during burst drain.
	l.OnFrame(8000, true)
	widened := l.ThresholdMs()
	if widened <= SuspectAfterMs {
		t.Fatalf("threshold did not widen after 6s stall: got %d", widened)
	}
	// Burst drain of queued heartbeats 5ms apart must not decay stall tolerance.
	for i := int64(1); i <= 4; i++ {
		l.OnFrame(8000+i*5, true)
	}
	if afterBurst := l.ThresholdMs(); afterBurst < widened {
		t.Fatalf("burst drain collapsed widened threshold from %d to %d", widened, afterBurst)
	}
	// A subsequent 16s silence (longer than the 15s floor, shorter than widened threshold)
	// must NOT trigger suspect on this lossy link.
	if l.Suspect(8020 + SuspectAfterMs + 1000) {
		t.Fatalf("falsely suspected at %dms silence when widened threshold=%dms", SuspectAfterMs+1000, l.ThresholdMs())
	}

	// Repeated stalls stay capped at DefaultDeadAfterMs (60s).
	now := int64(8020)
	for range 10 {
		now += 25000
		l.OnFrame(now, true)
	}
	if got := l.ThresholdMs(); got != DefaultDeadAfterMs {
		t.Fatalf("threshold exceeded or missed 60s cap: got %d", got)
	}

	// Calm 1s heartbeats gradually decay the widened threshold back to the 15s floor.
	for range 120 {
		now += 1000
		l.OnFrame(now, true)
	}
	if got := l.ThresholdMs(); got != SuspectAfterMs {
		t.Fatalf("threshold did not decay back to floor after calm period: got %d, want %d", got, SuspectAfterMs)
	}
}
