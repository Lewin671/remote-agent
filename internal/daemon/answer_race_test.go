package daemon

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"google.golang.org/protobuf/proto"
)

const answerRacePlanScreen = "  Implement this plan?\n\x1b[1;7m› 1. Yes, implement this plan  Switch\n\x1b[0m  2. Yes, clear context  Fresh\n  3. No, stay in Plan mode  Continue\n"
const answerRaceQuestionScreen = "  Question 1/1 (1 unanswered)\n  Which language?\n  \x1b[1;7m› 1. Go\n"

func answerRaceOrigin(s *session, kind remoteAnswerKind, id, request string) *pb.Origin {
	o := proto.CloneOf(agent.OriginFrom(observedClientContext(s, kind, id)))
	o.RequestId = request
	if kind == remotePlan {
		o.ObservedScreenHash = planScreenHash(answerRacePlanScreen)
	}
	return o
}

func TestConcurrentPlanAnswersConsumePromptBeforeReleasingControl(t *testing.T) {
	for _, recordFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "record-success", true: "record-failure"}[recordFails], func(t *testing.T) {
			h := newHarness(t, Config{})
			h.term.setScreen(answerRacePlanScreen) // CLI deliberately has not consumed the keys yet.
			plan := func() *pb.Event {
				return &pb.Event{Kind: &pb.Event_PlanProposed{PlanProposed: &pb.PlanProposed{Text: "synthetic plan"}}}
			}
			if err := h.s.emit(plan()); err != nil {
				t.Fatal(err)
			}
			first := answerRaceOrigin(h.s, remotePlan, "", "first-plan-request")
			second := answerRaceOrigin(h.s, remotePlan, "", "second-plan-request")
			recording, release := make(chan struct{}), make(chan struct{})
			a, b := make(chan error, 1), make(chan error, 1)
			go func() {
				a <- h.s.answerPlanUsing(context.Background(), pb.PlanChoice_PLAN_CHOICE_IMPLEMENT, first, func(e *pb.Event) error {
					close(recording)
					<-release
					if recordFails {
						return errors.New("synthetic recording failure")
					}
					return h.s.emit(e)
				})
			}()
			<-recording
			if h.s.ctlMu.TryLock() {
				h.s.ctlMu.Unlock()
				t.Error("plan recording ran after releasing the terminal lock")
			}
			go func() { b <- h.s.answerPlanBy(context.Background(), pb.PlanChoice_PLAN_CHOICE_IMPLEMENT, second) }()
			select {
			case err := <-b:
				if !errors.Is(err, errRemoteAnswerStale) {
					t.Errorf("overlapping answer=%v, want stale", err)
				}
				b <- err
			case <-time.After(100 * time.Millisecond):
			}
			close(release)
			err := <-a
			if recordFails {
				if err == nil || !strings.Contains(err.Error(), "already applied") {
					t.Errorf("first recording failure=%v", err)
				}
			} else if err != nil {
				t.Error(err)
			}
			if err := <-b; !errors.Is(err, errRemoteAnswerStale) {
				t.Errorf("second answer=%v, want stale", err)
			}
			if w := h.term.written(); len(w) != 1 {
				t.Errorf("duplicate plan keys: %q", w)
			}
			// A status poll can still see the old screen. A freshly observed
			// sequence is not permission to repeat our pending answer.
			if err := h.s.emit(&pb.Event{Kind: &pb.Event_SessionState{SessionState: &pb.SessionState{PlanOpen: true}}}); err != nil {
				t.Fatal(err)
			}
			fresh := answerRaceOrigin(h.s, remotePlan, "", "fresh-observation-same-prompt")
			if err := h.s.answerPlanBy(context.Background(), pb.PlanChoice_PLAN_CHOICE_IMPLEMENT, fresh); !errors.Is(err, errRemoteAnswerStale) {
				t.Errorf("pending answer after status refresh=%v", err)
			}
			if !recordFails {
				restartAnswerHarness(t, h)
				if err := h.s.answerPlanBy(context.Background(), pb.PlanChoice_PLAN_CHOICE_IMPLEMENT, answerRaceOrigin(h.s, remotePlan, "", "after-replay")); !errors.Is(err, errRemoteAnswerStale) {
					t.Errorf("replayed pending plan became actionable: %v", err)
				}
			}
			if err := h.s.emit(&pb.Event{Kind: &pb.Event_SessionState{SessionState: &pb.SessionState{PlanOpen: false}}}, plan()); err != nil {
				t.Fatal(err)
			}
			if err := h.s.answerPlanBy(context.Background(), pb.PlanChoice_PLAN_CHOICE_IMPLEMENT, answerRaceOrigin(h.s, remotePlan, "", "new-plan-request")); err != nil {
				t.Fatalf("new prompt refused: %v", err)
			}
		})
	}
}

type blockedQuestionAnswer struct {
	agent.Adapter
	entered, release chan struct{}
	fail             bool
	calls            atomic.Int32
}

type blockedQuestionControl struct {
	terminal
	entered, release chan struct{}
	calls            atomic.Int32
}

func (t *blockedQuestionControl) Control() (control, error) {
	if t.calls.Add(1) == 1 {
		close(t.entered)
		<-t.release
	}
	return t.terminal.Control()
}

func TestQueuedQuestionAnswerRechecksPendingAfterTerminalLock(t *testing.T) {
	h := newHarness(t, Config{})
	if err := h.s.emit(&pb.Event{Kind: &pb.Event_QuestionAsked{QuestionAsked: &pb.QuestionAsked{QuestionId: "q", Questions: []*pb.Question{{Text: "Which language?", Options: []*pb.QuestionOption{{Label: "Go"}}}}}}}); err != nil {
		t.Fatal(err)
	}
	h.term.setScreen(answerRaceQuestionScreen)
	term := &blockedQuestionControl{terminal: h.s.term, entered: make(chan struct{}), release: make(chan struct{})}
	h.s.term = term
	first := answerRaceOrigin(h.s, remoteQuestion, "q", "first-queued-question")
	second := answerRaceOrigin(h.s, remoteQuestion, "q", "second-queued-question")
	a, b := make(chan error, 1), make(chan error, 1)
	go func() { a <- h.s.answerBy("q", []int32{1}, nil, first) }()
	<-term.entered // first holds ctlMu, but has not installed answeredBy yet
	started := make(chan struct{})
	go func() { close(started); b <- h.s.answerBy("q", []int32{1}, nil, second) }()
	<-started
	// Let the second request pass its early check and queue for ctlMu.
	time.Sleep(100 * time.Millisecond)
	close(term.release)
	if err := <-a; err != nil {
		t.Fatal(err)
	}
	if err := <-b; !errors.Is(err, errRemoteAnswerStale) {
		t.Errorf("queued question answer=%v, want stale", err)
	}
	if w := h.term.written(); len(w) != 1 {
		t.Errorf("queued request repeated question keys: %q", w)
	}
	if n := term.calls.Load(); n != 2 {
		t.Fatalf("did not exercise the post-control-lock check: %d Control calls", n)
	}
}

func (a *blockedQuestionAnswer) Answer(term agent.Term, q *pb.QuestionAsked, index *int32, options []int32) error {
	if a.calls.Add(1) == 1 {
		if err := a.Adapter.Answer(term, q, index, options); err != nil {
			return err
		}
		close(a.entered)
		<-a.release
		if a.fail {
			return errors.New("synthetic key delivery failure")
		}
		return nil
	}
	return a.Adapter.Answer(term, q, index, options)
}

func TestConcurrentQuestionAnswersRespectPendingAndRollback(t *testing.T) {
	for _, fails := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure-rollback"}[fails], func(t *testing.T) {
			h := newHarness(t, Config{})
			q := func() *pb.Event {
				return &pb.Event{Kind: &pb.Event_QuestionAsked{QuestionAsked: &pb.QuestionAsked{QuestionId: "q", Questions: []*pb.Question{{Text: "Which language?", Options: []*pb.QuestionOption{{Label: "Go"}}}}}}}
			}
			if err := h.s.emit(q()); err != nil {
				t.Fatal(err)
			}
			h.term.setScreen(answerRaceQuestionScreen)
			adapter := &blockedQuestionAnswer{Adapter: h.s.ag, entered: make(chan struct{}), release: make(chan struct{}), fail: fails}
			h.s.ag = adapter
			first := answerRaceOrigin(h.s, remoteQuestion, "q", "first-question-request")
			second := answerRaceOrigin(h.s, remoteQuestion, "q", "second-question-request")
			a, b := make(chan error, 1), make(chan error, 1)
			go func() { a <- h.s.answerBy("q", []int32{1}, nil, first) }()
			<-adapter.entered
			go func() { b <- h.s.answerBy("q", []int32{1}, nil, second) }()
			// Pending is already set before keys are sent. Both the early
			// request check and the post-ctlMu check must reject it.
			var secondErr error
			select {
			case secondErr = <-b:
			case <-time.After(300 * time.Millisecond):
				t.Error("a request with a pending answer waited instead of refusing stale")
			}
			close(adapter.release)
			firstErr := <-a
			if secondErr == nil {
				secondErr = <-b
			}
			if !errors.Is(secondErr, errRemoteAnswerStale) {
				t.Errorf("second question answer=%v", secondErr)
			}
			if fails {
				if firstErr == nil {
					t.Error("first answer unexpectedly succeeded")
				}
				if err := h.s.answerBy("q", []int32{1}, nil, answerRaceOrigin(h.s, remoteQuestion, "q", "retry-after-failure")); err != nil {
					t.Errorf("failed answer did not roll back: %v", err)
				}
			} else {
				if firstErr != nil {
					t.Error(firstErr)
				}
				if got := adapter.calls.Load(); got != 1 {
					t.Errorf("answered same question %d times", got)
				}
				// Use the transcript annotation/commit path to settle the pending claim.
				ev := &pb.Event{Kind: &pb.Event_QuestionAnswered{QuestionAnswered: &pb.QuestionAnswered{QuestionId: "q"}}}
				consume := h.s.annotate([]*pb.Event{ev})
				if err := h.s.emit(ev); err != nil {
					t.Fatal(err)
				}
				h.s.mu.Lock()
				consume()
				h.s.mu.Unlock()
				if err := h.s.emit(q()); err != nil {
					t.Fatal(err)
				}
				if err := h.s.answerBy("q", []int32{1}, nil, answerRaceOrigin(h.s, remoteQuestion, "q", "new-question-request")); err != nil {
					t.Errorf("settled question stayed blocked: %v", err)
				}
			}
		})
	}
}

func TestConcurrentApprovalAnswersDeliverOnceAndRefuseStale(t *testing.T) {
	for _, recordFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "denial-recording-failure"}[recordFails], func(t *testing.T) {
			h := newHarness(t, Config{})
			if err := h.s.emit(&pb.Event{Kind: &pb.Event_ApprovalRequested{ApprovalRequested: &pb.ApprovalRequested{ApprovalId: "a", Command: "true"}}}); err != nil {
				t.Fatal(err)
			}
			pending := &approval{id: "a", decision: make(chan decision, 2), deadline: time.Now().Add(time.Minute)}
			h.s.approvals["a"] = pending
			first := answerRaceOrigin(h.s, remoteApproval, "a", "first-approval-request")
			second := answerRaceOrigin(h.s, remoteApproval, "a", "second-approval-request")
			if recordFails {
				h.s.log.Close()
			}
			start, results := make(chan struct{}), make(chan error, 2)
			for _, o := range []*pb.Origin{first, second} {
				go func() { <-start; results <- h.s.resolveBy("a", false, false, "denied", o) }()
			}
			close(start)
			stale, delivered := 0, 0
			for range 2 {
				err := <-results
				if errors.Is(err, errRemoteAnswerStale) {
					stale++
				} else if err == nil || recordFails && strings.Contains(err.Error(), "approval denied but could not record result") {
					delivered++
				} else {
					t.Errorf("unexpected answer result: %v", err)
				}
			}
			if stale != 1 || delivered != 1 || len(pending.decision) != 1 {
				t.Fatalf("stale=%d delivered=%d hook decisions=%d", stale, delivered, len(pending.decision))
			}
		})
	}
}

func TestFailedPlanKeysAndUndeliveredApprovalCanRetry(t *testing.T) {
	t.Run("plan-key-failure", func(t *testing.T) {
		h := newHarness(t, Config{})
		h.term.setScreen(answerRacePlanScreen)
		if err := h.s.emit(&pb.Event{Kind: &pb.Event_PlanProposed{PlanProposed: &pb.PlanProposed{Text: "synthetic plan"}}}); err != nil {
			t.Fatal(err)
		}
		origin := answerRaceOrigin(h.s, remotePlan, "", "failed-keys")
		h.term.failWrites = true
		if err := h.s.answerPlanBy(context.Background(), pb.PlanChoice_PLAN_CHOICE_IMPLEMENT, origin); err == nil {
			t.Fatal("key failure hidden")
		}
		h.term.failWrites = false
		if err := h.s.answerPlanBy(context.Background(), pb.PlanChoice_PLAN_CHOICE_IMPLEMENT, answerRaceOrigin(h.s, remotePlan, "", "retry-keys")); err != nil {
			t.Fatalf("failed keys left prompt blocked: %v", err)
		}
	})
	t.Run("approval-recording-failure-before-delivery", func(t *testing.T) {
		h := newHarness(t, Config{})
		if err := h.s.emit(&pb.Event{Kind: &pb.Event_ApprovalRequested{ApprovalRequested: &pb.ApprovalRequested{ApprovalId: "a", Command: "true"}}}); err != nil {
			t.Fatal(err)
		}
		pending := &approval{id: "a", decision: make(chan decision, 1), deadline: time.Now().Add(time.Minute)}
		h.s.approvals["a"] = pending
		origin := answerRaceOrigin(h.s, remoteApproval, "a", "failed-approval")
		h.s.log.Close()
		if err := h.s.resolveBy("a", true, false, "", origin); err == nil {
			t.Fatal("recording failure hidden")
		}
		if pending.claimed || len(pending.decision) != 0 {
			t.Fatal("failed allow reached the hook")
		}
		repairEventLog(t, h)
		if err := h.s.resolveBy("a", true, false, "", answerRaceOrigin(h.s, remoteApproval, "a", "retry-approval")); err != nil {
			t.Fatalf("undelivered allow stayed blocked: %v", err)
		}
		if !pending.claimed || len(pending.decision) != 1 {
			t.Fatal("retried allow not delivered exactly once")
		}
	})
}

// A client answers a multi-question call one index at a time. The first
// answer stays pending until the transcript records the whole call, which
// must not block the next index, while repeating an entered index stays stale.
func TestRemoteMultiQuestionAnswersEachIndex(t *testing.T) {
	h := newHarness(t, Config{})
	if err := h.s.emit(&pb.Event{Kind: &pb.Event_QuestionAsked{QuestionAsked: &pb.QuestionAsked{
		QuestionId: "q", Questions: []*pb.Question{{Text: "Which time zone?"}, {Text: "Which model?"}},
	}}}); err != nil {
		t.Fatal(err)
	}
	first, second := int32(0), int32(1)
	h.term.setScreen("  \x1b[2mQuestion 1/2 (2 unanswered)\x1b[0m\n  Which time zone?\n  \x1b[1;7m› 1. Beijing\n")
	if err := h.s.answerBy("q", []int32{1}, &first, answerRaceOrigin(h.s, remoteQuestion, "q", "first-index")); err != nil {
		t.Fatal(err)
	}
	if err := h.s.answerBy("q", []int32{1}, &first, answerRaceOrigin(h.s, remoteQuestion, "q", "repeat-first-index")); !errors.Is(err, errRemoteAnswerStale) {
		t.Errorf("repeated first index=%v, want stale", err)
	}
	h.term.setScreen("  \x1b[2mQuestion 2/2 (1 unanswered)\x1b[0m\n  Which model?\n  \x1b[1;7m› 1. Opus\n")
	if err := h.s.answerBy("q", []int32{1}, &second, answerRaceOrigin(h.s, remoteQuestion, "q", "second-index")); err != nil {
		t.Fatalf("second index after the first: %v", err)
	}
	if err := h.s.answerBy("q", []int32{1}, &second, answerRaceOrigin(h.s, remoteQuestion, "q", "repeat-second-index")); !errors.Is(err, errRemoteAnswerStale) {
		t.Errorf("repeated second index=%v, want stale", err)
	}
	if w := h.term.written(); len(w) != 2 {
		t.Errorf("writes %q, want one per index", w)
	}
	ev := &pb.Event{Kind: &pb.Event_QuestionAnswered{QuestionAnswered: &pb.QuestionAnswered{QuestionId: "q"}}}
	h.s.annotate([]*pb.Event{ev})
	if o := ev.GetOrigin(); o.GetRequestId() != "second-index" {
		t.Errorf("recorded origin %v, want the last answer", o)
	}
}
