package daemon

import (
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/paths"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScheduleRevisionIgnoresProgressCursors(t *testing.T) {
	h := scheduleHarness(t)
	defer h.s.log.Close()
	task := startScheduledTurn(t, h)
	h.d.tickSchedules(time.Now())
	before := readTask(t, h.d, task.ID)
	revision := h.d.scheduleRevision
	for i := 0; i < 5; i++ {
		h.s.emit(&pb.Event{TurnId: "timed", Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Phase: "commentary", Text: "working"}}})
		h.d.tickSchedules(time.Now())
	}
	after := readTask(t, h.d, task.ID)
	if after.Active.AfterSeq <= before.Active.AfterSeq {
		t.Fatal("cursor not persisted")
	}
	if after.Revision != before.Revision || h.d.scheduleRevision != revision {
		t.Fatal("progress republished machine metadata")
	}
	if err := h.d.loadSchedules(); err != nil {
		t.Fatal(err)
	}
	if h.d.scheduleRevision != revision {
		t.Fatal("unchanged reload advanced revision")
	}
	if err := notifyTest(t, h, "useful result"); err != nil {
		t.Fatal(err)
	}
	h.d.tickSchedules(time.Now())
	if h.d.scheduleRevision <= revision {
		t.Fatal("notice did not advance visible revision")
	}
}

func TestScheduleVisibleRevisionRejectsInvalidRecords(t *testing.T) {
	h := scheduleHarness(t)
	defer h.s.log.Close()
	if err := os.WriteFile(filepath.Join(paths.Dir(), "schedules.json"), []byte(`{"version":1,"tasks":{"broken":null}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := h.d.loadSchedules(); err == nil {
		t.Fatal("invalid task was projected instead of rejected")
	}
}
