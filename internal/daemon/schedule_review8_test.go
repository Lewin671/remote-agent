package daemon

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/Lewin671/remote-agent/internal/eventlog"
	"github.com/Lewin671/remote-agent/internal/schedule"
	"github.com/Lewin671/remote-agent/internal/tail"
)

func scheduleTranscriptHarness(t *testing.T, claude bool) *harness {
	t.Helper()
	var h *harness
	if claude {
		h = claudeHarness(t, Config{})
	} else {
		h = newHarness(t, Config{})
	}
	h.d.sessions = map[string]*session{h.s.meta.ID: h.s}
	if err := h.d.loadSchedules(); err != nil {
		t.Fatal(err)
	}
	// Bind the path without reading: no poll or PermissionRequest hook has run.
	h.s.reader = tail.NewReader(filepath.Join(h.dir, "rollout.jsonl"), 0)
	return h
}

func writeScheduledTranscriptInput(t *testing.T, h *harness, turn string, claude bool) *schedule.Task {
	t.Helper()
	now := time.Now()
	spec := taskSpec(schedule.Trigger{Type: "at", At: &now})
	spec.Title = "Task " + turn
	spec.Message = "Scheduled prompt " + turn
	task := createTask(t, h, spec, "")
	h.d.tickSchedules(now.Add(time.Second))
	task = readTask(t, h.d, task.ID)
	if task.Active == nil {
		t.Fatal("no dispatch")
	}
	// Model the delivery boundary: the written input is remembered for native
	// transcript matching, but its receipt has not been read or projected yet.
	h.s.mu.Lock()
	for i, in := range h.s.queue.Queue {
		if in.RequestID == task.Active.RequestID {
			h.s.rememberLocked(in)
			h.s.queue.Queue = append(h.s.queue.Queue[:i], h.s.queue.Queue[i+1:]...)
			break
		}
	}
	err := h.s.queue.save(h.s.dir)
	h.s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	writeTranscriptInput(h, turn, spec.Message, claude)
	return task
}

func writeTranscriptInput(h *harness, turn, text string, claude bool) {
	raw, _ := json.Marshal(text)
	if claude {
		h.writeRollout(`{"type":"user","promptId":"` + turn + `","origin":{"kind":"human"},"permissionMode":"bypassPermissions","message":{"role":"user","content":` + string(raw) + `}}`)
	} else {
		h.writeRollout(`{"type":"event_msg","payload":{"type":"task_started","turn_id":"`+turn+`"}}`,
			`{"type":"event_msg","payload":{"type":"item_completed","turn_id":"`+turn+`","item":{"type":"UserMessage","content":[{"type":"text","text":`+string(raw)+`}]}}}`)
	}
}

func writeTranscriptEnd(h *harness, turn string, claude bool) {
	if claude {
		h.writeRollout(`{"type":"system","subtype":"turn_duration","durationMs":1}`)
	} else {
		h.writeRollout(`{"type":"event_msg","payload":{"type":"task_complete","turn_id":"` + turn + `"}}`)
	}
}

func TestNotifyUserReadsTranscriptBeforeAdmission(t *testing.T) {
	for _, claude := range []bool{false, true} {
		for _, previous := range []bool{false, true} {
			t.Run(map[bool]string{false: "codex", true: "claude"}[claude]+map[bool]string{false: "/first", true: "/next"}[previous], func(t *testing.T) {
				h := scheduleTranscriptHarness(t, claude)
				defer h.s.log.Close()
				var old *schedule.Task
				if previous {
					old = writeScheduledTranscriptInput(t, h, "old", claude)
					if !h.s.readRollout() {
						t.Fatal("could not establish old cached turn")
					}
					for i := 0; i < 3; i++ {
						if err := notifyTest(t, h, "Old result", ""); err != nil {
							t.Fatal(err)
						}
					}
					writeTranscriptEnd(h, "old", claude)
				}
				task := writeScheduledTranscriptInput(t, h, "next", claude)
				cachedRun := h.s.scheduledRun
				if h.s.scheduledTask == task.ID {
					t.Fatal("test accidentally polled the new turn")
				}
				if previous && cachedRun != old.Active.ID || !previous && cachedRun != "" {
					t.Fatal("unexpected cached context")
				}
				if err := notifyTest(t, h, "Fresh result", ""); err != nil {
					t.Fatalf("legitimate pre-poll MCP notification rejected: %v", err)
				}
				events := h.events()
				notice := events[len(events)-1]
				n := notice.GetScheduledNotification()
				if n == nil || n.TaskId != task.ID || n.RunId != task.Active.ID || n.Title != h.s.info().Title || notice.TurnId != "next" || h.s.scheduledNotifications != 1 {
					t.Fatalf("notification used stale context: %v", notice)
				}
				h.d.tickSchedules(time.Now())
				got := readTask(t, h.d, task.ID)
				if got.Active == nil || !got.Active.MessageSeen || got.Active.NotificationCount != 1 || got.Active.NotificationText != "Fresh result" {
					t.Fatalf("notification did not reach correct run: %+v", got)
				}
				if previous {
					prior := readTask(t, h.d, old.ID)
					if len(prior.Runs) != 1 || prior.Runs[0].ID != old.Active.ID || prior.Runs[0].NotificationCount != 3 {
						t.Fatalf("new notice attributed to previous run: %+v", prior)
					}
				}
			})
		}
	}
}

func TestNotifyUserRequiresCommittedTranscript(t *testing.T) {
	for _, fault := range []string{"read", "commit"} {
		t.Run(fault, func(t *testing.T) {
			h := scheduleTranscriptHarness(t, false)
			defer func() { h.s.log.Close() }()
			task := writeScheduledTranscriptInput(t, h, "old", false)
			if !h.s.readRollout() {
				t.Fatal("could not establish old context")
			}
			writeTranscriptEnd(h, "old", false)
			writeTranscriptInput(h, "human", "Human takes over", false)
			reader := h.s.reader
			if fault == "read" {
				h.s.reader = tail.NewReader(h.dir, 0)
			} else if err := h.s.log.Close(); err != nil {
				t.Fatal(err)
			}
			err := notifyTest(t, h, "Must not use stale context", "")
			if connect.CodeOf(err) != connect.CodeUnavailable || !strings.Contains(err.Error(), "retry notify_user") {
				t.Fatalf("failure did not return clear retryable error: %v", err)
			}
			if h.s.scheduledRun != task.Active.ID || h.s.scheduledNotifications != 0 {
				t.Fatal("failed transcript read accepted notification or projected uncommitted input")
			}
			for _, e := range h.events() {
				if e.GetScheduledNotification() != nil {
					t.Fatal("notification committed using stale context")
				}
			}
			if fault == "read" {
				h.s.reader = reader
			} else {
				h.s.log, err = eventlog.Open(filepath.Join(h.dir, "events.jsonl"))
				if err != nil {
					t.Fatal(err)
				}
			}
			err = notifyTest(t, h, "No longer scheduled", "")
			if err == nil || connect.CodeOf(err) == connect.CodeUnavailable || !strings.Contains(err.Error(), "ordinary turns") {
				t.Fatalf("recovery used old scheduled context: %v", err)
			}
			if h.s.scheduledTask != "" || h.s.scheduledRun != "" {
				t.Fatal("fresh human input did not clear context")
			}
		})
	}
}
