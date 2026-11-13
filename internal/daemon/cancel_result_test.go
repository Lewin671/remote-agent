package daemon

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"io"
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
	client, machineID := startCancellationTestRelay(t, interruptedModelBackend{h.d, h.term})
	env, err := client.Seal(machineID, h.s.meta.ID, remoteclient.NewRequestID(), time.Now().Add(time.Minute), &pb.Command{Kind: &pb.Command_SetModel{SetModel: &pb.SetModelRequest{SessionId: h.s.meta.ID, Model: "gpt-6-luna", Effort: "high"}}})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		_, err := client.Forward(context.Background(), machineID, env)
		var ce *remoteclient.CommandError
		if !errors.As(err, &ce) || ce.Code != "unknown_outcome" {
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

func startCancellationTestRelay(t *testing.T, backend relay.Backend, configure ...func(*httptest.Server)) (*remoteclient.Client, string) {
	t.Helper()
	srv, err := server.New(server.Config{DBPath: filepath.Join(t.TempDir(), "relay.db"), SetupToken: "test-setup"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	http := httptest.NewUnstartedServer(srv.Handler())
	for _, fn := range configure {
		fn(http)
	}
	http.Start()
	t.Cleanup(http.Close)
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
	t.Cleanup(func() { client.Close() })
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
	r := relay.New(backend)
	go func() { defer close(done); r.Run(run) }()
	t.Cleanup(func() { stop(); <-done })
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
	return client, machine.ID
}

func TestFileGenerationConflictIsAuthenticatedAndFinal(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	h, _ := controlHarness(t)
	h.d.uploads = newUploads(t.TempDir())
	u := h.d.uploads
	client, machine := startCancellationTestRelay(t, interruptedModelBackend{Daemon: h.d})
	data := []byte("bytes")
	sum := sha256.Sum256(data)
	begin := &pb.BeginImageUploadRequest{TransferId: tid, FileName: "conflict.bin", TotalBytes: int64(len(data)), Sha256: sum[:]}
	ctx := context.Background()
	do := func(kind *pb.Command) {
		t.Helper()
		if _, err := client.DoMachine(ctx, machine, kind); err != nil {
			t.Fatal(err)
		}
	}
	do(&pb.Command{Kind: &pb.Command_BeginImageUpload{BeginImageUpload: begin}})
	do(&pb.Command{Kind: &pb.Command_PutImageChunk{PutImageChunk: &pb.PutImageChunkRequest{TransferId: tid, Data: data}}})
	hashes := 0
	u.hashFile = func(w io.Writer, r io.Reader) (int64, error) {
		hashes++
		n, err := io.Copy(w, r)
		// Complete has dropped the lock. Exercise the real abort/re-begin RPCs
		// from another request while its old descriptor remains open.
		do(&pb.Command{Kind: &pb.Command_AbortImageUpload{AbortImageUpload: &pb.AbortImageUploadRequest{TransferId: tid}}})
		do(&pb.Command{Kind: &pb.Command_BeginImageUpload{BeginImageUpload: begin}})
		return n, err
	}
	env, err := client.Seal(machine, "", remoteclient.NewRequestID(), time.Now().Add(time.Minute), &pb.Command{Kind: &pb.Command_CompleteImageUpload{CompleteImageUpload: &pb.CompleteImageUploadRequest{TransferId: tid}}})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		_, err = client.Forward(ctx, machine, env)
		var ce *remoteclient.CommandError
		if !errors.As(err, &ce) || ce.Code != "generation_conflict" || !ce.FromDaemon || remoteclient.Uncertain(err) {
			t.Fatalf("want signed final generation conflict: %v", err)
		}
	}
	if hashes != 1 {
		t.Fatalf("completion retried: %d", hashes)
	}
}
