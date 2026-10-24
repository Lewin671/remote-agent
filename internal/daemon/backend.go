package daemon

import (
	"errors"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/schedule"
	"time"
)

// The methods below let the relay reach sessions; see relay.Backend.

// SessionIDs lists all sessions, including exited ones.
func (d *Daemon) SessionIDs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	ids := make([]string, 0, len(d.sessions))
	for id := range d.sessions {
		ids = append(ids, id)
	}
	return ids
}

// SessionInfo describes the session with the given full ID.
func (d *Daemon) SessionInfo(id string) (*pb.Session, error) {
	s, err := d.exact(id)
	if err != nil {
		return nil, err
	}
	return s.info(), nil
}

// EachEvents streams the session's events after seq from one stable
// snapshot and returns the channel closed when a later append occurs.
func (d *Daemon) EachEvents(id string, after uint64, fn func(*pb.Event) error) (<-chan struct{}, error) {
	s, err := d.exact(id)
	if err != nil {
		return nil, err
	}
	return s.log.EachSince(after, fn)
}

// SessionDir is the session's state directory.
func (d *Daemon) SessionDir(id string) string {
	s, err := d.exact(id)
	if err != nil {
		return ""
	}
	return s.dir
}

// Local returns the handler of the local API.
func (d *Daemon) Local() remoteagentv1connect.LocalServiceHandler { return &handler{d: d} }

func (d *Daemon) exact(id string) (*session, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	s, ok := d.sessions[id]
	if !ok {
		return nil, errNotFound
	}
	return s, nil
}

// Image returns what the relay must do about an image's display copy.
func (d *Daemon) Image(sessionID, imageID string) ImageToUpload {
	s, err := d.exact(sessionID)
	if err != nil {
		return ImageToUpload{Failed: true}
	}
	return s.images.upload(imageID)
}

// ImageUploaded records whether the relay stored an image's display copy.
func (d *Daemon) ImageUploaded(sessionID, imageID string, ok bool, expiresMs int64) {
	if s, err := d.exact(sessionID); err == nil {
		s.images.uploaded(imageID, ok, expiresMs)
	}
}

// SessionPublicationFailed translates an unpublishable scheduled conversation
// into a durable execution failure. The relay invokes it outside scheduling locks.
func (d *Daemon) SessionPublicationFailed(id, reason string) error {
	if reason != "relay_session_limit" {
		return nil
	}
	s, err := d.exact(id)
	if err != nil {
		return err
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.mu.Lock()
	taskID, runID := s.meta.ScheduledTaskID, s.meta.ScheduledRunID
	s.mu.Unlock()
	d.scheduleMu.Lock()
	var rejected []schedule.Run
	changed := false
	matched := false
	for _, t := range d.tasks {
		taskChanged := false
		if t.Active != nil && (t.Active.SessionID == id || t.Active.SessionID == "" &&
			(t.ID == taskID && t.Active.ID == runID || t.Spec.Target.Type == "resume_session" && t.Spec.Target.Session == id)) {
			t.Active.SessionID = id
			rejected = append(rejected, *t.Active)
			matched = true
			d.endScheduledRun(t, time.Now().UTC(), "failed", reason)
			taskChanged = true
		} else {
			automaticChanged := false
			for i := range t.Runs {
				r := &t.Runs[i]
				if r.SessionID == id {
					matched = true
					rejected = append(rejected, *r)
				}
				if r.SessionID == id && (r.Status != "failed" || r.Reason != reason) {
					r.Status, r.Reason = "failed", reason
					automaticChanged = automaticChanged || !r.Manual
					taskChanged = true
				}
			}
			if automaticChanged && t.Status != "cancelled" {
				t.Status, t.Reason, t.NextRunAt = "failed", reason, nil
				if t.Active != nil {
					t.Status = "paused"
				}
			}
		}
		if taskChanged {
			touchTask(t, time.Now().UTC())
			changed = true
		}
	}
	var saveErr error
	if changed || matched {
		saveErr = d.saveSchedulesLocked()
	}
	d.scheduleMu.Unlock()
	for _, run := range rejected {
		saveErr = errors.Join(saveErr, s.stopUnpublishedScheduledRun(run))
	}
	return saveErr
}

// Retry uncertain cancellation/delivery and interrupt failures from durable
// failed runs, including after a daemon restart. Never interrupt a human turn.
func (d *Daemon) stopUnpublishedScheduledRuns() {
	d.scheduleMu.Lock()
	var runs []schedule.Run
	for _, task := range d.tasks {
		for _, run := range task.Runs {
			if run.Reason == "relay_session_limit" {
				runs = append(runs, run)
			}
		}
	}
	d.scheduleMu.Unlock()
	for _, run := range runs {
		if s, err := d.exact(run.SessionID); err == nil {
			_ = s.stopUnpublishedScheduledRun(run)
		}
	}
}

func (s *session) stopUnpublishedScheduledRun(run schedule.Run) error {
	_ = s.cancelInputBy(run.RequestID, originSystem)
	_, err := s.interruptRunBy(originSystem, run.ID)
	if errors.Is(err, errExited) {
		return nil
	}
	return err
}
