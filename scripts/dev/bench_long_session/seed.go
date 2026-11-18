//go:build ignore

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/eventlog"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type sessionMeta struct {
	ID        string    `json:"id"`
	Agent     pb.Agent  `json:"agent,omitempty"`
	Cwd       string    `json:"cwd"`
	Tmux      string    `json:"tmux"`
	CreatedAt time.Time `json:"created_at"`
	Exited    bool      `json:"exited,omitempty"`
	Title     string    `json:"title,omitempty"`
}

func longCodeBlock(turn int) string {
	return fmt.Sprintf("```go\n"+
		"// Turn %d: incremental timeline window builder\n"+
		"package screens\n\n"+
		"import (\n"+
		"\t\"fmt\"\n"+
		"\t\"slices\"\n"+
		")\n\n"+
		"func buildWindowSlice(events []Event, start, end int) []TimelineItem {\n"+
		"\tif start < 0 {\n"+
		"\t\tstart = 0\n"+
		"\t}\n"+
		"\tif end > len(events) {\n"+
		"\t\tend = len(events)\n"+
		"\t}\n"+
		"\tout := make([]TimelineItem, 0, end-start)\n"+
		"\tfor i := start; i < end; i++ {\n"+
		"\t\tev := events[i]\n"+
		"\t\tif ev.Seq == 0 {\n"+
		"\t\t\tcontinue\n"+
		"\t\t}\n"+
		"\t\tout = append(out, TimelineItem{\n"+
		"\t\t\tID:    fmt.Sprintf(\"ev-%%d\", ev.Seq),\n"+
		"\t\t\tSeq:   ev.Seq,\n"+
		"\t\t\tLabel: ev.Summary,\n"+
		"\t\t})\n"+
		"\t}\n"+
		"\treturn slices.Clip(out)\n"+
		"}\n"+
		"```", turn)
}

func diffBlock(turn int) string {
	return fmt.Sprintf("```diff\n"+
		"--- a/clientcore/screens/session_timeline.go\n"+
		"+++ b/clientcore/screens/session_timeline.go\n"+
		"@@ -17,12 +17,16 @@ func buildSessionTimeline(in SessionViewInput) *SessionTimelineView {\n"+
		"-   for _, e := range events {\n"+
		"-       rawItems = append(rawItems, convert(e))\n"+
		"-   }\n"+
		"+   // Turn %d optimization: only materialize requested window\n"+
		"+   start, end := resolveWindowRange(len(events), win)\n"+
		"+   for _, e := range events[start:end] {\n"+
		"+       rawItems = append(rawItems, convert(e))\n"+
		"+   }\n"+
		"    return out\n"+
		"}\n"+
		"```", turn)
}

func main() {
	home := flag.String("home", "", "daemon REMOTE_AGENT_HOME directory")
	sid := flag.String("sid", "long-bench-0001", "session ID")
	turns := flag.Int("turns", 120, "number of conversation turns")
	cwd := flag.String("cwd", "/tmp/bench-project", "session cwd")
	flag.Parse()

	if *home == "" {
		fmt.Fprintln(os.Stderr, "--home is required")
		os.Exit(2)
	}

	sessDir := filepath.Join(*home, "sessions", *sid)
	if err := os.MkdirAll(sessDir, 0o700); err != nil {
		panic(err)
	}

	base := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	meta := sessionMeta{
		ID:        *sid,
		Agent:     pb.Agent_AGENT_CODEX,
		Cwd:       *cwd,
		Tmux:      "bench-" + *sid,
		CreatedAt: base,
		Exited:    true,
		Title:     fmt.Sprintf("Long Session Benchmark (%d turns)", *turns),
	}
	mb, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(sessDir, "session.json"), append(mb, '\n'), 0o600); err != nil {
		panic(err)
	}

	logPath := filepath.Join(sessDir, "events.jsonl")
	_ = os.Remove(logPath)
	log, err := eventlog.Open(logPath)
	if err != nil {
		panic(err)
	}
	defer log.Close()

	var batch []*pb.Event
	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := log.Append(*sid, batch, "", 0); err != nil {
			panic(err)
		}
		batch = batch[:0]
	}

	ts := base
	nextTime := func() *timestamppb.Timestamp {
		ts = ts.Add(1500 * time.Millisecond)
		return timestamppb.New(ts)
	}
	exitOK := int32(0)
	exitFail := int32(1)

	for t := 1; t <= *turns; t++ {
		turnID := fmt.Sprintf("turn-%04d", t)
		batch = append(batch, &pb.Event{
			SessionId: *sid,
			Time:      nextTime(),
			TurnId:    turnID,
			Kind: &pb.Event_UserMessage{
				UserMessage: &pb.UserMessage{
					Text: fmt.Sprintf("Turn %d: Inspect `clientcore/screens/session_timeline.go`, run tests, and update the window slicing logic.", t),
				},
			},
		})
		batch = append(batch, &pb.Event{
			SessionId: *sid,
			Time:      nextTime(),
			TurnId:    turnID,
			Kind:      &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}},
		})
		batch = append(batch, &pb.Event{
			SessionId: *sid,
			Time:      nextTime(),
			TurnId:    turnID,
			Kind: &pb.Event_Reasoning{
				Reasoning: &pb.Reasoning{
					Text: fmt.Sprintf("Analyzing turn %d call paths: checking how `EventBuffer` stores contiguous events and how `buildSessionTimeline` slices windows.", t),
				},
			},
		})

		for c := 1; c <= 14; c++ {
			callID := fmt.Sprintf("call-%04d-%02d", t, c)
			cmdStr := fmt.Sprintf("rg -n 'buildSessionTimeline|applyTimelineWindow' clientcore/ # step %d.%d", t, c)
			kind := pb.ToolKind_TOOL_KIND_COMMAND
			title := cmdStr
			input := fmt.Sprintf(`{"command":%q,"workdir":%q}`, cmdStr, *cwd)
			ec := &exitOK
			status := "completed"
			if c%7 == 0 {
				kind = pb.ToolKind_TOOL_KIND_FILE_CHANGE
				title = fmt.Sprintf("Edit clientcore/screens/session_timeline.go (pass %d)", t)
				input = diffBlock(t)
			} else if c == 13 && t%5 == 0 {
				status = "failed"
				ec = &exitFail
			}
			batch = append(batch, &pb.Event{
				SessionId: *sid,
				Time:      nextTime(),
				TurnId:    turnID,
				Kind: &pb.Event_ToolStarted{
					ToolStarted: &pb.ToolStarted{
						Title:  title,
						CallId: callID,
						Kind:   kind,
						Input:  input,
					},
				},
			})
			batch = append(batch, &pb.Event{
				SessionId: *sid,
				Time:      nextTime(),
				TurnId:    turnID,
				Kind: &pb.Event_ToolCall{
					ToolCall: &pb.ToolCall{
						CallId:     callID,
						Kind:       kind,
						Title:      title,
						Input:      input,
						Output:     fmt.Sprintf("ok (turn %d call %d)", t, c),
						Status:     status,
						ExitCode:   ec,
						DurationMs: 120 + int64(c*15),
					},
				},
			})
		}

		batch = append(batch, &pb.Event{
			SessionId: *sid,
			Time:      nextTime(),
			TurnId:    turnID,
			Kind: &pb.Event_AgentMessage{
				AgentMessage: &pb.AgentMessage{
					Phase: "commentary",
					Text:  fmt.Sprintf("Checking benchmark metrics for turn **%d** (`session_timeline.go` and `Session.tsx`).", t),
				},
			},
		})

		md := fmt.Sprintf("## Turn %d Summary\n\n"+
			"Updated timeline windowing and verified rendering across `Session.tsx` and `HistoryRows.tsx`:\n\n"+
			"- Reduced per-call allocation in `buildSessionTimeline`\n"+
			"- Preserved `Virtuoso` scroll anchoring at `firstItemIndex`\n"+
			"- Checked [architecture doc](https://example.com/docs/arch#%d)\n\n"+
			"| Metric | Before | After | Delta |\n"+
			"|---|---|---|---|\n"+
			"| View WASM | 28.4 ms | 1.2 ms | -95.8%% |\n"+
			"| Payload | 1681 KB | 48 KB | -97.1%% |\n"+
			"| Long Tasks | 19 | 0 | -100%% |\n\n"+
			"%s\n\n"+
			"%s\n",
			t, t, longCodeBlock(t), diffBlock(t))

		batch = append(batch, &pb.Event{
			SessionId: *sid,
			Time:      nextTime(),
			TurnId:    turnID,
			Kind: &pb.Event_AgentMessage{
				AgentMessage: &pb.AgentMessage{
					Phase: "final_answer",
					Text:  md,
				},
			},
		})
		batch = append(batch, &pb.Event{
			SessionId: *sid,
			Time:      nextTime(),
			TurnId:    turnID,
			Kind: &pb.Event_TokenUsage{
				TokenUsage: &pb.TokenUsage{
					InputTokens:     int64(10000 + t*500),
					OutputTokens:    int64(800 + t*20),
					TotalTokens:     int64(10800 + t*520),
					LastTotalTokens: int64(25000 + t*200),
					ContextWindow:   200000,
				},
			},
		})
		batch = append(batch, &pb.Event{
			SessionId: *sid,
			Time:      nextTime(),
			TurnId:    turnID,
			Kind: &pb.Event_TurnEnded{
				TurnEnded: &pb.TurnEnded{
					Outcome:    pb.TurnOutcome_TURN_OUTCOME_COMPLETED,
					DurationMs: 4200 + int64(t*10),
				},
			},
		})
		if len(batch) >= 200 {
			flush()
		}
	}

	batch = append(batch, &pb.Event{
		SessionId: *sid,
		Time:      nextTime(),
		Kind: &pb.Event_SessionState{
			SessionState: &pb.SessionState{
				Status: pb.SessionStatus_SESSION_STATUS_EXITED,
				Detail: "completed",
			},
		},
	})
	flush()
	fmt.Printf("Seeded session %s in %s (%d turns)\n", *sid, sessDir, *turns)
}
