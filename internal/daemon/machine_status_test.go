package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/Lewin671/remote-agent/internal/schedule"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

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
	d.diskVolume = func() (*pb.DiskStatus, error) { return &pb.DiskStatus{AvailableBytes: 1}, nil }
	if !d.capabilities().MachineStatus {
		t.Fatal("missing capability")
	}
}
func TestDiskFullMappingHidesRawCause(t *testing.T) {
	d := New(t.TempDir(), Config{})
	d.diskVolume = func() (*pb.DiskStatus, error) { return &pb.DiskStatus{AvailableBytes: 1}, nil }
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
	d.diskVolume = func() (*pb.DiskStatus, error) { return &pb.DiskStatus{AvailableBytes: 1}, nil }
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
	d.diskVolume = func() (*pb.DiskStatus, error) { return &pb.DiskStatus{AvailableBytes: 1}, nil }
	var counter uint64
	d.statusCollector = machinestatus.NewReader(d.dir, nil, func(context.Context) (machinestatus.Raw, error) {
		counter++
		return machinestatus.Raw{Total: counter, Status: &pb.MachineStatus{}}, nil
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
	d.diskVolume = func() (*pb.DiskStatus, error) { return &pb.DiskStatus{AvailableBytes: 1}, nil }
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
	h.d.diskVolume = func() (*pb.DiskStatus, error) { return &pb.DiskStatus{AvailableBytes: 1}, nil }
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
	h.d.diskVolume = func() (*pb.DiskStatus, error) { return &pb.DiskStatus{AvailableBytes: 1}, nil }
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

func TestPausedCleanupStillReclaimsPendingDisplays(t *testing.T) {
	d := New(t.TempDir(), Config{})
	st := openImageStore(d.sessionDir("fixture"))
	d.sessions["fixture"] = &session{images: st}
	f := imageFound(t, 130, 80)
	st.process(f, "")
	id := f.Ref.ImageId
	if id == "" || st.byID[id].State != imagePending {
		t.Fatal("missing pending image")
	}
	st.paused = func() bool { return true }
	if n, e := d.trimImageCache(0); e != nil || n != 0 {
		t.Fatal("cleanup failed while deferred", n, e)
	}
	for _, path := range []string{st.displayPath(id), st.previewPath(id)} {
		if _, e := os.Stat(path); !os.IsNotExist(e) {
			t.Fatal("blob not reclaimed", e)
		}
	}
	reloaded := openImageStore(d.sessionDir("fixture"))
	if reloaded.byID[id].State != imageFailed {
		t.Fatal("cleanup index not persisted")
	}
}
func TestHealthyVolumeErrorsUseStorageExhaustedWithoutReserveRelease(t *testing.T) {
	d := New(t.TempDir(), Config{})
	d.diskVolume = func() (*pb.DiskStatus, error) { return &pb.DiskStatus{AvailableBytes: 50e9}, nil }
	for _, cause := range []error{syscall.ENOSPC, syscall.EDQUOT} {
		e := d.rpcErr(&os.PathError{Op: "write", Path: "fixture-other-target", Err: cause})
		if protocol.MachineErrorCode(e) != protocol.StorageExhausted || strings.Contains(e.Error(), "fixture-other-target") {
			t.Fatal(e)
		}
	}
	if d.diskGuard.Paused() || len(d.diskGuard.Alerts()) != 0 {
		t.Fatal("unrelated volume triggered emergency")
	}
}
func TestScheduleDiskFullMapping(t *testing.T) {
	h := scheduleHarness(t)
	h.d.diskVolume = func() (*pb.DiskStatus, error) { return &pb.DiskStatus{AvailableBytes: 1}, nil }
	h.d.syncStateDir = func(string) error { return syscall.ENOSPC }
	at := time.Now().Add(time.Hour)
	spec, _ := json.Marshal(taskSpec(schedule.Trigger{Type: "at", At: &at}))
	_, e := (&handler{d: h.d}).Schedule(context.Background(), connect.NewRequest(&pb.ScheduleRequest{Operation: "create", SessionId: h.s.meta.ID, RequestId: "fixture-disk-full", SpecJson: spec}))
	if protocol.MachineErrorCode(e) != protocol.DiskFull {
		t.Fatal(e)
	}
}
func TestStorageErrorLogsAreRateLimited(t *testing.T) {
	d := New(t.TempDir(), Config{})
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(old)
	for i := 0; i < 20; i++ {
		d.logStorageFailure("fixture", syscall.ENOSPC)
	}
	if strings.Count(buf.String(), "machine write failed") != 1 {
		t.Fatal("unbounded storage logs")
	}
	d.diskLogAt["fixture"] = time.Now().Add(-time.Minute)
	d.logStorageFailure("fixture", syscall.ENOSPC)
	if strings.Count(buf.String(), "machine write failed") != 2 {
		t.Fatal("suppression never rearmed")
	}
}
