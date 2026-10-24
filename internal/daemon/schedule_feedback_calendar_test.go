package daemon

import (
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/schedule"
	"testing"
	"time"
)

func calendarTime(t *testing.T, stamp string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		t.Fatal(err)
	}
	return at
}

func TestCalendarCatchupOccupiesOccurrenceDateOnly(t *testing.T) {
	for _, late := range []int64{86400, 60} {
		for _, restart := range []bool{false, true} {
			t.Run(time.Duration(late).String()+map[bool]string{true: "restart", false: "live"}[restart], func(t *testing.T) {
				h := scheduleHarness(t)
				defer h.s.log.Close()
				spec := taskSpec(schedule.Trigger{Type: "calendar", Time: "23:59", Timezone: "Asia/Shanghai"})
				spec.LateSeconds = late
				task := createTask(t, h, spec, "")
				due := calendarTime(t, "2026-10-20T23:59:00+08:00")
				now := calendarTime(t, "2026-10-21T00:03:00+08:00")
				h.d.tasks[task.ID].NextRunAt = &due
				if err := h.d.saveSchedulesLocked(); err != nil {
					t.Fatal(err)
				}
				if restart {
					if err := h.d.loadSchedules(); err != nil {
						t.Fatal(err)
					}
				}
				h.d.tickSchedules(now)
				got := readTask(t, h.d, task.ID)
				if late == 86400 {
					if got.Active == nil || got.LastCalendarDate != "2026-10-20" {
						t.Fatalf("wrong reservation: %+v", got)
					}
					completeTask(h, got.Active, now)
					h.d.tickSchedules(now)
				} else if got.LastCalendarDate != "" {
					t.Fatal("skipped run consumed a date")
				}
				got = readTask(t, h.d, task.ID)
				want := calendarTime(t, "2026-10-21T23:59:00+08:00")
				if got.NextRunAt == nil || !got.NextRunAt.Equal(want) {
					t.Fatalf("today lost: %+v", got)
				}
			})
		}
	}
}

// Referenced by subsequent calendar feedback regressions.
var _ = pb.ScheduleRequest{}
