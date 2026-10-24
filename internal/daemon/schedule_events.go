package daemon

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/eventlog"
	"google.golang.org/protobuf/proto"
)

// annotateScheduled runs before append and never changes projected state.
// The same annotations survive relay catch-up, task history pruning and restart.
func (s *session) annotateScheduled(batches []eventlog.Batch) {
	task, run, count := s.scheduledTask, s.scheduledRun, s.scheduledNotifications
	for _, batch := range batches {
		for _, e := range batch.Events {
			if st := e.GetSessionState(); st != nil && st.Detail == "resumed" && humanOrigin(e.Origin) {
				task, run, count = "", "", 0
			}
			if m := e.GetUserMessage(); m != nil {
				// Already annotated native records take this path too. A request ID
				// lookup also covers daemon-generated confirmations of queued input.
				if e.Origin.GetScheduledTaskId() == "" && m.RequestId != "" {
					for _, in := range append(append([]input{}, s.queue.Queue...), s.recent...) {
						if in.RequestID == m.RequestId && in.Origin != nil && in.Origin.ScheduledTaskID != "" {
							e.Origin = in.Origin.pb()
							break
						}
					}
				}
				if humanOrigin(e.Origin) {
					task, run, count = "", "", 0
				}
				if e.Origin.GetScheduledTaskId() != "" {
					task, run, count = e.Origin.ScheduledTaskId, e.Origin.ScheduledRunId, 0
				}
			}
			if n := e.GetScheduledNotification(); n != nil && n.RunId == run {
				count++
			}
			if end := e.GetTurnEnded(); end != nil {
				// Clone: a caller may retain a parser's event or retry a failed append.
				e.Kind = &pb.Event_TurnEnded{TurnEnded: proto.CloneOf(end)}
				end = e.GetTurnEnded()
				end.ScheduledTaskId, end.ScheduledRunId, end.ScheduledNotified = task, run, count > 0
			}
		}
	}
}

func (s *session) observeScheduled(e *pb.Event) {
	s.scheduledCloseAfter = time.Time{}
	if humanOrigin(e.Origin) && (e.GetUserMessage() != nil || e.GetSessionState() != nil && e.GetSessionState().Detail == "resumed") {
		s.scheduledHumanSeq = e.Seq
	}
	if in := e.GetInputState(); in != nil && in.Status == pb.InputStatus_INPUT_STATUS_QUEUED && humanOrigin(e.Origin) {
		s.claimOwnershipLocked()
	}
	if st := e.GetSessionState(); st != nil && st.Detail == "resumed" && humanOrigin(e.Origin) {
		s.takeOverLocked()
	}
	if m := e.GetUserMessage(); m != nil {
		if humanOrigin(e.Origin) {
			s.scheduledTask, s.scheduledRun, s.scheduledNotifications = "", "", 0
		}
		if e.Origin.GetScheduledTaskId() != "" {
			s.scheduledTask, s.scheduledRun, s.scheduledNotifications = e.Origin.ScheduledTaskId, e.Origin.ScheduledRunId, 0
		} else if humanOrigin(e.Origin) {
			// The committed user event is the durable source of truth. Replaying
			// it repairs a failed session.json write without issuing another turn.
			if s.meta.TaskOwned {
				s.takeOverLocked()
			}
		}
	}
	if n := e.GetScheduledNotification(); n != nil && n.RunId == s.scheduledRun {
		s.scheduledNotifications++
	}
	if end := e.GetTurnEnded(); end != nil {
		if end.ScheduledTaskId != "" {
			s.scheduledClose = true
		}
	}
}

// Caller holds scheduleMu, never the inverse of a session lock.
func (d *Daemon) sessionHasScheduledRunLocked(id string) bool {

	s, err := d.exact(id)
	if err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.scheduledTask != "" {
		return true
	}
	for _, task := range d.tasks {
		if run := task.Active; task.Spec.Target.Type == "new_session" && run != nil && run.SessionID == id && !run.MessageSeen && s.scheduledHumanSeq <= run.StartSeq {
			return true
		}
	}
	return false
}

func (d *Daemon) notifyScheduled(q *pb.ScheduleRequest, scope string) ([]byte, error) {
	if scope == "" || scope != q.SessionId {
		return nil, errors.New("notify_user requires a bound session MCP")
	}
	var in struct {
		Text  string `json:"text"`
		Title string `json:"title"`
	}
	if len(q.SpecJson) > 4096 || json.Unmarshal(q.SpecJson, &in) != nil || strings.TrimSpace(in.Text) == "" || !utf8.ValidString(in.Text) || !utf8.ValidString(in.Title) || utf8.RuneCountInString(in.Text) > 200 || utf8.RuneCountInString(in.Title) > 60 {
		return nil, errors.New("notify_user requires text (1-200 characters) and optional title (up to 60 characters)")
	}
	s, err := d.exact(scope)
	if err != nil {
		return nil, err
	}
	// Snapshot the title outside the session lock, preserving the lock order.
	d.scheduleMu.Lock()
	titles := map[string]string{}
	for id, t := range d.tasks {
		titles[id] = t.Spec.Title
	}
	d.scheduleMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running || s.meta.Exited || s.scheduledTask == "" || s.scheduledRun == "" {
		return nil, errors.New("notify_user is only available during a scheduled task's turn; ordinary turns already notify on completion")
	}
	if s.scheduledNotifications >= 3 {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("notify_user is limited to 3 notifications per scheduled run"))
	}
	if in.Title == "" {
		in.Title = string([]rune(titles[s.scheduledTask])[:min(60, utf8.RuneCountInString(titles[s.scheduledTask]))])
	}
	e := &pb.Event{TurnId: s.turnID, Kind: &pb.Event_ScheduledNotification{ScheduledNotification: &pb.ScheduledNotification{TaskId: s.scheduledTask, RunId: s.scheduledRun, Title: in.Title, Text: in.Text}}}
	if err := s.appendLocked([]*pb.Event{e}, "", 0); err != nil {
		return nil, err
	}
	return []byte(`{"notified":true}`), nil
}

func (d *Daemon) scheduledClosures() []*session {
	ended := map[string]bool{}
	rejected := map[string]string{}
	uncertain := map[string]bool{}
	d.scheduleMu.Lock()
	for _, task := range d.tasks {
		if task.Spec.Target.Type != "new_session" {
			continue
		}
		for _, run := range task.Runs {
			if run.Status == "failed" && run.Reason == "relay_session_limit" {
				rejected[run.SessionID] = run.ID
			}
			if run.SessionID != "" {
				uncertain[run.SessionID] = run.Status == "uncertain"
			}
			if run.SessionID != "" && (run.Status == "completed" || run.Status == "failed" || run.Status == "interrupted" || run.Status == "cancelled") {
				ended[run.SessionID] = true
			}
		}
		if task.Active != nil && task.Active.SessionID != "" && task.Active.Status == "uncertain" {
			uncertain[task.Active.SessionID] = true
		}
	}
	d.scheduleMu.Unlock()
	d.mu.Lock()
	sessions := make([]*session, 0, len(d.sessions))
	for _, s := range d.sessions {
		sessions = append(sessions, s)
	}
	d.mu.Unlock()
	out := sessions[:0]
	for _, s := range sessions {
		s.mu.Lock()
		if ended[s.meta.ID] {
			s.scheduledClose = true
		}
		s.scheduledCloseRejected = rejected[s.meta.ID] != "" && rejected[s.meta.ID] == s.meta.ScheduledRunID
		ready := !uncertain[s.meta.ID] && s.scheduledCloseEligibleLocked() && !time.Now().Before(s.scheduledCloseAfter)
		s.mu.Unlock()
		if ready {
			out = append(out, s)
		}
	}
	return out
}

// Rejected new sessions have no published conversation to finish. They use
// the same closure owner, transcript read, takeover check and retry policy.
func (s *session) scheduledCloseEligibleLocked() bool {
	return s.meta.TaskOwned && !s.meta.TaskCLIClosed && !s.meta.Exited && s.scheduledClose &&
		(s.scheduledCloseRejected || !s.running && len(s.queue.Queue) == 0 && len(s.queue.Held) == 0)
}

func (d *Daemon) publishedScheduleRevision() uint64 {
	d.scheduleMu.Lock()
	defer d.scheduleMu.Unlock()
	if !time.Now().Before(d.schedulePublishAt) && d.schedulePublished != d.scheduleRevision {
		d.schedulePublished = d.scheduleRevision
		d.schedulePublishAt = time.Now().Add(2 * time.Second)
	}
	return d.schedulePublished
}

func humanOrigin(origin *pb.Origin) bool {
	return origin.GetKind() == pb.OriginKind_ORIGIN_KIND_CLIENT || origin.GetKind() == pb.OriginKind_ORIGIN_KIND_LOCAL
}

func (s *session) takeOverLocked() {
	s.scheduledTask, s.scheduledRun, s.scheduledNotifications = "", "", 0
	s.claimOwnershipLocked()
}

func (s *session) claimOwnershipLocked() {
	if s.meta.TaskOwned {
		s.meta.TaskOwned = false
		s.noteStorageLocked(s.meta.save(s.dir))
	}
}
