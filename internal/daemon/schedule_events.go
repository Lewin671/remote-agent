package daemon

import (
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/eventlog"
	"github.com/Lewin671/remote-agent/internal/schedule"
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

// admitScheduledContext is the sole request admission boundary for decisions
// based on live scheduled context or its annotations. Never call with scheduleMu
// or mu held: the order is readMu -> scheduleMu (if needed) -> mu.
func (s *session) admitScheduledContext(operation string, decide func() error) error {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	if !s.readRolloutLocked() {
		return connect.NewError(connect.CodeUnavailable, errors.New("could not read and commit current transcript; retry "+operation))
	}
	return decide()
}

// Safety controls refresh when possible, but a read fault must not disable
// interrupting, approving or opening the CLI. readRolloutLocked logs the fault.
func (s *session) withLatestOrCachedContext(operation string, decide func() error) error {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	if !s.readRolloutLocked() {
		slog.Warn("transcript unavailable; using cached state", "operation", operation)
	}
	return decide()
}

// Request callers hold the admission read lock and scheduleMu, never mu.
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
		if run := task.Active; task.ID == s.meta.ScheduledTaskID && run != nil && run.ID == s.meta.ScheduledRunID && run.SessionID == id && !run.MessageSeen && s.scheduledHumanSeq <= run.StartSeq {
			return true
		}
	}
	return false
}

// Caller has passed admitScheduledContext for this bound session.
func (d *Daemon) notifyScheduled(q *pb.ScheduleRequest, s *session) ([]byte, error) {
	var in struct {
		Text  string `json:"text"`
		Title string `json:"title"`
	}
	if len(q.SpecJson) > 4096 || json.Unmarshal(q.SpecJson, &in) != nil || strings.TrimSpace(in.Text) == "" || !utf8.ValidString(in.Text) || !utf8.ValidString(in.Title) || utf8.RuneCountInString(in.Text) > 200 || utf8.RuneCountInString(in.Title) > 60 {
		return nil, errors.New("notify_user requires text (1-200 characters) and optional title (up to 60 characters)")
	}
	// Read the current run's immutable dispatch source in the established
	// scheduleMu -> mu order. A task can be edited while an old turn follows up.
	d.scheduleMu.Lock()
	s.mu.Lock()
	defer s.mu.Unlock()
	var source schedule.Run
	if task := d.tasks[s.scheduledTask]; task != nil {
		if task.Active != nil && task.Active.ID == s.scheduledRun {
			source = *task.Active
		} else {
			for _, run := range task.Runs {
				if run.ID == s.scheduledRun {
					source = run
					break
				}
			}
		}
	}
	d.scheduleMu.Unlock()
	if !s.running || s.meta.Exited || s.scheduledTask == "" || s.scheduledRun == "" {
		return nil, errors.New("notify_user is only available during a scheduled task's turn; ordinary turns already notify on completion")
	}
	if s.scheduledNotifications >= 3 {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("notify_user is limited to 3 notifications per scheduled run"))
	}
	if in.Title == "" {
		if source.ID != s.scheduledRun || source.SessionID != s.meta.ID {
			return nil, errors.New("scheduled execution source is unavailable for the notification title")
		}
		switch source.TargetType {
		case "new_session":
			in.Title = source.TaskTitle
		case "resume_session":
			in.Title = s.infoLocked().Title
		default:
			return nil, errors.New("scheduled execution has no recorded target type for the notification title")
		}
		in.Title = string([]rune(in.Title)[:min(60, utf8.RuneCountInString(in.Title))])
	}
	e := &pb.Event{TurnId: s.turnID, Kind: &pb.Event_ScheduledNotification{ScheduledNotification: &pb.ScheduledNotification{TaskId: s.scheduledTask, RunId: s.scheduledRun, Title: in.Title, Text: in.Text}}}
	if err := s.appendLocked([]*pb.Event{e}, "", 0); err != nil {
		return nil, err
	}
	return []byte(`{"notified":true}`), nil
}

// Creation identity is immutable even when the task changes its target.
type scheduledRunKey struct {
	taskID, runID string
}

func (d *Daemon) scheduledClosures() []*session {
	ended := map[scheduledRunKey]string{}
	rejected := map[scheduledRunKey]string{}
	uncertain := map[scheduledRunKey]string{}
	d.scheduleMu.Lock()
	for _, task := range d.tasks {
		for _, run := range task.Runs {
			key := scheduledRunKey{task.ID, run.ID}
			if run.Status == "failed" && run.Reason == "relay_session_limit" {
				rejected[key] = run.SessionID
			}
			if run.Status == "uncertain" {
				uncertain[key] = run.SessionID
			}
			if run.SessionID != "" && (run.Status == "completed" || run.Status == "failed" || run.Status == "interrupted" || run.Status == "cancelled") {
				ended[key] = run.SessionID
			}
		}
		if task.Active != nil && task.Active.SessionID != "" && task.Active.Status == "uncertain" {
			uncertain[scheduledRunKey{task.ID, task.Active.ID}] = task.Active.SessionID
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
		key := scheduledRunKey{s.meta.ScheduledTaskID, s.meta.ScheduledRunID}
		if ended[key] == s.meta.ID {
			s.scheduledClose = true
		}
		s.scheduledCloseRejected = rejected[key] == s.meta.ID
		ready := uncertain[key] != s.meta.ID && s.scheduledCloseEligibleLocked() && !time.Now().Before(s.scheduledCloseAfter)
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

// Canonical accepted input is the ownership authority, even if its QUEUED
// event could not be appended. This does not clear the current model turn.
func (s *session) claimQueuedHumanOwnershipLocked() {
	if !s.meta.TaskOwned {
		return
	}
	for _, pending := range [][]input{s.queue.Queue, s.queue.Held} {
		for _, in := range pending {
			if humanOrigin(in.Origin.pb()) {
				s.claimOwnershipLocked()
				return
			}
		}
	}
}
