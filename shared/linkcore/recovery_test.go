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

	// 1s heartbeat cadence -> max(3s, 3*1s) = 3s
	l.OnFrame(2000, true)
	if got := l.ThresholdMs(); got != 3000 {
		t.Fatalf("1s cadence threshold = %v, want 3s", got)
	}
	if l.Suspect(4000) {
		t.Fatal("suspected too early at 2s silence")
	}
	if !l.Suspect(5000) {
		t.Fatal("did not suspect at 3s silence")
	}

	// 20s legacy server cadence -> max(3s, 3*20s) = 60s
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

	// Sub-second bursts still respect 3s minimum floor.
	burst := NewLiveness(0)
	burst.OnFrame(100, true)
	burst.OnFrame(200, true)
	if got := burst.ThresholdMs(); got != 3000 {
		t.Fatalf("burst floor threshold = %v, want 3s", got)
	}
}
