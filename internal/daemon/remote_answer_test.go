package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/relay"
	"google.golang.org/protobuf/proto"
)

func oldClientContext() context.Context {
	return agent.WithOrigin(context.Background(), &pb.Origin{Kind: pb.OriginKind_ORIGIN_KIND_CLIENT, DeviceId: "old-client"})
}

// These positive fixtures model the observations attached by the authenticated
// relay after a client reads the owner's current snapshot. Legacy/no-observation
// inputs remain separate rejection cases below.
func observedClientContext(s *session, kind remoteAnswerKind, id string) context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	seq := uint64(0)
	switch kind {
	case remoteApproval:
		seq = s.answerSafety.approvals[id].EventSeq
	case remoteQuestion:
		seq = s.answerSafety.questions[id].EventSeq
	case remotePlan:
		seq = s.answerSafety.planSeq
	}
	return agent.WithOrigin(context.Background(), &pb.Origin{Kind: pb.OriginKind_ORIGIN_KIND_CLIENT, DeviceId: "synthetic-client", ObservedEventSeq: seq})
}

func restartAnswerHarness(t *testing.T, h *harness) {
	t.Helper()
	m := h.s.meta
	if err := h.s.log.Close(); err != nil {
		t.Fatal(err)
	}
	h.open(m)
	h.d.sessions[m.ID] = h.s
}

func requireTruncatedAnswer(t *testing.T, err error) {
	t.Helper()
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), "内容被截断，请在终端处理") {
		t.Fatalf("expected explicit terminal-only error, got code %s", connect.CodeOf(err))
	}
}

// Invoke the same RPC handler reached by relay, with its authenticated origin,
// without any client-side suffix checks. The real permission-hook output is
// the authority for whether an allow escaped to the CLI.
func TestOldClientCannotAllowTruncatedApproval(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(map[bool]string{false: "live", true: "restarted"}[restart], func(t *testing.T) {
			h := newHarness(t, Config{ApprovalTimeout: time.Minute})
			if restart {
				restartAnswerHarness(t, h)
			}
			h.d.sessions[h.s.meta.ID] = h.s
			h.writeRollout(turnContextLine("user"))
			h.s.bindRollout("codex-1", h.dir+"/rollout.jsonl")
			out := make(chan []byte, 1)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			go func() {
				b, err := h.s.permission(ctx, &agent.Hook{ToolName: "shell", ToolInput: []byte(`{"command":"` + strings.Repeat("x", 1<<20) + `"}`)})
				if err != nil {
					t.Error(err)
				}
				out <- b
			}()
			eventually(t, "pending oversized approval", func() bool { return h.s.info().PendingApprovals == 1 })
			var id string
			for _, e := range h.events() {
				if a := e.GetApprovalRequested(); a != nil {
					id = a.ApprovalId
				}
			}
			rpc := &handler{d: h.d}
			for _, always := range []bool{false, true} {
				_, err := rpc.ResolveApproval(oldClientContext(), connect.NewRequest(&pb.ResolveApprovalRequest{SessionId: h.s.meta.ID, ApprovalId: id, Allow: true, Always: always}))
				requireTruncatedAnswer(t, err)
				select {
				case b := <-out:
					t.Fatalf("CLI received decision before deny: %s", b)
				default:
				}
			}
			_, err := rpc.ResolveApproval(observedClientContext(h.s, remoteApproval, id), connect.NewRequest(&pb.ResolveApprovalRequest{SessionId: h.s.meta.ID, ApprovalId: id}))
			if err != nil {
				t.Fatal(err)
			}
			select {
			case b := <-out:
				if !strings.Contains(string(b), `"behavior":"deny"`) {
					t.Fatalf("CLI output: %s", b)
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
		})
	}
}

func TestRemoteApprovalSafetyFromOriginalHistory(t *testing.T) {
	for _, field := range []string{"description", "command", "tool_input", "skeleton", "rewritten_id", "normal"} {
		for _, restart := range []bool{false, true} {
			t.Run(field+map[bool]string{false: "/live", true: "/replay"}[restart], func(t *testing.T) {
				h := newHarness(t, Config{})
				a := &pb.ApprovalRequested{ApprovalId: "a", Command: "true"}
				e := &pb.Event{Kind: &pb.Event_ApprovalRequested{ApprovalRequested: a}}
				switch field {
				case "description":
					a.Description = strings.Repeat("x", 1<<20)
				case "command":
					a.Command = strings.Repeat("x", 1<<20)
				case "tool_input":
					a.ToolInputJson = strings.Repeat("x", 1<<20)
				case "skeleton":
					e.TurnId = strings.Repeat("x", 1<<20)
				case "rewritten_id":
					a.ApprovalId = strings.Repeat("x", 1<<20)
				}
				if err := h.s.emit(e); err != nil {
					t.Fatal(err)
				}
				if restart {
					restartAnswerHarness(t, h)
				}
				h.d.sessions[h.s.meta.ID] = h.s
				rpc := &handler{d: h.d}
				view := relay.RemoteAnswerSafety(h.events()[0])
				ids := []string{a.ApprovalId}
				if view.ID != a.ApprovalId {
					ids = append(ids, view.ID)
				}
				for _, id := range ids {
					if id != a.ApprovalId {
						if err := h.s.emit(&pb.Event{Kind: &pb.Event_ApprovalRequested{ApprovalRequested: &pb.ApprovalRequested{ApprovalId: id, Command: "true"}}}); err != nil {
							t.Fatal(err)
						}
					}
					// Supplying an alias equal to another live request's real ID must
					// still fail before that request is claimed.
					pending := &approval{id: id, decision: make(chan decision, 1), deadline: time.Now().Add(time.Minute)}
					h.s.approvals[id] = pending
					for _, allow := range []bool{true, false} {
						_, err := rpc.ResolveApproval(observedClientContext(h.s, remoteApproval, id), connect.NewRequest(&pb.ResolveApprovalRequest{SessionId: h.s.meta.ID, ApprovalId: id, Allow: allow}))
						blocked := view.TerminalOnly || allow && view.ApprovalTruncated
						if blocked {
							requireTruncatedAnswer(t, err)
							if pending.claimed || len(pending.decision) != 0 {
								t.Fatal("blocked answer reached hook")
							}
						} else {
							if err != nil {
								t.Fatal(err)
							}
							if d := <-pending.decision; d.allow != allow {
								t.Fatal("wrong hook decision")
							}
							// Give the next independent attempt a live hook.
							fresh := proto.CloneOf(a)
							fresh.ApprovalId = id
							if err := h.s.emit(&pb.Event{Kind: &pb.Event_ApprovalRequested{ApprovalRequested: fresh}}); err != nil {
								t.Fatal(err)
							}
							pending.claimed = false
							h.s.approvals[id] = pending
						}
					}
					delete(h.s.approvals, id)
				}
				// Local RPC bypasses remote content policy (native always-grant
				// restrictions still apply independently).
				pending := &approval{id: a.ApprovalId, decision: make(chan decision, 1), deadline: time.Now().Add(time.Minute)}
				h.s.approvals[a.ApprovalId] = pending
				if _, err := rpc.ResolveApproval(context.Background(), connect.NewRequest(&pb.ResolveApprovalRequest{SessionId: h.s.meta.ID, ApprovalId: a.ApprovalId, Allow: true})); err != nil {
					t.Fatal(err)
				}
				if !(<-pending.decision).allow {
					t.Fatal("local approval blocked")
				}
			})
		}
	}
}

func TestRemoteQuestionSafetySurvivesRestart(t *testing.T) {
	for _, kind := range []string{"normal", "labels", "rewritten_id"} {
		for _, restart := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "/live", true: "/replay"}[restart], func(t *testing.T) {
				h := newHarness(t, Config{})
				q := &pb.QuestionAsked{QuestionId: "q", Questions: []*pb.Question{{Text: "Which language?", Options: []*pb.QuestionOption{{Label: "Go"}}}}}
				if kind == "labels" {
					q.Questions[0].Options[0].Label = strings.Repeat("x", 1<<20)
				}
				if kind == "rewritten_id" {
					q.QuestionId = strings.Repeat("x", 1<<20)
				}
				if err := h.s.emit(&pb.Event{Kind: &pb.Event_QuestionAsked{QuestionAsked: q}}); err != nil {
					t.Fatal(err)
				}
				if restart {
					restartAnswerHarness(t, h)
				}
				h.d.sessions[h.s.meta.ID] = h.s
				rpc := &handler{d: h.d}
				h.term.setScreen("  Question 1/1 (1 unanswered)\n  Which language?\n  \x1b[1;7m› 1. Go\n")
				ids := []string{q.QuestionId}
				view := relay.RemoteAnswerSafety(h.events()[0])
				if view.ID != q.QuestionId {
					ids = append(ids, view.ID)
				}
				for _, id := range ids {
					_, err := rpc.AnswerQuestion(observedClientContext(h.s, remoteQuestion, id), connect.NewRequest(&pb.AnswerQuestionRequest{SessionId: h.s.meta.ID, QuestionId: id, Option: 1}))
					if kind != "normal" {
						requireTruncatedAnswer(t, err)
						if len(h.term.written()) != 0 {
							t.Fatal("skeleton wrote terminal keys")
						}
					} else if err != nil {
						t.Fatal(err)
					}
				}
				if kind != "normal" {
					if _, err := rpc.AnswerQuestion(context.Background(), connect.NewRequest(&pb.AnswerQuestionRequest{SessionId: h.s.meta.ID, QuestionId: q.QuestionId, Option: 1})); err != nil {
						t.Fatal(err)
					}
				}
				if w := h.term.written(); len(w) != 1 || w[0] != "1" {
					t.Fatalf("writes %q", w)
				}
			})
		}
	}
}

func TestRemoteSkeletonPlanCannotWriteKeys(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(map[bool]string{false: "live", true: "replay"}[restart], func(t *testing.T) {
			h, _ := controlHarness(t)
			if err := h.s.emit(&pb.Event{TurnId: strings.Repeat("x", 1<<20), Kind: &pb.Event_PlanProposed{PlanProposed: &pb.PlanProposed{Text: "plan"}}}); err != nil {
				t.Fatal(err)
			}
			// A plan prompt remains actionable after the model turn ends.
			if err := h.s.emit(&pb.Event{Kind: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{}}}); err != nil {
				t.Fatal(err)
			}
			if restart {
				restartAnswerHarness(t, h)
			}
			h.d.sessions[h.s.meta.ID] = h.s
			h.term.setScreen("  Implement this plan?\n\x1b[1;7m› 1. Yes, implement this plan  Switch\n\x1b[0m  2. Yes, clear context  Fresh\n  3. No, stay in Plan mode  Continue\n")
			rpc := &handler{d: h.d}
			for _, choice := range []pb.PlanChoice{pb.PlanChoice_PLAN_CHOICE_IMPLEMENT, pb.PlanChoice_PLAN_CHOICE_KEEP_PLANNING} {
				_, err := rpc.AnswerPlan(oldClientContext(), connect.NewRequest(&pb.AnswerPlanRequest{SessionId: h.s.meta.ID, Choice: choice}))
				requireTruncatedAnswer(t, err)
			}
			if len(h.term.written()) != 0 {
				t.Fatal("skeleton plan wrote terminal keys")
			}
			if _, err := rpc.AnswerPlan(context.Background(), connect.NewRequest(&pb.AnswerPlanRequest{SessionId: h.s.meta.ID, Choice: pb.PlanChoice_PLAN_CHOICE_IMPLEMENT})); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Closing the log makes any read fail deterministically, regardless of the
// machine's speed or file cache. Safety checks (including both checks in the
// question RPC) must still work from the pending projection. This is a unit
// proof that history length does not add request-time log work, not a benchmark.
func TestRemoteAnswerChecksDoNotReadHistory(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(map[bool]string{false: "live", true: "replay"}[restart], func(t *testing.T) {
			h := newHarness(t, Config{})
			history := make([]*pb.Event, 4096)
			for i := range history {
				history[i] = &pb.Event{Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Text: "unrelated history"}}}
			}
			history = append(history,
				&pb.Event{Kind: &pb.Event_ApprovalRequested{ApprovalRequested: &pb.ApprovalRequested{ApprovalId: "a", Command: strings.Repeat("x", 1<<20)}}},
				&pb.Event{Kind: &pb.Event_QuestionAsked{QuestionAsked: &pb.QuestionAsked{QuestionId: "q", Questions: []*pb.Question{{Text: "Which language?", Options: []*pb.QuestionOption{{Label: "Go"}}}}}}},
				&pb.Event{TurnId: strings.Repeat("t", 1<<20), Kind: &pb.Event_PlanProposed{PlanProposed: &pb.PlanProposed{Text: "plan"}}},
			)
			if err := h.s.emit(history...); err != nil {
				t.Fatal(err)
			}
			if restart {
				restartAnswerHarness(t, h)
			}
			h.d.sessions[h.s.meta.ID] = h.s
			if err := h.s.log.Close(); err != nil {
				t.Fatal(err)
			}

			for _, tc := range []struct {
				kind           remoteAnswerKind
				id             string
				allow, blocked bool
			}{
				{remoteApproval, "a", true, true}, {remoteApproval, "a", false, false},
				{remoteQuestion, "q", false, false}, {remotePlan, "", false, true},
			} {
				h.s.mu.Lock()
				seq := h.s.answerSafety.approvals[tc.id].EventSeq
				if tc.kind == remoteQuestion {
					seq = h.s.answerSafety.questions[tc.id].EventSeq
				}
				origin := &pb.Origin{Kind: pb.OriginKind_ORIGIN_KIND_CLIENT, DeviceId: "synthetic-client", ObservedEventSeq: seq}
				err := h.s.checkRemoteAnswer(tc.kind, tc.id, tc.allow, origin)
				h.s.mu.Unlock()
				if tc.blocked {
					if err != errRemoteAnswerTruncated {
						t.Fatalf("safety check depended on history: %v", err)
					}
				} else if err != nil {
					t.Fatalf("safe answer read closed history: %v", err)
				}
			}
			h.term.setScreen("  Question 1/1 (1 unanswered)\n  Which language?\n  \x1b[1;7m› 1. Go\n")
			if _, err := (&handler{d: h.d}).AnswerQuestion(observedClientContext(h.s, remoteQuestion, "q"), connect.NewRequest(&pb.AnswerQuestionRequest{SessionId: h.s.meta.ID, QuestionId: "q", Option: 1})); err != nil {
				t.Fatalf("question RPC read closed history: %v", err)
			}
			if w := h.term.written(); len(w) != 1 || w[0] != "1" {
				t.Fatalf("normal answer did not reach CLI: %q", w)
			}
		})
	}
}

func TestAnswerSafetyFollowsCommittedPendingState(t *testing.T) {
	h := newHarness(t, Config{})
	approval := &pb.Event{Kind: &pb.Event_ApprovalRequested{ApprovalRequested: &pb.ApprovalRequested{ApprovalId: "a", Command: strings.Repeat("x", 1<<20)}}}
	question := &pb.Event{Kind: &pb.Event_QuestionAsked{QuestionAsked: &pb.QuestionAsked{QuestionId: "q", Questions: []*pb.Question{{Options: []*pb.QuestionOption{{Label: strings.Repeat("x", 1<<20)}}}}}}}
	plan := &pb.Event{TurnId: strings.Repeat("t", 1<<20), Kind: &pb.Event_PlanProposed{PlanProposed: &pb.PlanProposed{Text: "plan"}}}
	if err := h.s.emit(approval, question, plan); err != nil {
		t.Fatal(err)
	}
	if err := h.s.emit(&pb.Event{Kind: &pb.Event_TurnEnded{TurnEnded: &pb.TurnEnded{}}}); err != nil {
		t.Fatal(err)
	}
	p := &h.s.answerSafety
	if len(p.questions) != 0 || !p.approvals["a"].ApprovalTruncated || !p.plan.TerminalOnly {
		t.Fatal("turn end did not preserve approval/plan or release questions")
	}
	if err := h.s.emit(question,
		&pb.Event{Kind: &pb.Event_ApprovalResolved{ApprovalResolved: &pb.ApprovalResolved{ApprovalId: "a"}}},
		&pb.Event{Kind: &pb.Event_QuestionAnswered{QuestionAnswered: &pb.QuestionAnswered{QuestionId: "q"}}},
		&pb.Event{Kind: &pb.Event_PlanAnswered{PlanAnswered: &pb.PlanAnswered{}}},
	); err != nil {
		t.Fatal(err)
	}
	if len(p.approvals) != 0 || len(p.questions) != 0 || p.plan.TerminalOnly {
		t.Fatal("settled answers retained safety state")
	}
	// The same request ID can be used again; only the currently pending
	// original event should determine its restrictions.
	if err := h.s.emit(&pb.Event{Kind: &pb.Event_ApprovalRequested{ApprovalRequested: &pb.ApprovalRequested{ApprovalId: "a", Command: "true"}}}); err != nil {
		t.Fatal(err)
	}
	if p.approvals["a"].ApprovalTruncated {
		t.Fatal("new complete request inherited an old restriction")
	}
	if err := h.s.log.Close(); err != nil {
		t.Fatal(err)
	}
	if err := h.s.emit(approval, question, plan); err == nil {
		t.Fatal("closed log accepted events")
	}
	if p.approvals["a"].ApprovalTruncated || len(p.questions) != 0 || p.plan.TerminalOnly {
		t.Fatal("uncommitted events changed answer safety")
	}
}

func TestCurrentObservationCannotAnswerAReusedApprovalID(t *testing.T) {
	h := newHarness(t, Config{})
	event := func() *pb.Event {
		return &pb.Event{Kind: &pb.Event_ApprovalRequested{ApprovalRequested: &pb.ApprovalRequested{ApprovalId: "same-id", Command: "true"}}}
	}
	if err := h.s.emit(event()); err != nil {
		t.Fatal(err)
	}
	original := agent.OriginFrom(observedClientContext(h.s, remoteApproval, "same-id"))
	h.s.mu.Lock()
	if err := h.s.checkRemoteAnswer(remoteApproval, "same-id", true, &pb.Origin{Kind: pb.OriginKind_ORIGIN_KIND_CLIENT}); err != errRemoteAnswerStale {
		t.Fatalf("missing observation: %v", err)
	}
	if err := h.s.checkRemoteAnswer(remoteApproval, "same-id", true, original); err != nil {
		t.Fatal(err)
	}
	h.s.mu.Unlock()
	if err := h.s.emit(&pb.Event{Kind: &pb.Event_ApprovalResolved{ApprovalResolved: &pb.ApprovalResolved{ApprovalId: "same-id"}}}, event()); err != nil {
		t.Fatal(err)
	}
	current := agent.OriginFrom(observedClientContext(h.s, remoteApproval, "same-id"))
	h.s.mu.Lock()
	defer h.s.mu.Unlock()
	if err := h.s.checkRemoteAnswer(remoteApproval, "same-id", true, original); err != errRemoteAnswerStale {
		t.Fatalf("old prompt answered reused ID: %v", err)
	}
	if err := h.s.checkRemoteAnswer(remoteApproval, "same-id", true, current); err != nil {
		t.Fatal(err)
	}
}
func TestPendingPromptBodiesDoNotLeakAcrossReplacementOrAsyncQuestions(t *testing.T) {
	var pending pendingAnswerSafety
	for seq := uint64(1); seq <= 256; seq++ {
		pending.observe(&pb.Event{Seq: seq, Kind: &pb.Event_QuestionAsked{QuestionAsked: &pb.QuestionAsked{QuestionId: "async-id", Async: true}}})
	}
	for seq := uint64(257); seq <= 512; seq++ {
		pending.observe(&pb.Event{Seq: seq, Kind: &pb.Event_ApprovalRequested{ApprovalRequested: &pb.ApprovalRequested{ApprovalId: "same-id", Command: "true"}}})
	}
	if len(pending.events) != 1 || pending.events[512] == nil || pending.approvals["same-id"].EventSeq != 512 {
		t.Fatal("unactionable/replaced prompts exhausted live snapshot capacity")
	}
}

func TestOwnerSnapshotAttestsCurrentQueueAndBoundsPrivatePreview(t *testing.T) {
	h := newHarness(t, Config{})
	h.d.sessions[h.s.meta.ID] = h.s
	h.s.mu.Lock()
	h.s.running = true
	h.s.queue.Queue = []input{{RequestID: "current-input", Text: strings.Repeat("界", 1000), Mode: pb.SendMode_SEND_MODE_QUEUE, Paused: true, Stage: pb.InputStage_INPUT_STAGE_WAITING, Origin: &storedOrigin{Kind: pb.OriginKind_ORIGIN_KIND_CLIENT, DeviceID: "synthetic-client"}}}
	h.s.mu.Unlock()
	snapshot, err := h.d.LiveSnapshot(h.s.meta.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Running || len(snapshot.Actions) != 1 {
		t.Fatal("current owner queue missing")
	}
	event := snapshot.Actions[0]
	state := event.GetInputState()
	if event.SessionId != h.s.meta.ID || state == nil || state.RequestId != "current-input" || state.Status != pb.InputStatus_INPUT_STATUS_QUEUED || !state.Paused || state.Mode != pb.SendMode_SEND_MODE_QUEUE || event.Origin.GetDeviceId() != "synthetic-client" {
		t.Fatal("queue attestation lost scope or state")
	}
	if len(state.Text) > 600 || !strings.HasSuffix(state.Text, "[内容已截断]") {
		t.Fatal("private preview was not bounded and marked")
	}
}
