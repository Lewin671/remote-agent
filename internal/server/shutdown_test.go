package server

import (
	"context"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/coder/websocket"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func TestCloseCancelsAndWaitsBeforeClosingStorage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ts := newPushAdmissionServer(t)
		s := ts.srv
		s.ctx, s.cancel = context.WithCancel(context.Background())
		s.stop = make(chan struct{})
		s.streams = map[string]map[*streamAuth]struct{}{}
		streamCtx, streamCancel := context.WithCancel(context.Background())
		s.streams["phone"] = map[*streamAuth]struct{}{{cancel: streamCancel}: {}}
		workerCanceled := make(chan struct{})
		releaseWorker := make(chan struct{})
		defer func() {
			select {
			case <-releaseWorker:
			default:
				close(releaseWorker)
			}
		}()
		if !s.startWorker(func() { <-s.ctx.Done(); close(workerCanceled); <-releaseWorker }) {
			t.Fatal("worker rejected")
		}
		requestCanceled := make(chan struct{})
		releaseRequest := make(chan struct{})
		defer func() {
			select {
			case <-releaseRequest:
			default:
				close(releaseRequest)
			}
		}()
		requestDone := make(chan struct{})
		handler := s.trackRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
			close(requestCanceled)
			<-releaseRequest
			if err := s.store.db.Ping(); err != nil {
				t.Error("storage closed before handler completed")
			}
		}))
		go func() {
			defer close(requestDone)
			handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil))
		}()
		synctest.Wait()
		done := make(chan error, 1)
		go func() { done <- s.Close() }()
		synctest.Wait()
		for _, ch := range []<-chan struct{}{workerCanceled, requestCanceled, streamCtx.Done()} {
			select {
			case <-ch:
			default:
				t.Fatal("activity not canceled")
			}
		}
		select {
		case err := <-done:
			t.Fatalf("Close returned before drain: %v", err)
		default:
		}
		if err := s.store.db.Ping(); err != nil {
			t.Fatal("database closed during drain")
		}
		late := httptest.NewRecorder()
		handler.ServeHTTP(late, httptest.NewRequest("GET", "/", nil))
		if late.Code != http.StatusServiceUnavailable {
			t.Fatal("new request admitted during shutdown")
		}
		if s.startWorker(func() {}) {
			t.Fatal("new worker admitted during shutdown")
		}
		close(releaseRequest)
		<-requestDone
		synctest.Wait()
		select {
		case <-done:
			t.Fatal("Close did not wait for background worker")
		default:
		}
		close(releaseWorker)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if err := s.store.db.Ping(); err == nil {
			t.Fatal("database left open")
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestCloseCancelsPushDeliveryAndWaits(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ts := newPushAdmissionServer(t)
		s := ts.srv
		s.ctx, s.cancel = context.WithCancel(context.Background())
		s.stop = make(chan struct{})
		entered := make(chan struct{})
		canceled := make(chan struct{})
		release := make(chan struct{})
		defer func() {
			select {
			case <-release:
			default:
				close(release)
			}
		}()
		s.push.client = &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			close(entered)
			select {
			case <-r.Context().Done():
				close(canceled)
			case <-release:
				return nil, context.Canceled
			}
			<-release
			return nil, r.Context().Err()
		})}
		sub := pushSubscription(t, "https://push.example.com/endpoint")
		if err := s.push.subscribe(context.Background(), ts.tenant(), "phone", sub, []pb.NotifyKind{pb.NotifyKind_NOTIFY_KIND_ATTENTION}); err != nil {
			t.Fatal(err)
		}
		if err := s.push.notify(context.Background(), ts.tenant(), "session", pb.NotifyKind_NOTIFY_KIND_ATTENTION, nil, "key"); err != nil {
			t.Fatal(err)
		}
		<-entered
		done := make(chan error, 1)
		go func() { done <- s.Close() }()
		synctest.Wait()
		select {
		case <-canceled:
		default:
			t.Fatal("push delivery context not canceled")
		}
		select {
		case <-done:
			t.Fatal("Close returned before push delivery finished")
		default:
		}
		close(release)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		if len(s.push.deliveries) != 0 {
			t.Fatal("push delivery reservation leaked")
		}
	})
}

func TestCloseDrainsAuthenticatedAndPendingWebSockets(t *testing.T) {
	ts := newTestServer(t)
	priv, id, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup")
	if err != nil {
		t.Fatal(err)
	}
	stream := ts.stream(t, ts.login(t, priv, id))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pending, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.url, "http")+"/v1/term", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer pending.CloseNow()
	reads := make(chan error, 2)
	for _, conn := range []*websocket.Conn{stream.ws, pending} {
		go func() { _, _, err := conn.Read(ctx); reads <- err }()
	}
	done := make(chan error, 1)
	go func() { done <- ts.srv.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("Close failed to drain WebSockets")
	}
	for range 2 {
		if err := <-reads; err == nil || ctx.Err() != nil {
			t.Fatalf("WebSocket not canceled promptly: %v", err)
		}
	}
	if len(ts.srv.streamDevices()) != 0 || len(ts.srv.pendingStreamHellos) != 0 {
		t.Fatal("stream registrations leaked")
	}
	if ts.srv.hub.queueBytes != 0 {
		t.Fatal("stream queue budget leaked")
	}
}
