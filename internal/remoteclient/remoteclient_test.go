package remoteclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"
)

// wsServer serves one WebSocket per request with handle.
func wsServer(t *testing.T, handle func(ctx context.Context, ws *websocket.Conn)) *httptest.Server {
	t.Helper()
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer ws.CloseNow()
		ws.SetReadLimit(4 << 20)
		handle(r.Context(), ws)
	}))
	t.Cleanup(hs.Close)
	return hs
}

func TestStreamFailsOnGarbledFirstFrame(t *testing.T) {
	hs := wsServer(t, func(ctx context.Context, ws *websocket.Conn) {
		if _, _, err := ws.Read(ctx); err != nil {
			return
		}
		ws.Write(ctx, websocket.MessageBinary, []byte{0xff})
		ws.Read(ctx) // hold the connection open until the client leaves
	})
	c := &Client{Server: hs.URL, token: "t"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := c.Stream(ctx)
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stream = %v, want a frame error before the deadline", err)
	}
}

func TestStreamGivesUpWhenContextEnds(t *testing.T) {
	hs := wsServer(t, func(ctx context.Context, ws *websocket.Conn) {
		ws.Read(ctx) // read the hello and never answer
		ws.Read(ctx)
	})
	c := &Client{Server: hs.URL, token: "t"}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := c.Stream(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Stream = %v, want the deadline", err)
	}
}

func TestTermSendKeepsSeqOrderAcrossGoroutines(t *testing.T) {
	const senders, each = 8, 50
	key := e2e.NewKey()
	want := &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_TERMINAL_INPUT, MachineId: "m", SessionId: "s", RequestId: "stream"}
	got := make(chan error, 1)
	hs := wsServer(t, func(ctx context.Context, ws *websocket.Conn) {
		var last uint64
		for i := 0; i < senders*each; i++ {
			_, b, err := ws.Read(ctx)
			if err != nil {
				got <- err
				return
			}
			env := &pb.Envelope{}
			if err := proto.Unmarshal(b, env); err != nil {
				got <- err
				return
			}
			h, err := e2e.OpenMessage(key, env, want, &pb.TerminalFrame{})
			if err != nil {
				got <- err
				return
			}
			if h.Seq != last+1 {
				got <- errors.New("frame out of order")
				return
			}
			last = h.Seq
		}
		got <- nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(hs.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	term := &Term{c: &Client{DeviceID: "d"}, s: Session{MachineID: "m", ID: "s", Key: key}, ws: ws, stream: "stream"}
	var wg sync.WaitGroup
	for range senders {
		wg.Go(func() {
			for range each {
				if err := term.Send(ctx, &pb.TerminalFrame{}); err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()
	if err := <-got; err != nil {
		t.Fatal(err)
	}
}
