package server

import (
	"context"
	"time"
)

// streamAuth tracks both main and terminal connections for revocation.
type streamAuth struct{ cancel context.CancelFunc }

// Registration and revocation share a lock so a hello racing device deletion
// cannot leave an authenticated stream alive after revocation has finished.
func (s *Server) beginStream(parent context.Context, token string) (context.Context, *device, context.CancelFunc, error) {
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	d, err := s.authenticate(parent, token)
	if err != nil {
		return nil, nil, nil, err
	}
	ctx, cancel := context.WithDeadline(parent, time.UnixMilli(d.expiresMs))
	a := &streamAuth{cancel: cancel}
	if s.streams[d.id] == nil {
		s.streams[d.id] = map[*streamAuth]struct{}{}
	}
	s.streams[d.id][a] = struct{}{}
	release := func() {
		cancel()
		s.streamMu.Lock()
		defer s.streamMu.Unlock()
		delete(s.streams[d.id], a)
		if len(s.streams[d.id]) == 0 {
			delete(s.streams, d.id)
		}
	}
	return ctx, d, release, nil
}

func (s *Server) revokeStreams(deviceID string) {
	s.streamMu.Lock()
	defer s.streamMu.Unlock()
	for a := range s.streams[deviceID] {
		a.cancel()
	}
	delete(s.streams, deviceID)
}
