package daemon

import (
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
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

// Events returns the session's events after seq and a channel closed when
// more are appended.
func (d *Daemon) Events(id string, after uint64) ([]*pb.Event, <-chan struct{}, error) {
	s, err := d.exact(id)
	if err != nil {
		return nil, nil, err
	}
	evs, changed := s.log.Since(after)
	return evs, changed, nil
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
