package daemon

import (
	"testing"
	"time"
)

func TestRelaySessionLimitFailsScheduledExecutionDurably(t *testing.T) {
	h := scheduleHarness(t)
	defer h.s.log.Close()
	task := startScheduledTurn(t, h)
	if err := h.d.SessionPublicationFailed(h.s.meta.ID, "relay_session_limit"); err != nil {
		t.Fatal(err)
	}
	if err := h.d.loadSchedules(); err != nil {
		t.Fatal(err)
	}
	got := readTask(t, h.d, task.ID)
	if got.Status != "failed" || got.Reason != "relay_session_limit" || got.Active != nil || got.NextRunAt != nil || len(got.Runs) != 1 || got.Runs[0].Status != "failed" || got.Runs[0].Reason != "relay_session_limit" {
		t.Fatalf("quota not surfaced: %+v", got)
	}
	h.d.tickSchedules(time.Now().Add(time.Hour))
	if readTask(t, h.d, task.ID).Dispatches != 1 {
		t.Fatal("quota failure kept dispatching")
	}
}
