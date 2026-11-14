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
