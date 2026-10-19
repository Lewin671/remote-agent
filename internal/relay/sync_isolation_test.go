package relay_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/relay"
	"google.golang.org/protobuf/proto"
)

type failingSyncBackend struct {
	relay.Backend
	stage    string
	attempts atomic.Int32
	broken   atomic.Bool
}

func (b *failingSyncBackend) SessionInfo(id string) (*pb.Session, error) {
	if id == "bad" && b.stage == "metadata" && b.broken.Load() {
		b.attempts.Add(1)
		return nil, errors.New(marker)
	}
	return b.Backend.SessionInfo(id)
}
func (b *failingSyncBackend) EachEvents(id string, after uint64, fn func(*pb.Event) error) (<-chan struct{}, error) {
	if id == "bad" && b.stage == "events" && b.broken.Load() {
		b.attempts.Add(1)
		return nil, errors.New(marker)
	}
	return b.Backend.EachEvents(id, after, fn)
}

func (b *failingSyncBackend) SessionDir(id string) string {
	// A key-read attempt reaches SessionDir immediately before opening the
	// deliberately invalid relay.key. No metadata is published past it.
	if id == "bad" && b.stage == "key" && b.broken.Load() {
		b.attempts.Add(1)
	}
	return b.Backend.SessionDir(id)
}

func machineRequestSession(r *http.Request) string {
	body, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(body))
	var req interface {
		proto.Message
		GetSessionId() string
	}
	if strings.HasSuffix(r.URL.Path, "/PutSession") {
		req = &pb.PutSessionRequest{}
	} else {
		req = &pb.UploadEventsRequest{}
	}
	if proto.Unmarshal(body, req) != nil {
		return ""
	}
	return req.GetSessionId()
}

func TestSessionSyncFailureDoesNotReconnectOrBlockHealthySession(t *testing.T) {
	for _, stage := range []string{"metadata", "events", "key", "PutSession", "UploadEvents"} {
		t.Run(stage, func(t *testing.T) {
			var logins, rejected atomic.Int32
			var broken atomic.Bool
			broken.Store(true)
			e := setupWithHandler(t, func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.HasSuffix(r.URL.Path, "/Login") {
						logins.Add(1)
					}
					if strings.HasSuffix(r.URL.Path, "/"+stage) && machineRequestSession(r) == "bad" && broken.Load() {
						rejected.Add(1)
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusForbidden)
						_, _ = io.WriteString(w, `{"code":"permission_denied","message":"private failure"}`)
						return
					}
					next.ServeHTTP(w, r)
				})
			})
			bad := e.backend.add(t, "bad")
			healthy := e.backend.add(t, "healthy")
			appendText(t, bad, "bad", "recover me")
			appendText(t, healthy, "healthy", "initial")
			if stage == "events" {
				e.backend.mu.Lock()
				e.backend.exited["bad"] = true
				e.backend.mu.Unlock()
			}
			keyPath := filepath.Join(e.backend.SessionDir("bad"), "relay.key")
			if stage == "key" {
				if err := os.Mkdir(keyPath, 0700); err != nil {
					t.Fatal(err)
				}
			}
			backend := &failingSyncBackend{Backend: e.backend, stage: stage}
			backend.broken.Store(true)
			logins.Store(0)
			e.startRelayBackend(backend, func(r *relay.Relay) {
				relay.SetSyncRetryBaseForTest(r, testSyncInterval)
			})
			s := e.session(t, "healthy", 1)
			// Observe three failures, spanning two retry delays, while live events and
			// forwarded commands continue to work on the original connection.
			waitFor(t, "independent retries", func() bool {
				switch stage {
				case "metadata", "events", "key":
					return backend.attempts.Load() >= 3
				default:
					return rejected.Load() >= 3
				}
			})
			if stage == "events" {
				archive := e.backend.add(t, "archive")
				appendText(t, archive, "archive", "ended backlog still syncs")
				e.backend.mu.Lock()
				e.backend.exited["archive"] = true
				e.backend.mu.Unlock()
				e.session(t, "archive", 1)
			}
			appendText(t, healthy, "healthy", "still syncing")
			s = e.session(t, "healthy", 2)
			evs, err := e.client.Events(context.Background(), s, 0)
			if err != nil || len(evs) != 2 || evs[1].GetAgentMessage().Text != "still syncing" {
				t.Fatalf("healthy events: %v %v", evs, err)
			}
			_, err = e.client.Do(context.Background(), s, &pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: "healthy", Text: "command during failure"}}})
			if err != nil || e.backend.sendCount() != 1 {
				t.Fatalf("healthy command: %v", err)
			}
			e.online(t, true)
			if n := logins.Load(); n != 1 {
				t.Fatalf("session failure caused %d logins; want 1", n)
			}
			broken.Store(false)
			backend.broken.Store(false)
			if stage == "key" {
				if err := os.Remove(keyPath); err != nil {
					t.Fatal(err)
				}
			}
			e.session(t, "bad", 1)
			if n := logins.Load(); n != 1 {
				t.Fatalf("recovery caused reconnect: %d logins", n)
			}
		})
	}
}

func TestSessionRPCConnectionFailureStillReconnects(t *testing.T) {
	for _, code := range []string{"unauthenticated", "unavailable", "canceled", "deadline_exceeded"} {
		t.Run(code, func(t *testing.T) {
			var logins, failures atomic.Int32
			e := setupWithHandler(t, func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.HasSuffix(r.URL.Path, "/Login") {
						logins.Add(1)
					}
					if strings.HasSuffix(r.URL.Path, "/UploadEvents") && failures.Add(1) == 1 {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusServiceUnavailable)
						_, _ = io.WriteString(w, `{"code":"`+code+`","message":"injected connection failure"}`)
						return
					}
					next.ServeHTTP(w, r)
				})
			})
			appendText(t, e.backend.add(t, "session"), "session", "retry after reconnect")
			logins.Store(0)
			e.startRelay()
			e.session(t, "session", 1)
			e.online(t, true)
			if n := logins.Load(); n < 2 {
				t.Fatalf("connection error did not reconnect: %d logins", n)
			}
		})
	}
}
