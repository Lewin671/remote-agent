package antigravity

import (
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"strings"
	"testing"
)

func TestHookContract(t *testing.T) {
	a := New("agy", t.TempDir())
	for _, event := range []string{"SessionStart", "Stop", "PreToolUse"} {
		h, err := a.DecodeHook([]byte(`{"hook_event_name":"` + event + `","conversationId":"conversation","transcriptPath":"/workspace/brain/id/logs/transcript_full.jsonl","modelName":"gemini-3.8-flash-low","stepIdx":6,"toolCall":{"name":"run_command","args":{"CommandLine":"echo hi"}},"terminationReason":"USER_CANCELED"}`))
		if err != nil {
			t.Fatal(err)
		}
		if h.HookEventName != event || h.SessionID != "conversation" || strings.HasSuffix(h.TranscriptPath, "_full.jsonl") {
			t.Fatalf("hook %v", h)
		}
		if event == "PreToolUse" && h.ToolUseID != "conversation/step_6" {
			t.Fatal("call ID not aligned with result step")
		}
		if event == "Stop" {
			if e := a.HookEvents(h); len(e) != 1 || e[0].GetTurnEnded().Outcome != pb.TurnOutcome_TURN_OUTCOME_ABORTED {
				t.Fatal("cancel stop")
			}
		}
	}
	if _, err := a.DecodeHook([]byte(`{"conversationId":"x"}`)); err == nil {
		t.Fatal("guessed lifecycle event")
	}
	if a.ForwardPermission(nil, nil) {
		t.Fatal("PreToolUse must never block on a decision")
	}
}
