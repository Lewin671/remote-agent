package netpolicy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestRedirectCannotReceiveCredentials(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var received atomic.Bool
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Store(true) }))
			defer target.Close()
			source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, status) }))
			defer source.Close()
			client := Client(time.Second)
			client.Transport = source.Client().Transport
			req, err := http.NewRequest(http.MethodPost, source.URL, strings.NewReader("setup-token-and-login-signature"))
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer test-token")
			res, err := client.Do(req)
			if res != nil {
				res.Body.Close()
			}
			if err == nil || received.Load() {
				t.Fatalf("redirect followed: error=%v received=%v", err, received.Load())
			}
		})
	}
}

func TestWebSocketRedirectRejected(t *testing.T) {
	var received atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { received.Store(true) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(source.URL, "http"), &websocket.DialOptions{HTTPClient: Client(time.Second)})
	if ws != nil {
		ws.CloseNow()
	}
	if err == nil || received.Load() {
		t.Fatalf("redirect followed: error=%v received=%v", err, received.Load())
	}
}
