package daemon

import (
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"strings"
	"testing"
	"time"
)

func TestRunSummaryUsesWholeFinalAnswer(t *testing.T) {
	h := scheduleHarness(t)
	defer h.s.log.Close()
	task := startScheduledTurn(t, h)
	h.s.emit(&pb.Event{TurnId: "timed", Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Phase: "final_answer", Text: "outdated"}}}, &pb.Event{TurnId: "timed", Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Phase: "final_answer", Text: "## 总结\n\n发现新结果。\t\n### 详情\n" + strings.Repeat("字", 220)}}})
	endTimed(t, h, pb.TurnOutcome_TURN_OUTCOME_COMPLETED)
	h.d.tickSchedules(time.Now())
	got := readTask(t, h.d, task.ID).Runs[0].FinalSummary
	if !strings.HasPrefix(got, "总结 发现新结果。 详情 ") || strings.Contains(got, "#") || len([]rune(got)) != 200 || strings.ContainsAny(got, "\\t\\n") {
		t.Fatalf("unhelpful summary: %q", got)
	}
}
