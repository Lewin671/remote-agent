package relay_test

import (
	"context"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

func blockReplacementMain(t *testing.T) (*env, *atomic.Bool) {
	t.Helper()
	blocked := &atomic.Bool{}
	e := setupWithHandler(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if blocked.Load() && r.URL.Path == "/v1/stream" {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	return e, blocked
}

func TestLinkTerminalSurvivesWhileAllReplacementMainPathsFail(t *testing.T) {
	e, blocked := blockReplacementMain(t)
	e.backend.add(t, "link-session")
	e.startRelay()
	s := e.session(t, "link-session", 0)
	e.online(t, true)
	e.streamsMu.Lock()
	var main net.Conn
	for c := range e.streams {
		main = c
	}
	e.streamsMu.Unlock()
	if main == nil {
		t.Fatal("missing main path")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	term, err := e.client.OpenTerminal(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	if _, err := term.Read(ctx); err != nil {
		t.Fatal(err)
	}
	e.backend.mu.Lock()
	native := e.backend.term
	e.backend.mu.Unlock()
	blocked.Store(true)
	if err := main.Close(); err != nil {
		t.Fatal(err)
	}
	e.online(t, false)
	select {
	case <-native.done:
		t.Fatal("main path ended the logical terminal")
	default:
	}
	native.out <- []byte("after main loss")
	frame, err := term.Read(ctx)
	if err != nil || string(frame.GetOutput()) != "after main loss" {
		t.Fatalf("independent output failed: %v", err)
	}
	if err := term.Send(ctx, &pb.TerminalFrame{Kind: &pb.TerminalFrame_Input{Input: []byte("once")}}); err != nil {
		t.Fatal(err)
	}
	select {
	case input := <-native.input:
		if string(input) != "once" {
			t.Fatal("input changed")
		}
	case <-ctx.Done():
		t.Fatal("independent input stopped")
	}
}

func TestLinkPublicationContinuesWhileAllMainPathsFail(t *testing.T) {
	e, blocked := blockReplacementMain(t)
	log := e.backend.add(t, "link-sync")
	e.startRelay()
	e.session(t, "link-sync", 0)
	e.online(t, true)
	blocked.Store(true)
	e.dropStreams()
	e.online(t, false)
	appendText(t, log, "link-sync", "published after main loss")
	s := e.session(t, "link-sync", 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	events, err := e.client.Events(ctx, s, 0)
	if err != nil || len(events) != 1 || events[0].GetAgentMessage().Text != "published after main loss" {
		t.Fatalf("independent publication failed: events=%d err=%v", len(events), err)
	}
}
