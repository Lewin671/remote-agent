package daemon

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/codex"
	"github.com/Lewin671/remote-agent/internal/eventlog"
	"github.com/Lewin671/remote-agent/internal/tmux"
)

// Accepted input and request IDs survive a daemon restart.
func TestQueueSurvivesRestart(t *testing.T) {
	h := newHarness(t, Config{})
	h.term.setScreen(draftScreen)
	if _, err := h.s.send("hello", "r1"); err != nil {
		t.Fatal(err)
	}
	m := h.s.meta
	h.s.log.Close()
	h.open(m)
	if n := h.s.info().QueuedInputs; n != 1 {
		t.Fatalf("queued after restart: %d", n)
	}
	if pos, _ := h.s.send("hello", "r1"); pos != 0 {
		t.Fatal("retried request accepted twice after restart")
	}
	h.term.setScreen(idleScreen)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.s.start(ctx)
	eventually(t, "delivery after restart", func() bool { return len(h.term.written()) == 1 })
	// Let the session finish settling the message before the test's
	// directory is removed.
	cancel()
	<-h.s.done
}

// A message that was being written when the daemon died is settled from
// history after the restart and never written again.
func TestInflightIsSettledNotReplayed(t *testing.T) {
	synctest.Test(t, testInflightIsSettledNotReplayed)
}

func testInflightIsSettledNotReplayed(t *testing.T) {
	for _, submitted := range []bool{true, false} {
		h := newHarness(t, Config{SubmitTimeout: 100 * time.Millisecond})
		off := int64(0)
		q := &queueState{Queue: []input{{Text: "one", RequestID: "r1", Inflight: &off}, {Text: "two", RequestID: "r2"}}}
		if err := q.save(h.dir); err != nil {
			t.Fatal(err)
		}
		if submitted {
			os.WriteFile(h.d.cfg.HistoryPath, []byte(`{"session_id":"x","text":"one"}`+"\n"), 0o600)
		}
		m := h.s.meta
		h.s.log.Close()
		h.open(m)
		ctx, cancel := context.WithCancel(context.Background())
		h.s.start(ctx)
		eventually(t, "second message written", func() bool { return len(h.term.written()) >= 1 })
		time.Sleep(50 * time.Millisecond)
		cancel()
		if w := h.term.written(); len(w) != 1 || !strings.Contains(w[0], "two") {
			t.Fatalf("submitted=%v: writes %q", submitted, w)
		}
		want := pb.InputStatus_INPUT_STATUS_UNCONFIRMED
		if submitted {
			want = pb.InputStatus_INPUT_STATUS_SUBMITTED
		}
		if got := inputStatus(h, "r1"); got != want {
			t.Fatalf("submitted=%v: r1 status %v", submitted, got)
		}
	}
}

func inputStatus(h *harness, requestID string) pb.InputStatus {
	var st pb.InputStatus
	for _, e := range h.events() {
		if s := e.GetInputState(); s != nil && s.RequestId == requestID {
			st = s.Status
		}
	}
	return st
}

// Without a history line, a user message in the rollout confirms the
// submission; with neither, the message is reported unconfirmed, not
// silently dropped and not sent again.
func TestSubmissionConfirmation(t *testing.T) {
	synctest.Test(t, testSubmissionConfirmation)
}

func testSubmissionConfirmation(t *testing.T) {
	h := newHarness(t, Config{SubmitTimeout: 200 * time.Millisecond})
	h.term.history = filepath.Join(h.dir, "elsewhere.jsonl") // Codex writes no history
	h.s.bindRollout("codex-1", filepath.Join(h.dir, "rollout.jsonl"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.s.start(ctx)

	h.s.send("seen in rollout", "r1")
	eventually(t, "write", func() bool { return len(h.term.written()) == 1 })
	h.writeRollout(`{"timestamp":"2026-09-29T10:00:00Z","type":"event_msg","payload":{"type":"item_completed","turn_id":"t1","item":{"type":"UserMessage","id":"u1","content":[{"type":"text","text":"seen in rollout"}]}}}`)
	eventually(t, "r1 submitted", func() bool { return inputStatus(h, "r1") == pb.InputStatus_INPUT_STATUS_SUBMITTED })

	h.s.send("never recorded", "r2")
	eventually(t, "r2 unconfirmed", func() bool { return inputStatus(h, "r2") == pb.InputStatus_INPUT_STATUS_UNCONFIRMED })
	time.Sleep(100 * time.Millisecond)
	if n := len(h.term.written()); n != 2 {
		t.Fatalf("unconfirmed message written again: %d writes", n)
	}
	if h.s.info().QueuedInputs != 0 {
		t.Fatal("queue not empty")
	}
}

// A failed save leaves neither the message nor its request ID behind, so
// a retry is accepted.
func TestSendSaveFailureRollsBack(t *testing.T) {
	h := newHarness(t, Config{})
	if err := os.Chmod(h.dir, 0o500); err != nil {
		t.Fatal(err)
	}
	_, err := h.s.send("x", "r1")
	os.Chmod(h.dir, 0o700)
	if err == nil {
		t.Skip("directory is writable despite chmod (running as root?)")
	}
	if pos, err := h.s.send("x", "r1"); err != nil || pos != 1 {
		t.Fatalf("retry after failed save: pos %d, %v", pos, err)
	}
}

// answer checks, after taking the hold, that the question is still open
// and is the one on screen.
func TestAnswerRevalidates(t *testing.T) {
	h := newHarness(t, Config{})
	h.s.emit(&pb.Event{Kind: &pb.Event_QuestionAsked{QuestionAsked: &pb.QuestionAsked{
		QuestionId: "q1", Questions: []*pb.Question{{Text: "Which language should the script use?"}},
	}}})
	other := "  \x1b[2mQuestion 1/1 (1 unanswered)\x1b[0m\n  Which database should we pick?\n  \x1b[1;7m› 1. Postgres\n"
	h.term.setScreen(other)
	if err := h.s.answer("q1", []int32{1}, nil); err == nil {
		t.Fatal("answered a different question")
	}
	mine := "  \x1b[2mQuestion 1/1 (1 unanswered)\x1b[0m\n  Which language should the\n  script use?\n  \x1b[1;7m› 1. Go\n"
	h.term.setScreen(mine)
	if err := h.s.answer("q1", []int32{1}, nil); err != nil {
		t.Fatal(err)
	}
	if w := h.term.written(); len(w) != 1 || w[0] != "1" {
		t.Fatalf("writes %q", w)
	}
}

// With a question index, the option is only entered while that question
// of a multi-question call is showing.
func TestAnswerChecksQuestionIndex(t *testing.T) {
	h := newHarness(t, Config{})
	h.s.emit(&pb.Event{Kind: &pb.Event_QuestionAsked{QuestionAsked: &pb.QuestionAsked{
		QuestionId: "q1", Questions: []*pb.Question{{Text: "Which language should the script use?"}, {Text: "Which database should we pick?"}},
	}}})
	h.term.setScreen("  \x1b[2mQuestion 2/2 (1 unanswered)\x1b[0m\n  Which database should we pick?\n  \x1b[1;7m› 1. Postgres\n")
	first, second, bad := int32(0), int32(1), int32(2)
	if err := h.s.answer("q1", []int32{1}, &first); err == nil {
		t.Fatal("answered question 1 while question 2 is showing")
	}
	if err := h.s.answer("q1", []int32{1}, &bad); err == nil {
		t.Fatal("accepted an index out of range")
	}
	if err := h.s.answer("q1", []int32{2}, &second); err != nil {
		t.Fatal(err)
	}
	if w := h.term.written(); len(w) != 1 || w[0] != "2" {
		t.Fatalf("writes %q", w)
	}
}

// interrupt rechecks the turn after taking the control lock.
func TestInterruptRechecksTurn(t *testing.T) {
	h := newHarness(t, Config{})
	h.writeRollout(`{"timestamp":"2026-09-29T10:00:00Z","type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}`)
	h.s.bindRollout("codex-1", filepath.Join(h.dir, "rollout.jsonl"))
	h.writeRollout(`{"timestamp":"2026-09-29T10:00:01Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"t1"}}`)
	h.s.mu.Lock()
	h.s.running = true // stale, as if read before the turn ended
	h.s.mu.Unlock()
	if ok, err := h.s.interrupt(); ok || err != nil {
		t.Fatalf("interrupt = %v, %v", ok, err)
	}
	if w := h.term.written(); len(w) != 0 {
		t.Fatalf("Esc sent to an idle session: %q", w)
	}
}

// After a restart, an async question's rendering as an agent message is
// still suppressed.
func TestAsyncQuestionSurvivesRestart(t *testing.T) {
	h := newHarness(t, Config{})
	h.writeRollout(`{"timestamp":"2026-09-29T10:00:00Z","type":"response_item","payload":{"type":"function_call","name":"request_user_input_async","arguments":"{\"questions\":[{\"title\":\"Why?\"}]}","call_id":"call-a"}}`)
	h.s.bindRollout("codex-1", filepath.Join(h.dir, "rollout.jsonl"))
	m := h.s.meta
	h.s.log.Close()
	h.writeRollout(`{"timestamp":"2026-09-29T10:00:01Z","type":"event_msg","payload":{"type":"item_completed","turn_id":"t1","item":{"type":"AgentMessage","id":"call-a","content":[{"text":"Why?"}]}}}`)
	h.open(m)
	h.s.readRollout()
	for _, e := range h.events() {
		if e.GetAgentMessage() != nil {
			t.Fatal("async question duplicated as an agent message")
		}
	}
}

// Identical text submitted by another Codex session does not confirm
// this session's injection.
func TestHistoryMatchesSession(t *testing.T) {
	h := newHarness(t, Config{})
	path := h.d.cfg.HistoryPath
	hist := h.s.newHistory(-1)
	os.WriteFile(path, []byte(`{"session_id":"other","text":"same"}`+"\n"), 0o600)
	if hist.submitted(input{Text: "same"}.matches, "mine") {
		t.Fatal("another session's line counted")
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"session_id":"mine","text":"same"}` + "\n")
	f.Close()
	if !hist.submitted(input{Text: "same"}.matches, "mine") {
		t.Fatal("own line not counted")
	}
}

// A reply that returns success is the decision Codex gets, even when it
// races the timeout.
func TestApprovalReplyRacingTimeout(t *testing.T) {
	h := newHarness(t, Config{ApprovalTimeout: time.Millisecond})
	h.writeRollout(turnContextLine("user"))
	for i := 0; i < 400; i++ {
		out := make(chan []byte, 1)
		go func() {
			b, _ := h.s.hook(context.Background(), permissionPayload(h))
			out <- b
		}()
		var id string
		timedOut := false
		for id == "" && !timedOut {
			h.s.mu.Lock()
			for k, a := range h.s.approvals {
				if !a.claimed {
					id = k
				}
			}
			h.s.mu.Unlock()
			if id == "" {
				select {
				case <-out:
					// On a busy machine the request timed out before it
					// was seen here: nothing raced in this round.
					timedOut = true
				case <-time.After(50 * time.Microsecond):
				}
			}
		}
		if timedOut {
			continue
		}
		time.Sleep(time.Duration(700+(i%12)*50) * time.Microsecond)
		err := h.s.resolve(id, true, "")
		b := <-out
		allowed := strings.Contains(string(b), `"allow"`)
		if err == nil && !allowed {
			t.Fatalf("round %d: approve succeeded but Codex got %s", i, b)
		}
		if err != nil && allowed {
			t.Fatalf("round %d: approve failed but Codex got allow", i)
		}
	}
}

// A reply after the deadline is refused even if the timer has not fired,
// e.g. when the machine was asleep.
func TestApprovalReplyAfterDeadline(t *testing.T) {
	h := newHarness(t, Config{ApprovalTimeout: time.Minute})
	h.writeRollout(turnContextLine("user"))
	out := make(chan []byte, 1)
	ctx, cancel := context.WithCancel(context.Background())
	// The request's timer still has a minute to run: changing a.deadline
	// below only changes the reply check. Cancel and join the hook before
	// the harness removes its files, including on an assertion failure.
	t.Cleanup(func() {
		cancel()
		<-out
	})
	go func() {
		b, _ := h.s.hook(ctx, permissionPayload(h))
		out <- b
	}()
	var id string
	for id == "" {
		h.s.mu.Lock()
		for k, a := range h.s.approvals {
			id = k
			a.deadline = time.Now().Add(-time.Second)
		}
		h.s.mu.Unlock()
		time.Sleep(time.Millisecond)
	}
	if err := h.s.resolve(id, true, ""); !errors.Is(err, errExpired) {
		t.Fatalf("late reply: %v", err)
	}
	h.s.mu.Lock()
	a := h.s.approvals[id]
	h.s.mu.Unlock()
	if a == nil || a.claimed {
		t.Fatal("a refused late reply must leave the approval to the timer")
	}
}

// Control operations do not interleave: nothing is written between another
// operation's hold and release.
func TestControlOperationsAreSerialized(t *testing.T) {
	h := newHarness(t, Config{})
	h.s.emit(&pb.Event{Kind: &pb.Event_TurnStarted{TurnStarted: &pb.TurnStarted{}}})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); h.s.inject(input{Text: "msg"}) }()
		go func() { defer wg.Done(); h.s.interrupt() }()
	}
	wg.Wait()
	h.term.mu.Lock()
	ops := append([]string(nil), h.term.ops...)
	h.term.mu.Unlock()
	held := false
	writesInHold := 0
	for _, op := range ops {
		switch op {
		case "hold":
			if held {
				t.Fatal("nested hold")
			}
			held, writesInHold = true, 0
		case "release":
			held = false
		case "write":
			if held {
				writesInHold++
				if writesInHold > 1 {
					t.Fatalf("a second write landed inside a hold: %v", ops)
				}
			}
		}
	}
}

// If the inflight mark cannot be saved, the message stays queued unwritten.
func TestMarkInflightFailureKeepsMessageQueued(t *testing.T) {
	h := newHarness(t, Config{})
	if _, err := h.s.send("x", "r1"); err != nil {
		t.Fatal(err)
	}
	os.Chmod(h.dir, 0o500)
	written, err := h.s.inject(input{Text: "x", RequestID: "r1"})
	os.Chmod(h.dir, 0o700)
	if err == nil {
		t.Skip("directory is writable despite chmod")
	}
	if written != notWritten || len(h.term.written()) != 0 {
		t.Fatal("wrote although the mark was not saved")
	}
	if h.s.queue.Queue[0].Inflight != nil {
		t.Fatal("inflight mark left in memory")
	}
}

// Codex records a message when its turn starts, which may be after the
// wait for it ran out: the record then confirms a message that was already
// reported unconfirmed, and no later record confirms it again.
func TestLateRecordConfirmsUnconfirmedMessage(t *testing.T) {
	synctest.Test(t, testLateRecordConfirmsUnconfirmedMessage)
}

func testLateRecordConfirmsUnconfirmedMessage(t *testing.T) {
	h := newHarness(t, Config{SubmitTimeout: 100 * time.Millisecond})
	h.term.history = filepath.Join(h.dir, "elsewhere.jsonl") // Codex writes no history
	h.s.bindRollout("codex-1", filepath.Join(h.dir, "rollout.jsonl"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.s.start(ctx)

	h.s.send("recorded late", "r1")
	eventually(t, "r1 unconfirmed", func() bool { return inputStatus(h, "r1") == pb.InputStatus_INPUT_STATUS_UNCONFIRMED })
	record := func(id string) string {
		return `{"timestamp":"2026-09-29T10:00:00Z","type":"event_msg","payload":{"type":"item_completed","turn_id":"t1","item":{"type":"UserMessage","id":"` + id + `","content":[{"type":"text","text":"recorded late"}]}}}`
	}
	h.writeRollout(record("u1"))
	eventually(t, "r1 submitted", func() bool { return inputStatus(h, "r1") == pb.InputStatus_INPUT_STATUS_SUBMITTED })
	// The same text typed at the computer afterwards is not this message.
	h.writeRollout(record("u2"))
	eventually(t, "second record", func() bool {
		n := 0
		for _, e := range h.events() {
			if e.GetUserMessage() != nil {
				n++
			}
		}
		return n == 2
	})
	time.Sleep(50 * time.Millisecond)
	var got []pb.InputStatus
	for _, e := range h.events() {
		if st := e.GetInputState(); st != nil && st.RequestId == "r1" && st.Stage == pb.InputStage_INPUT_STAGE_UNSPECIFIED {
			got = append(got, st.Status)
		}
	}
	want := []pb.InputStatus{pb.InputStatus_INPUT_STATUS_UNCONFIRMED, pb.InputStatus_INPUT_STATUS_SUBMITTED}
	if !slices.Equal(got, want) {
		t.Fatalf("r1 outcomes %v, want %v", got, want)
	}
	// As when confirmed in time, the state comes just before the record.
	evs := h.events()
	for i, e := range evs {
		if st := e.GetInputState(); st != nil && st.Status == pb.InputStatus_INPUT_STATUS_SUBMITTED {
			if i+1 >= len(evs) || evs[i+1].GetUserMessage().GetRequestId() != "r1" {
				t.Fatalf("no record of r1 right after its late confirmation: %v", evs[i:])
			}
		}
	}
	if n := len(h.term.written()); n != 1 {
		t.Fatalf("written %d times", n)
	}
}

// A write whose reply fails counts as possibly written: it is settled from
// Codex's records and never written again.
func TestAmbiguousWriteIsNotRetried(t *testing.T) {
	synctest.Test(t, testAmbiguousWriteIsNotRetried)
}

func testAmbiguousWriteIsNotRetried(t *testing.T) {
	h := newHarness(t, Config{SubmitTimeout: 100 * time.Millisecond})
	h.term.failWrites = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.s.start(ctx)
	h.s.send("x", "r1")
	eventually(t, "unconfirmed", func() bool { return inputStatus(h, "r1") == pb.InputStatus_INPUT_STATUS_UNCONFIRMED })
	time.Sleep(50 * time.Millisecond)
	if n := len(h.term.written()); n != 1 {
		t.Fatalf("written %d times", n)
	}
}

// A queued message can be canceled until it is being written.
func TestCancelInput(t *testing.T) {
	h := newHarness(t, Config{})
	h.term.setScreen(draftScreen)
	ctx, cancel := context.WithCancel(context.Background())
	defer func() { cancel(); <-h.s.done }()
	h.s.start(ctx)
	for _, id := range []string{"r1", "r2", "r3"} {
		if _, err := h.s.send("msg "+id, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.s.cancelInput("r1"); err != nil {
		t.Fatal(err)
	}
	if err := h.s.cancelInput("r1"); !errors.Is(err, errNotFound) {
		t.Fatalf("second cancel: %v", err)
	}
	if n := h.s.info().QueuedInputs; n != 2 {
		t.Fatalf("queued %d", n)
	}
	if inputStatus(h, "r1") != pb.InputStatus_INPUT_STATUS_CANCELED {
		t.Fatal("no canceled state")
	}
	h.term.setScreen(idleScreen)
	eventually(t, "remaining messages", func() bool { return len(h.term.written()) == 2 })
	w := h.term.written()
	if w[0] != string(codex.PasteBytes("msg r2")) || w[1] != string(codex.PasteBytes("msg r3")) {
		t.Fatalf("writes %q", w)
	}

	// A message marked as being written stays.
	h.s.mu.Lock()
	off := int64(0)
	h.s.queue.Queue = []input{{Text: "x", RequestID: "r4", Inflight: &off}}
	h.s.mu.Unlock()
	if err := h.s.cancelInput("r4"); !errors.Is(err, errWriting) {
		t.Fatalf("cancel while writing: %v", err)
	}
}

// A session that ends with messages still queued says what became of each
// instead of dropping them: never written, canceled because it exited;
// written but unconfirmed, unconfirmed.
func TestExitSettlesQueuedInput(t *testing.T) {
	h := newHarness(t, Config{SubmitTimeout: time.Minute})
	h.term.setScreen(draftScreen) // nothing gets written
	if _, err := h.s.send("never written", "r1"); err != nil {
		t.Fatal(err)
	}
	off := int64(0)
	h.s.mu.Lock()
	h.s.queue.Queue = append([]input{{Text: "written", RequestID: "r0", Inflight: &off, InflightSeq: h.s.log.LastSeq()}}, h.s.queue.Queue...)
	h.s.mu.Unlock()
	h.term.mu.Lock()
	h.term.alive = false
	h.term.mu.Unlock()
	h.s.checkScreen()
	got := map[string]*pb.InputState{}
	for _, e := range h.events() {
		if st := e.GetInputState(); st != nil {
			got[st.RequestId] = st
		}
	}
	if st := got["r1"]; st.GetStatus() != pb.InputStatus_INPUT_STATUS_CANCELED || st.GetReason() != "exited" {
		t.Errorf("queued: %v", st)
	}
	if st := got["r0"]; st.GetStatus() != pb.InputStatus_INPUT_STATUS_UNCONFIRMED {
		t.Errorf("written: %v", st)
	}
	if n := h.s.info().QueuedInputs; n != 0 {
		t.Errorf("still queued: %d", n)
	}
}

// A message leaves the queue only once its outcome is logged: when the
// log cannot be written the message stays, and is settled later instead of
// being gone with clients still shown it waiting.
func TestOutcomeIsLoggedBeforeTheMessageLeavesTheQueue(t *testing.T) {
	h := newHarness(t, Config{SubmitTimeout: 50 * time.Millisecond})
	h.term.setScreen(draftScreen)
	for _, id := range []string{"r1", "r2"} {
		if _, err := h.s.send("msg "+id, id); err != nil {
			t.Fatal(err)
		}
	}
	queued := func() []string {
		q, err := loadQueue(h.dir)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, in := range q.Queue {
			ids = append(ids, in.RequestID)
		}
		return ids
	}
	h.s.log.Close() // the disk is full
	h.s.finishInput(h.s.queue.Queue[0], pb.InputStatus_INPUT_STATUS_UNCONFIRMED, "")
	if err := h.s.cancelInput("r2"); err == nil {
		t.Fatal("cancel reported done though it could not be recorded")
	}
	if ids := queued(); len(ids) != 2 || len(h.s.queue.Queue) != 2 {
		t.Fatalf("left the queue without an outcome: %v", ids)
	}
	log, err := eventlog.Open(filepath.Join(h.dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	h.s.log = log
	h.s.finishInput(h.s.queue.Queue[0], pb.InputStatus_INPUT_STATUS_UNCONFIRMED, "")
	if err := h.s.cancelInput("r2"); err != nil {
		t.Fatal(err)
	}
	if ids := queued(); len(ids) != 0 {
		t.Fatalf("still queued: %v", ids)
	}
	if inputStatus(h, "r1") != pb.InputStatus_INPUT_STATUS_UNCONFIRMED || inputStatus(h, "r2") != pb.InputStatus_INPUT_STATUS_CANCELED {
		t.Fatal("outcomes not logged")
	}
}

// The daemon stopped after logging a message's outcome and before saving
// the queue without it: the next one drops the message instead of settling
// it a second time.
func TestRestartDropsMessagesWhoseOutcomeIsLogged(t *testing.T) {
	h := newHarness(t, Config{})
	h.term.setScreen(draftScreen)
	for _, id := range []string{"r1", "r2", "r3"} {
		if _, err := h.s.send("msg "+id, id); err != nil {
			t.Fatal(err)
		}
	}
	h.s.mu.Lock()
	h.s.queue.Held = []input{{Text: "held", RequestID: "h1"}, {Text: "held too", RequestID: "h2"}}
	h.s.queue.save(h.dir)
	h.s.mu.Unlock()
	before, _ := os.ReadFile(filepath.Join(h.dir, "queue.json"))
	off := int64(0)
	h.s.queue.Queue[0].Inflight = &off
	h.s.finishInput(h.s.queue.Queue[0], pb.InputStatus_INPUT_STATUS_SUBMITTED, "")
	if err := h.s.cancelInput("r3"); err != nil {
		t.Fatal(err)
	}
	h.s.received("h1")
	n := len(h.events())
	// As if none of the three saves had happened.
	if err := os.WriteFile(filepath.Join(h.dir, "queue.json"), before, 0o600); err != nil {
		t.Fatal(err)
	}
	h.reopen()
	if ids := queueIDs(h); len(ids) != 1 || ids[0] != "r2" {
		t.Fatalf("queue after restart %v", ids)
	}
	if held := h.s.queue.Held; len(held) != 1 || held[0].RequestID != "h2" {
		t.Fatalf("held after restart %+v", held)
	}
	if len(h.events()) != n {
		t.Fatal("settled again")
	}
	q, _ := loadQueue(h.dir)
	if len(q.Queue) != 1 || len(q.Held) != 1 {
		t.Fatalf("not saved: %+v", q)
	}
}

// A session that exited while the log could not be written keeps its
// queued messages until their outcomes can be logged; they are never sent.
func TestExitedQueueIsSettledOnTheNextStart(t *testing.T) {
	h := newHarness(t, Config{})
	h.term.setScreen(draftScreen)
	if _, err := h.s.send("never written", "r1"); err != nil {
		t.Fatal(err)
	}
	h.s.log.Close()
	h.s.markExited("stopped")
	if q, _ := loadQueue(h.dir); len(q.Queue) != 1 {
		t.Fatalf("dropped without a word: %+v", q)
	}
	log, err := eventlog.Open(filepath.Join(h.dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	h.s.log = log
	h.reopen()
	h.s.settleExited() // as restoring an exited session does
	if st := inputStates(h, "r1"); st[len(st)-1].Status != pb.InputStatus_INPUT_STATUS_CANCELED || st[len(st)-1].Reason != "exited" {
		t.Fatalf("states %v", st)
	}
	if q, _ := loadQueue(h.dir); len(q.Queue) != 0 {
		t.Fatalf("still queued: %+v", q)
	}
}

// A session that exited while the log could not be written is not resumed
// until its queued messages are settled: started with them still queued,
// it would be sent what was to be canceled.
func TestResumeWaitsForTheExitedQueueToBeSettled(t *testing.T) {
	h := newHarness(t, Config{})
	h.d.sessions[h.s.meta.ID] = h.s
	// No tmux to ask or to start: a resume that got that far fails there.
	h.d.cfg.Tmux = &tmux.Client{Bin: filepath.Join(h.dir, "no-tmux")}
	h.term.setScreen(draftScreen)
	if _, err := h.s.send("never written", "r1"); err != nil {
		t.Fatal(err)
	}
	h.s.log.Close() // the disk is full
	h.s.markExited("stopped")
	if _, err := h.d.resume(h.s.meta.ID); !errors.Is(err, errUnsettled) {
		t.Fatalf("resumed with a message still queued: %v", err)
	}
	h.s.mu.Lock()
	exited, queued := h.s.meta.Exited, len(h.s.queue.Queue)
	h.s.mu.Unlock()
	if !exited || queued != 1 {
		t.Fatalf("exited %v, %d queued", exited, queued)
	}
	log, err := eventlog.Open(filepath.Join(h.dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	h.s.log = log
	if _, err := h.d.resume(h.s.meta.ID); err == nil || errors.Is(err, errUnsettled) {
		t.Fatalf("resume with the log back: %v", err)
	}
	if st := inputStates(h, "r1"); st[len(st)-1].Status != pb.InputStatus_INPUT_STATUS_CANCELED || st[len(st)-1].Reason != "exited" {
		t.Fatalf("states %v", st)
	}
	if q, _ := loadQueue(h.dir); len(q.Queue) != 0 {
		t.Fatalf("still queued: %+v", q)
	}
}

func TestStopWaitsForPTYHostBeforeReadingFinalTranscript(t *testing.T) {
	h := newHarness(t, Config{})
	rollout := filepath.Join(h.dir, "rollout.jsonl")
	h.s.bindRollout("codex-1", rollout)
	term := &barrierHostTerminal{terminal: h.s.term, entered: make(chan struct{}), release: make(chan struct{})}
	h.s.term = term
	defer close(term.release)

	done := make(chan error, 1)
	go func() { done <- h.s.kill() }()
	select {
	case <-term.entered:
	case err := <-done:
		t.Fatalf("stop bypassed the pty-host exit check: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not check the pty-host")
	}
	h.s.mu.Lock()
	exited := h.s.meta.Exited
	h.s.mu.Unlock()
	if exited {
		t.Fatal("session was marked exited while its pty-host was still alive")
	}
	for _, e := range h.events() {
		if e.GetSessionState().GetStatus() == pb.SessionStatus_SESSION_STATUS_EXITED {
			t.Fatal("EXITED was emitted while the pty-host was still alive")
		}
	}

	h.writeRollout(
		taskStarted,
		`{"timestamp":"2026-09-29T10:00:01Z","type":"event_msg","payload":{"type":"item_completed","turn_id":"t1","item":{"type":"AgentMessage","id":"m1","content":[{"text":"written before pty-host exit"}]}}}`,
		`{"timestamp":"2026-09-29T10:00:02Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"t1"}}`,
	)
	term.release <- struct{}{}
	if err := awaitLifecycleResult(t, done); err != nil {
		t.Fatal(err)
	}

	messageAt, exitedAt := -1, -1
	for i, e := range h.events() {
		if m := e.GetAgentMessage(); m != nil && m.Text == "written before pty-host exit" {
			messageAt = i
		}
		if e.GetSessionState().GetStatus() == pb.SessionStatus_SESSION_STATUS_EXITED {
			exitedAt = i
		}
	}
	if messageAt < 0 {
		t.Fatal("final transcript record was not read before stop returned")
	}
	if exitedAt <= messageAt {
		t.Fatalf("EXITED event index %d did not follow final transcript record index %d", exitedAt, messageAt)
	}
}

type barrierHostTerminal struct {
	terminal
	entered, release chan struct{}
}

func (t *barrierHostTerminal) HostAlive(ctx context.Context) (bool, error) {
	close(t.entered)
	select {
	case <-t.release:
		return false, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

func TestResumeWaitsForPreviousPTYHost(t *testing.T) {
	h := newHarness(t, Config{})
	h.d.sessions[h.s.meta.ID] = h.s
	h.d.cfg.Tmux = &tmux.Client{Bin: filepath.Join(h.dir, "missing-tmux"), Socket: "ra-test"}
	h.s.markExited("stopped")
	term := &barrierHostTerminal{terminal: h.s.term, entered: make(chan struct{}), release: make(chan struct{})}
	h.s.term = term
	defer close(term.release)
	done := make(chan error, 1)
	go func() { _, err := h.d.resume(h.s.meta.ID); done <- err }()
	select {
	case <-term.entered:
		// Resume has reached the host-exit check, which cannot complete
		// until this test explicitly reports that the old host is gone.
	case err := <-done:
		t.Fatalf("resume bypassed the previous pty-host: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("resume did not check the previous pty-host")
	}
	term.release <- struct{}{}
	if err := awaitLifecycleResult(t, done); err == nil {
		t.Fatal("resume unexpectedly started with the missing tmux executable")
	}
}

func TestLateRecordIsMatchedAfterRestart(t *testing.T) {
	h := newHarness(t, Config{})
	h.s.bindRollout("", filepath.Join(h.dir, "rollout.jsonl"))
	origin := &pb.Origin{Kind: pb.OriginKind_ORIGIN_KIND_CLIENT, DeviceId: "dev", RequestId: "r1"}
	if _, err := h.s.enqueue("run the checks", "r1", nil, origin); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.s.status = pb.SessionStatus_SESSION_STATUS_IDLE
	go h.s.injectLoop(ctx)
	eventually(t, "r1 submitted", func() bool { return inputStatus(h, "r1") == pb.InputStatus_INPUT_STATUS_SUBMITTED })
	cancel()
	h.reopen()
	user := func(text string) string {
		return `{"timestamp":"2026-09-29T10:00:00Z","type":"event_msg","payload":{"type":"item_completed","turn_id":"t1","item":{"type":"UserMessage","id":"u1","content":[{"type":"text","text":"` + text + `"}]}}}`
	}
	h.writeRollout(user("run the checks"), user("run the checks"), user("typed here"))
	h.s.readRollout()
	var got []*pb.Event
	for _, e := range h.events() {
		if e.GetUserMessage() != nil {
			got = append(got, e)
		}
	}
	if len(got) != 3 {
		t.Fatalf("%d user messages", len(got))
	}
	if got[0].GetUserMessage().RequestId != "r1" || got[0].Origin.GetDeviceId() != "dev" {
		t.Fatalf("the late record lost its message: %v", got[0])
	}
	// It is matched once; what nothing was written for is local.
	for _, e := range got[1:] {
		if e.GetUserMessage().RequestId != "" || e.Origin.GetKind() != pb.OriginKind_ORIGIN_KIND_LOCAL {
			t.Fatalf("tied to a message that was not written for it: %v", e)
		}
	}
}

// A message reported unconfirmed stays one a late record confirms when the
// daemon restarts in between: the restart remembers it as written, and as
// unconfirmed.
func TestLateRecordConfirmsUnconfirmedMessageAfterRestart(t *testing.T) {
	h := newHarness(t, Config{SubmitTimeout: 100 * time.Millisecond})
	h.term.history = filepath.Join(h.dir, "elsewhere.jsonl") // Codex writes no history
	h.s.bindRollout("codex-1", filepath.Join(h.dir, "rollout.jsonl"))
	ctx, cancel := context.WithCancel(context.Background())
	h.s.start(ctx)
	h.s.send("recorded late", "r1")
	eventually(t, "r1 unconfirmed", func() bool { return inputStatus(h, "r1") == pb.InputStatus_INPUT_STATUS_UNCONFIRMED })
	cancel()
	h.reopen()
	h.writeRollout(`{"timestamp":"2026-09-29T10:00:00Z","type":"event_msg","payload":{"type":"item_completed","turn_id":"t1","item":{"type":"UserMessage","id":"u1","content":[{"type":"text","text":"recorded late"}]}}}`)
	h.s.readRollout()
	if got := inputStatus(h, "r1"); got != pb.InputStatus_INPUT_STATUS_SUBMITTED {
		t.Fatalf("r1 is %v after its record came", got)
	}
	evs := h.events()
	for i, e := range evs {
		if m := e.GetUserMessage(); m != nil {
			if m.RequestId != "r1" || i == 0 || evs[i-1].GetInputState().GetStatus() != pb.InputStatus_INPUT_STATUS_SUBMITTED {
				t.Fatalf("the record is not r1's, confirmed just before it: %v", e)
			}
		}
	}
}

// A queue.json that cannot be read holds accepted messages and the request
// IDs that guard against sending twice. The session accepts no input, so
// that nothing overwrites the file, instead of carrying on as if it had
// never been given anything.
func TestUnreadableQueueIsKeptAndBlocksInput(t *testing.T) {
	synctest.Test(t, testUnreadableQueueIsKeptAndBlocksInput)
}

func testUnreadableQueueIsKeptAndBlocksInput(t *testing.T) {
	h := newHarness(t, Config{})
	h.term.setScreen(draftScreen)
	if _, err := h.s.send("accepted", "r1"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(h.dir, "queue.json")
	saved, _ := os.ReadFile(path)
	damaged := saved[:len(saved)/2]
	if err := os.WriteFile(path, damaged, 0o600); err != nil {
		t.Fatal(err)
	}
	h.reopen()
	if _, err := h.s.send("accepted", "r1"); !errors.Is(err, errQueueUnreadable) {
		t.Fatalf("a retried request: %v", err)
	}
	if _, err := h.s.send("new", "r2"); !errors.Is(err, errQueueUnreadable) {
		t.Fatalf("a new message: %v", err)
	}
	h.term.setScreen(idleScreen)
	ctx, cancel := context.WithCancel(context.Background())
	h.s.start(ctx)
	time.Sleep(100 * time.Millisecond)
	h.s.kill()
	cancel()
	<-h.s.done
	if got, _ := os.ReadFile(path); string(got) != string(damaged) {
		t.Fatal("the unreadable queue was overwritten")
	}
	if w := h.term.written(); len(w) != 0 {
		t.Fatalf("wrote %q", w)
	}
	// Once the file is whole again, the next start carries on.
	if err := os.WriteFile(path, saved, 0o600); err != nil {
		t.Fatal(err)
	}
	m := h.s.meta
	m.Exited = false
	h.s.log.Close()
	h.open(m)
	if n := h.s.info().QueuedInputs; n != 1 {
		t.Fatalf("queued after repair: %d", n)
	}
	if pos, err := h.s.send("accepted", "r1"); err != nil || pos != 0 {
		t.Fatalf("a retried request after repair: %d, %v", pos, err)
	}
}

// Two questions of one call that begin alike are told apart by as much of
// their text as it takes: an answer meant for one is not entered while the
// other is showing.
func TestAnswerTellsApartQuestionsThatBeginAlike(t *testing.T) {
	h := newHarness(t, Config{})
	h.s.emit(&pb.Event{Kind: &pb.Event_QuestionAsked{QuestionAsked: &pb.QuestionAsked{
		QuestionId: "q1", Questions: []*pb.Question{
			{Text: "Which implementation should we use for the parser?"},
			{Text: "Which implementation should we use for the API?"},
			{Text: "Which implementation should we use for the API? Pick one"},
		},
	}}})
	h.term.setScreen("  \x1b[2mQuestion 2/3 (2 unanswered)\x1b[0m\n  Which implementation should we\n  use for the API?\n  \x1b[1;7m› 1. Ours\n")
	first, second, third := int32(0), int32(1), int32(2)
	if err := h.s.answer("q1", []int32{1}, &first); !errors.Is(err, errNotAnswering) {
		t.Fatalf("answered the parser question while the API question is showing: %v", err)
	}
	if err := h.s.answer("q1", []int32{1}, &third); !errors.Is(err, errNotAnswering) {
		t.Fatalf("answered the third question while the second is showing: %v", err)
	}
	if w := h.term.written(); len(w) != 0 {
		t.Fatalf("writes %q", w)
	}
	// The second question's whole text is how the third begins: the screen
	// cannot tell whether the second is showing.
	if err := h.s.answer("q1", []int32{1}, &second); !errors.Is(err, errNotAnswering) {
		t.Fatalf("second: %v", err)
	}
	h.term.setScreen("  \x1b[2mQuestion 1/3 (3 unanswered)\x1b[0m\n  Which implementation should we\n  use for the parser?\n  \x1b[1;7m› 1. Ours\n")
	if err := h.s.answer("q1", []int32{2}, &first); err != nil {
		t.Fatal(err)
	}
	if w := h.term.written(); len(w) != 1 || w[0] != "2" {
		t.Fatalf("writes %q", w)
	}
}

// One transcript line gave several events and the machine crashed while
// they were written: only the first reached the disk. The log drops it
// with the rest of its batch, and the line, read again from the saved
// position, gives each event once. The same goes for the copied history
// a fork starts with, which is one batch too.
func TestTornBatchIsReadAgainOnce(t *testing.T) {
	h := claudeHarness(t, Config{})
	h.writeRollout(
		`{"type":"assistant","uuid":"u1","message":{"id":"m1","model":"claude-haiku-4-5-20251001","content":[{"type":"text","text":"first"}]}}`,
		`{"type":"assistant","uuid":"u2","message":{"id":"m2","model":"claude-haiku-4-5-20251001","stop_reason":"tool_use","content":[{"type":"text","text":"second"},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"touch /w/x"}},{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"touch /w/y"}}]}}`,
	)
	h.s.bindRollout("cc-1", filepath.Join(h.dir, "rollout.jsonl"))
	h.s.readRollout()
	kinds := func() []string {
		var out []string
		for i, e := range h.events() {
			if e.Seq != uint64(i+1) {
				t.Fatalf("seq %d at position %d", e.Seq, i+1)
			}
			switch k := e.Kind.(type) {
			case *pb.Event_AgentMessage:
				out = append(out, "message "+k.AgentMessage.Text)
			case *pb.Event_ToolStarted:
				out = append(out, "started "+k.ToolStarted.CallId)
			}
		}
		return out
	}
	want := kinds()
	if len(want) != 4 || want[1] != "message second" || want[3] != "started t2" {
		t.Fatalf("events %v", want)
	}
	m := h.s.meta
	h.s.log.Close()
	// The last line's batch lost its last record.
	path := filepath.Join(h.dir, "events.jsonl")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b[:bytes.LastIndexByte(b[:len(b)-1], '\n')+1], 0o600); err != nil {
		t.Fatal(err)
	}
	h.open(m)
	if got := kinds(); len(got) != 1 || got[0] != "message first" {
		t.Fatalf("events kept of the torn batch: %v", got)
	}
	h.s.readRollout()
	if got := kinds(); !slices.Equal(got, want) {
		t.Fatalf("events after the line was read again: %v, want %v", got, want)
	}

	// A fork's first write.
	dir := t.TempDir()
	path = filepath.Join(dir, "events.jsonl")
	log, err := eventlog.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	forked := &pb.Event{Kind: &pb.Event_SessionForked{SessionForked: &pb.SessionForked{SourceSessionId: m.ID}}}
	if err := log.Append("fork", append(forkHistory(h.events()), forked), "", 0); err != nil {
		t.Fatal(err)
	}
	n := log.LastSeq()
	log.Close()
	b, _ = os.ReadFile(path)
	if err := os.WriteFile(path, b[:bytes.LastIndexByte(b[:len(b)-1], '\n')+1], 0o600); err != nil {
		t.Fatal(err)
	}
	log, err = eventlog.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	if n < 3 || log.LastSeq() != 0 {
		t.Fatalf("%d of %d copied events kept without the record that says where the fork starts", log.LastSeq(), n)
	}
}
