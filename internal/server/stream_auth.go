package server

import (
	"connectrpc.com/connect"
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

// Hello frames contain only a token and routing identifiers, never an
// envelope. Bound pre-authentication buffers and connections independently
// of the larger authenticated frame limits.
const (
	maxStreamHelloBytes    = 4096
	maxPendingStreamHellos = 16
	maxDeviceStreams       = 16
	maxTotalStreams        = 128
)

// acquireStreamHello limits connections waiting for authentication across
// both WebSocket endpoints. Release after authentication or on any error.
func (s *Server) acquireStreamHello(w http.ResponseWriter) (func(), bool) {
	select {
	case s.pendingStreamHellos <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-s.pendingStreamHellos }) }, true
	default:
		w.Header().Set("Retry-After", "1")
		http.Error(w, "too many pending stream authentications", http.StatusServiceUnavailable)
		return nil, false
	}
}

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
	if len(s.streams[d.id]) >= maxDeviceStreams {
		return nil, nil, nil, connect.NewError(connect.CodeResourceExhausted, errors.New("too many device streams"))
	}
	total := 0
	for _, streams := range s.streams {
		total += len(streams)
	}
	if total >= maxTotalStreams {
		return nil, nil, nil, connect.NewError(connect.CodeResourceExhausted, errors.New("too many streams"))
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
