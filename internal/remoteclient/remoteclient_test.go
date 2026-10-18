package remoteclient

import (
	"context"
	"crypto/ed25519"
	"errors"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
	client, machine, machineID := syntheticTerminalSecurity(t)
	want := &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_TERMINAL_INPUT, MachineId: machineID, SessionId: "synthetic-session", RequestId: "synthetic-terminal-stream"}
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
			wire, err := deviceauth.FromEnvelope(env)
			if err != nil {
				got <- err
				return
			}
			secureHeader, _, err := machine.OpenCurrent(ctx, wire, key)
			h := secureHeader.Projection()
			if err == nil {
				err = e2e.Expect(h, want)
			}
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
	term := &Term{c: client, s: Session{MachineID: machineID, ID: want.SessionId, Key: key}, ws: ws, stream: want.RequestId}
	var wg sync.WaitGroup
	for range senders {
		wg.Go(func() {
			for range each {
				if err := term.Send(ctx, &pb.TerminalFrame{Kind: &pb.TerminalFrame_Input{Input: []byte("x")}}); err != nil {
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

func syntheticTerminalSecurity(t *testing.T) (*Client, *deviceauth.Remote, string) {
	t.Helper()
	pub, ed, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	x, xp, err := deviceauth.NewXKey()
	if err != nil {
		t.Fatal(err)
	}
	member, err := deviceauth.NewMember(pub, xp, deviceauth.Machine)
	if err != nil {
		t.Fatal(err)
	}
	keys, err := e2e.Derive(e2e.NewKey())
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	a, err := deviceauth.InitAuthority(filepath.Join(dir, "authority.db"), e2e.AccountID(keys.AccountPublicKey()), member)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	pin, state := a.Snapshot()
	endpoint, err := deviceauth.InitEndpoint(filepath.Join(dir, "machine.db"), pin, state, member.ID, ed.Seed(), x)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { endpoint.Close() })
	cp, ce, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	cx, cxp, err := deviceauth.NewXKey()
	if err != nil {
		t.Fatal(err)
	}
	cm, err := deviceauth.NewMember(cp, cxp, deviceauth.Client)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := a.BeginPair("synthetic-terminal-pair", member.ID, cm)
	if err != nil {
		t.Fatal(err)
	}
	approve, err := deviceauth.SignPair(pair, ed, false)
	if err != nil {
		t.Fatal(err)
	}
	accept, err := deviceauth.SignPair(pair, ce, true)
	if err != nil {
		t.Fatal(err)
	}
	state, err = a.ApprovePair(pair.ID, approve, accept)
	if err != nil {
		t.Fatal(err)
	}
	clientEndpoint, err := deviceauth.InitEndpoint(filepath.Join(dir, "client.db"), pin, state, cm.ID, ce.Seed(), cx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { clientEndpoint.Close() })
	exchange := func(_ context.Context, c deviceauth.Capsule) (deviceauth.Capsule, error) { return a.Handle(c) }
	return &Client{DeviceID: cm.ID, security: &deviceauth.Remote{Endpoint: clientEndpoint, Exchange: exchange}}, &deviceauth.Remote{Endpoint: endpoint, Exchange: exchange}, member.ID
}
