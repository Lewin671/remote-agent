package transport

import (
	"testing"
	"time"
)

func TestRecoveryBudgetsSurviveHintsAndReady(t *testing.T) {
	now := time.Unix(1, 0)
	global := &Budget{}
	a, b, c := Recovery{Budget: global}, Recovery{Budget: global}, Recovery{Budget: global}
	if !a.Take(now) || !a.Take(now) || a.Take(now) {
		t.Fatal("target burst")
	}
	a.Ready()
	if a.Take(now) {
		t.Fatal("Ready refilled target")
	}
	if !b.Take(now) || !b.Take(now) || c.Take(now) {
		t.Fatal("process burst")
	}
	if c.Delay(now) != 500*time.Millisecond {
		t.Fatal("process refill rate")
	}
	if !c.Take(now.Add(500*time.Millisecond)) || a.Take(now.Add(500*time.Millisecond)) {
		t.Fatal("atomic budgets")
	}
	if !a.Take(now.Add(time.Second)) {
		t.Fatal("target refill")
	}
}
func TestRecoveryFullJitterDoesNotMoveForNotifications(t *testing.T) {
	now := time.Unix(1, 0)
	r := Recovery{Budget: &Budget{}, Random: func() float64 { return .5 }}
	for _, cap := range []time.Duration{1, 2, 4, 8, 16, 30, 30} {
		r.Failed(now)
		want := cap * time.Second / 2
		for range 1000 {
			if got := r.Delay(now); got != want {
				t.Fatalf("delay %v want %v", got, want)
			}
		}
		now = now.Add(want)
	}
	r.Ready()
	r.Failed(now)
	if r.Delay(now) != 500*time.Millisecond {
		t.Fatal("authenticated recovery did not reset failure sequence")
	}
}

func TestAdaptiveLivenessThresholds(t *testing.T) {
	l := NewAdaptiveLiveness(0)
	if got := l.Threshold(); got != 60*time.Second {
		t.Fatalf("initial threshold = %v, want 60s", got)
	}
	if l.Suspect(int64(59 * time.Second)) {
		t.Fatal("suspected before 60s with 0 heartbeats")
	}
	l.OnFrame(int64(1*time.Second), true)
	if got := l.Threshold(); got != 60*time.Second {
		t.Fatalf("after 1 heartbeat threshold = %v, want 60s", got)
	}
	if l.Suspect(int64(59 * time.Second)) {
		t.Fatal("suspected before 60s with 1 heartbeat")
	}
	if !l.Suspect(int64(61 * time.Second)) {
		t.Fatal("did not suspect at 60s silence with <2 heartbeats")
	}

	// 1s heartbeat cadence -> max(3s, 3*1s) = 3s
	l.OnFrame(int64(2*time.Second), true)
	if got := l.Threshold(); got != 3*time.Second {
		t.Fatalf("1s cadence threshold = %v, want 3s", got)
	}
	if l.Suspect(int64(4 * time.Second)) {
		t.Fatal("suspected too early at 2s silence")
	}
	if !l.Suspect(int64(5 * time.Second)) {
		t.Fatal("did not suspect at 3s silence")
	}

	// 20s legacy server cadence -> max(3s, 3*20s) = 60s
	legacy := NewAdaptiveLiveness(0)
	legacy.OnFrame(int64(20*time.Second), true)
	legacy.OnFrame(int64(40*time.Second), true)
	if got := legacy.Threshold(); got != 60*time.Second {
		t.Fatalf("20s cadence threshold = %v, want 60s", got)
	}
	if legacy.Suspect(int64(99 * time.Second)) {
		t.Fatal("20s legacy server falsely suspected before 60s")
	}
	if !legacy.Suspect(int64(100 * time.Second)) {
		t.Fatal("20s legacy server not suspected at 60s")
	}

	// Sub-second bursts still respect 3s minimum floor.
	burst := NewAdaptiveLiveness(0)
	burst.OnFrame(int64(100*time.Millisecond), true)
	burst.OnFrame(int64(200*time.Millisecond), true)
	if got := burst.Threshold(); got != 3*time.Second {
		t.Fatalf("burst floor threshold = %v, want 3s", got)
	}
}
