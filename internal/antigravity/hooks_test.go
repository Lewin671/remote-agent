package antigravity

import (
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"os"
	"path/filepath"
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

func TestStatusLineTranscriptAliasResolvesToActualCLIState(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "brain", "conversation", ".system_generated", "logs", "transcript.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, path, []byte("{}\n"))
	a := New("agy", home)
	reported := filepath.Join(t.TempDir(), "antigravity", "brain", "conversation", ".system_generated", "logs", "transcript.jsonl")
	r, err := a.ParseStatusLine([]byte(`{"product":"antigravity","agent_state":"idle","conversation_id":"conversation","transcript_path":"`+reported+`"}`), "")
	if err != nil || r.TranscriptPath != path {
		t.Fatalf("bad native path alias: %v %v", r, err)
	}
	h, err := a.DecodeHook([]byte(`{"hook_event_name":"SessionStart","conversationId":"conversation","transcriptPath":"` + filepath.Join(filepath.Dir(path), "transcript_full.jsonl") + `"}`))
	if err != nil || h.TranscriptPath != path {
		t.Fatalf("hook and status line disagree: %v %v", h, err)
	}
}
