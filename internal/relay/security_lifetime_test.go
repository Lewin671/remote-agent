package relay_test

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/Lewin671/remote-agent/internal/relay"
)

func TestSecurityRenewsWithoutMainWSAndDuringSlowPublication(t *testing.T) {
	var blockWS, expire atomic.Bool
	var renewed atomic.Int32
	var mu sync.Mutex
	firstToken := ""
	publicationStarted, releasePublication := make(chan struct{}), make(chan struct{})
	var once sync.Once
	// The server can answer signed membership pulls while a genuine PutMachine
	// request and all replacement stream attempts are deliberately held/failing.
	e := setupWithHandler(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/stream" && blockWS.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			if strings.HasSuffix(r.URL.Path, "/PutMachine") {
				once.Do(func() { close(publicationStarted) })
				select {
				case <-releasePublication:
				case <-r.Context().Done():
					return
				}
			}
			if strings.HasSuffix(r.URL.Path, "/GetSecurityStates") {
				token := r.Header.Get("Authorization")
				mu.Lock()
				if firstToken == "" {
					firstToken = token
				}
				stale := token == firstToken
				mu.Unlock()
				if expire.Load() && stale {
					connect.NewErrorWriter().Write(w, r, connect.NewError(connect.CodeUnauthenticated, errors.New("test token expired")))
					return
				}
				if expire.Load() && !stale {
					renewed.Add(1)
				}
			}
			next.ServeHTTP(w, r)
		})
	})
	var release sync.Once
	defer release.Do(func() { close(releasePublication) })
	e.startRelayBackend(e.backend, func(r *relay.Relay) { relay.SetSecurityIntervalForTest(r, 20*time.Millisecond) })
	select {
	case <-publicationStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("publication never started")
	}
	e.online(t, true)
	waitFor(t, "initial independent membership pull", func() bool { mu.Lock(); defer mu.Unlock(); return firstToken != "" })
	blockWS.Store(true)
	expire.Store(true)
	e.dropStreams()
	e.online(t, false)
	waitFor(t, "independent token renewal while WS is down and publication held", func() bool { return renewed.Load() >= 2 })
	release.Do(func() { close(releasePublication) })
	// All request bodies remain encrypted in the ordinary server implementation.
}
