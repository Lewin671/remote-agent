package remoteclient

import (
	"connectrpc.com/connect"
	"context"
	"crypto/ed25519"
	"errors"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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
	want := &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_TERMINAL_INPUT, MachineId: machineID, SessionId: "0123456789abcdef0123456789abcdef", RequestId: "fedcba9876543210fedcba9876543210"}
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
			secureHeader, _, err := machine.Open(wire, key, true, uint64(time.Now().UnixMilli()))
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

func syntheticTerminalSecurity(t *testing.T) (*Client, *deviceauth.Endpoint, string) {
	t.Helper()
	_, phoneEd, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, pxpub, err := deviceauth.NewXKey()
	if err != nil {
		t.Fatal(err)
	}
	phone, err := deviceauth.NewMember(phoneEd.Public().(ed25519.PublicKey), pxpub, deviceauth.Client)
	if err != nil {
		t.Fatal(err)
	}
	var recPub [32]byte
	recPub[0] = 1
	secret := make([]byte, 32)
	secret[0] = 1
	var domain deviceauth.Hash
	domain[0] = 1
	now := uint64(time.Now().UnixMilli())
	genesis, err := deviceauth.BuildGenesis("0123456789abcdef0123456789abcdef", domain, phone, recPub, secret, phoneEd, now)
	if err != nil {
		t.Fatal(err)
	}
	pin, _ := genesis.Pin()
	_, med, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	mx, mxpub, err := deviceauth.NewXKey()
	if err != nil {
		t.Fatal(err)
	}
	mm, err := deviceauth.NewMember(med.Public().(ed25519.PublicKey), mxpub, deviceauth.Machine)
	if err != nil {
		t.Fatal(err)
	}
	secret2 := make([]byte, 32)
	secret2[0] = 2
	withMachine, err := deviceauth.BuildPairState(genesis, mm, mxpub, secret, secret2, phoneEd, now+1000)
	if err != nil {
		t.Fatal(err)
	}
	_, ced, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	cx, cxpub, err := deviceauth.NewXKey()
	if err != nil {
		t.Fatal(err)
	}
	cm, err := deviceauth.NewMember(ced.Public().(ed25519.PublicKey), cxpub, deviceauth.Client)
	if err != nil {
		t.Fatal(err)
	}
	secret3 := make([]byte, 32)
	secret3[0] = 3
	withClient, err := deviceauth.BuildPairState(withMachine, cm, cxpub, secret2, secret3, phoneEd, now+2000)
	if err != nil {
		t.Fatal(err)
	}
	states := []deviceauth.State{genesis, withMachine, withClient}
	proofs := []*deviceauth.RecoveryStatement{nil, nil, nil}
	lease, _ := deviceauth.BuildLease(withClient, phoneEd, withClient.Core.IssuedAt)
	machineEndpoint, err := deviceauth.InitEndpoint("", pin, states, proofs, mm.ID, med.Seed(), mx, lease)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { machineEndpoint.Close() })
	clientEndpoint, err := deviceauth.InitEndpoint("", pin, states, proofs, cm.ID, ced.Seed(), cx, lease)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { clientEndpoint.Close() })
	return &Client{DeviceID: cm.ID, endpoint: clientEndpoint}, machineEndpoint, mm.ID
}

// A failed pull must not erase a local clock observation, even across reopen.
func TestSyncPersistsObservedTimeBeforeFailedFetch(t *testing.T) {
	client, _, _ := syntheticTerminalSecurity(t)
	defer client.endpoint.Close() // Close the file before t.TempDir cleanup, including on Windows.
	path := filepath.Join(t.TempDir(), "security.db")
	if err := client.endpoint.Save(path); err != nil {
		t.Fatal(err)
	}
	observed := uint64(time.Now().UnixMilli())
	var atFetch atomic.Uint64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stored, err := deviceauth.OpenEndpoint(path)
		if err != nil {
			t.Error(err)
			http.Error(w, "storage", 500)
			return
		}
		atFetch.Store(stored.Floor())
		stored.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"code":"unavailable","message":"synthetic pull failure"}`))
	}))
	defer srv.Close()
	client.api = remoteagentv1connect.NewClientServiceClient(srv.Client(), srv.URL)
	if err := client.Sync(context.Background()); connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("Sync = %v", err)
	}
	if atFetch.Load() < observed {
		t.Errorf("fetch began before time was persisted: floor=%d observed=%d", atFetch.Load(), observed)
	}
	if client.endpoint.Floor() < observed {
		t.Error("cached time floor did not advance")
	}
	reopened, err := deviceauth.OpenEndpoint(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if reopened.Floor() < observed {
		t.Error("observed time was lost across reopen")
	}
}
