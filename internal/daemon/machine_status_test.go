package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/codex"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/machinestatus"
	"github.com/Lewin671/remote-agent/internal/protocol"
)

func TestMachineStatusReadOnlyClassification(t *testing.T) {
	cmd := &pb.Command{Kind: &pb.Command_GetMachineStatus{GetMachineStatus: &pb.GetMachineStatusRequest{}}}
	if !deviceauth.IsReadOnlyCommand(cmd) {
		t.Fatal("would write acceptance record")
	}
	d := New(t.TempDir(), Config{})
	if !d.capabilities().MachineStatus {
		t.Fatal("missing capability")
	}
}
func TestDiskFullMappingHidesRawCause(t *testing.T) {
	d := New(t.TempDir(), Config{})
	raw := &os.PathError{Op: "mkdir", Path: "/private/fixture", Err: syscall.ENOSPC}
	for _, mapError := range []func(error) error{d.rpcErr, d.uploadErr} {
		e := mapError(raw)
		if protocol.MachineErrorCode(e) != protocol.DiskFull || connect.CodeOf(e) != connect.CodeResourceExhausted || strings.Contains(e.Error(), "private") {
			t.Fatalf("unsafe mapping: %v", e)
		}
	}
	if !d.diskGuard.Paused() {
		t.Fatal("reserve writes not paused")
	}
	if d.diskError(errors.New("other")) != nil {
		t.Fatal("non-space mapped")
	}
}
func TestUploadDiskFullIsStructured(t *testing.T) {
	d := New(t.TempDir(), Config{})
	d.uploads.beforeUploadStep = func(string) error { return syscall.ENOSPC }
	ctx := context.Background()
	ctx = agent.WithOrigin(ctx, &pb.Origin{Kind: pb.OriginKind_ORIGIN_KIND_CLIENT, DeviceId: "fixture"})
	_, e := (&handler{d: d}).BeginImageUpload(ctx, connect.NewRequest(&pb.BeginImageUploadRequest{TransferId: strings.Repeat("a", 32), FileName: "fixture.txt", TotalBytes: 1, Sha256: make([]byte, 32)}))
	if protocol.MachineErrorCode(e) != protocol.DiskFull {
		t.Fatal(e)
	}
}
func TestStatusSnapshotDoesNotWrite(t *testing.T) {
	d := New(t.TempDir(), Config{})
	d.statusCollector = machinestatus.NewReader(d.dir, nil, func(context.Context) (machinestatus.Raw, error) {
		return machinestatus.Raw{Total: 1, Status: &pb.MachineStatus{}}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.statusCollector.Run(ctx)
	before, _ := os.ReadDir(d.dir)
	if _, e := (&handler{d: d}).GetMachineStatus(ctx, connect.NewRequest(&pb.GetMachineStatusRequest{})); e != nil {
		t.Fatal(e)
	}
	after, _ := os.ReadDir(d.dir)
	if len(before) != len(after) {
		t.Fatal("snapshot wrote files")
	}
}

func TestCreateDiskFullIsStructured(t *testing.T) {
	d := New(t.TempDir(), Config{Agents: map[pb.Agent]AgentConfig{pb.Agent_AGENT_CODEX: {Cmd: "/usr/bin/true"}}})
	var e error
	// Fault before a CLI starts: no real subprocess, no disk exhaustion.
	d.agents[pb.Agent_AGENT_CODEX] = codex.New("/usr/bin/true", "", "")
	d.uploads.beforeUploadStep = func(step string) error {
		if step == "create-cli" {
			return syscall.ENOSPC
		}
		return nil
	}
	_, e = (&handler{d: d}).CreateSession(context.Background(), connect.NewRequest(&pb.CreateSessionRequest{Cwd: t.TempDir(), Env: map[string]string{"PATH": "/usr/bin:/bin"}}))
	if protocol.MachineErrorCode(e) != protocol.DiskFull {
		t.Fatal(e)
	}
}
func TestSendDiskFullIsStructured(t *testing.T) {
	h := newHarness(t, Config{})
	h.d.sessions[h.s.meta.ID] = h.s
	h.d.uploads.beforeUploadStep = func(step string) error {
		if step == "queue-commit" {
			return syscall.ENOSPC
		}
		return nil
	}
	_, e := (&handler{d: h.d}).Send(context.Background(), connect.NewRequest(&pb.SendRequest{SessionId: h.s.meta.ID, Text: "fixture", RequestId: "fixture-request"}))
	if protocol.MachineErrorCode(e) != protocol.DiskFull {
		t.Fatal(e)
	}
}

func TestAcceptedSendDiskFullKeepsUncertainOutcome(t *testing.T) {
	h := newHarness(t, Config{})
	h.d.sessions[h.s.meta.ID] = h.s
	h.d.syncStateDir = func(string) error { return syscall.ENOSPC }
	_, e := (&handler{d: h.d}).Send(context.Background(), connect.NewRequest(&pb.SendRequest{SessionId: h.s.meta.ID, Text: "fixture", RequestId: "fixture-request"}))
	if connect.CodeOf(e) != connect.CodeAborted || protocol.MachineErrorCode(e) != protocol.DiskFull {
		t.Fatal(e)
	}
}

func TestDeferredImageWritesPauseImmediately(t *testing.T) {
	dir := t.TempDir()
	st := openImageStore(dir)
	st.paused = func() bool { return true }
	path := filepath.Join(dir, "fixture")
	if e := st.write(path, []byte("cache")); !errors.Is(e, errDeferredImageWrite) {
		t.Fatal(e)
	}
	if _, e := os.Stat(path); !os.IsNotExist(e) {
		t.Fatal("paused cache wrote")
	}
}
