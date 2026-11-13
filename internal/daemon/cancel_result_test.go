package daemon

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/relay"
	"github.com/Lewin671/remote-agent/internal/remoteclient"
	"github.com/Lewin671/remote-agent/internal/server"
)

// Cancel the real daemon RPC after the native adapter's first terminal write.
// Only the terminal is simulated; Forward/WS, crypto and storage are real.
type interruptedModelBackend struct {
	*Daemon
	term *fakeTerm
}
type interruptedModelLocal struct {
	remoteagentv1connect.LocalServiceHandler
	term *fakeTerm
}

func (b interruptedModelBackend) Local() remoteagentv1connect.LocalServiceHandler {
	return interruptedModelLocal{b.Daemon.Local(), b.term}
}
func (l interruptedModelLocal) SetModel(ctx context.Context, req *connect.Request[pb.SetModelRequest]) (*connect.Response[pb.SetModelResponse], error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	l.term.mu.Lock()
	original := l.term.onWrite
	l.term.onWrite = func(f *fakeTerm, b string) { original(f, b); cancel() }
	l.term.mu.Unlock()
	return l.LocalServiceHandler.SetModel(ctx, req)
}

func TestCanceledNativeMenuResultStaysUnknown(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	h, _ := controlHarness(t)
	h.d.sessions[h.s.meta.ID] = h.s
	srv, err := server.New(server.Config{DBPath: filepath.Join(t.TempDir(), "relay.db"), SetupToken: "test-setup"})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	http := httptest.NewServer(srv.Handler())
	defer http.Close()
	ctx := context.Background()
	authority, err := remoteclient.NewAuthority(ctx, http.URL, "test-setup")
	if err != nil {
		t.Fatal(err)
	}
	_, ed, _ := ed25519.GenerateKey(nil)
	x, _, _ := deviceauth.NewXKey()
	machine, err := authority.Enroll(ctx, ed, x, deviceauth.Machine)
	if err != nil {
		t.Fatal(err)
	}
	// Save after enrolling the client so the daemon starts on the current head.
	_, phoneEd, _ := ed25519.GenerateKey(nil)
	phoneX, _, _ := deviceauth.NewXKey()
	phone, err := authority.Enroll(ctx, phoneEd, phoneX, deviceauth.Client)
	if err != nil {
		t.Fatal(err)
	}
	client := phone.Client(authority)
	defer client.Close()
	if err := machine.Endpoint.Save(filepath.Join(os.Getenv("REMOTE_AGENT_HOME"), "security-v3.db")); err != nil {
		t.Fatal(err)
	}
	machine.Endpoint.Close()
	acct := &relay.Account{Server: http.URL, Root: authority.Root, DeviceSeed: ed.Seed(), DeviceID: machine.ID, Security: &relay.SecurityConfig{Pin: authority.Pin, XPrivate: x}}
	if err := acct.Save(); err != nil {
		t.Fatal(err)
	}
	run, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	r := relay.New(interruptedModelBackend{h.d, h.term})
	go func() { defer close(done); r.Run(run) }()
	defer func() { stop(); <-done }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		machines, err := client.Machines(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ready := false
		for _, m := range machines {
			ready = ready || m.ID == machine.ID && m.Online
		}
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("test relay did not connect")
		}
		time.Sleep(10 * time.Millisecond)
	}
	env, err := client.Seal(machine.ID, h.s.meta.ID, remoteclient.NewRequestID(), time.Now().Add(time.Minute), &pb.Command{Kind: &pb.Command_SetModel{SetModel: &pb.SetModelRequest{SessionId: h.s.meta.ID, Model: "gpt-6-luna", Effort: "high"}}})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		_, err := client.Forward(ctx, machine.ID, env)
		var ce *remoteclient.CommandError
		if !errors.As(err, &ce) || ce.Code != "unknown" {
			t.Errorf("interrupted native operation: want unknown, got %v", err)
		}
	}
	keys := h.term.written()
	if len(keys) != 1 || keys[0] != "/model" {
		t.Fatalf("native side effect: %q", keys)
	}
}

func (b interruptedModelBackend) Image(string, string) relay.ImageUpload {
	return relay.ImageUpload{Failed: true}
}
func (b interruptedModelBackend) OpenTerminal(string) (relay.Terminal, error) {
	return nil, errors.New("unused test terminal stream")
}
