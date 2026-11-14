package transport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
)

type stubPath struct{ id string }

func (s stubPath) Read(context.Context) ([]byte, error) { return []byte(s.id), nil }
func (s stubPath) Write(context.Context, []byte) error  { return nil }
func (s stubPath) Finish()                              {}
func (s stubPath) Close()                               {}

func TestRouterDetachDoesNotClearNewerGeneration(t *testing.T) {
	var r Router
	if _, err := r.OpenStream(context.Background(), StreamRoute{ID: "s1"}); err == nil {
		t.Fatal("OpenStream succeeded before Attach")
	}
	detach1 := r.Attach(func(context.Context, StreamRoute) (Path, error) { return stubPath{id: "gen1"}, nil })
	detach2 := r.Attach(func(context.Context, StreamRoute) (Path, error) { return stubPath{id: "gen2"}, nil })

	// Late cleanup of generation 1 must leave generation 2 intact.
	detach1()
	p, err := r.OpenStream(context.Background(), StreamRoute{ID: "s1"})
	if err != nil {
		t.Fatalf("OpenStream after stale detach: %v", err)
	}
	b, _ := p.Read(context.Background())
	if string(b) != "gen2" {
		t.Fatalf("got %q want gen2", b)
	}

	detach2()
	if _, err := r.OpenStream(context.Background(), StreamRoute{ID: "s1"}); err == nil {
		t.Fatal("OpenStream succeeded after active detach")
	}
}

func TestRelayPathReadRejectsNonBinaryAndCloseEndsLeg(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		_ = ws.Write(r.Context(), websocket.MessageBinary, []byte("ok"))
		_ = ws.Write(r.Context(), websocket.MessageText, []byte("bad"))
		_, _, _ = ws.Read(r.Context())
	}))
	defer srv.Close()

	ws, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	path := NewRelayPath(ws)
	defer path.Close()

	got, err := path.Read(context.Background())
	if err != nil || string(got) != "ok" {
		t.Fatalf("binary Read: got=%q err=%v", got, err)
	}
	if _, err := path.Read(context.Background()); err == nil {
		t.Fatal("expected non-binary frame error")
	}
}
