package daemon

import (
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
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
	d.scheduleMu.Lock()
	defer d.scheduleMu.Unlock()
	changed := false
	for _, t := range d.tasks {
		taskChanged := false
		if t.Active != nil && t.Active.SessionID == id {
			d.endScheduledRun(t, time.Now().UTC(), "failed", reason)
			taskChanged = true
		} else {
			for i := range t.Runs {
				r := &t.Runs[i]
				if r.SessionID == id && (r.Status != "failed" || r.Reason != reason) {
					r.Status, r.Reason = "failed", reason
					taskChanged = true
				}
			}
			if taskChanged && t.Status != "cancelled" {
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
	if changed {
		return d.saveSchedulesLocked()
	}
	return nil
}
