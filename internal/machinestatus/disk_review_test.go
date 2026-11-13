package machinestatus

import (
	"bytes"
	"context"
	"errors"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"testing/synctest"
	"time"
)

type blockingDisk struct {
	mu              sync.Mutex
	entered, finish chan struct{}
	size            uint64
	err             error
	calls           int
}

func (f *blockingDisk) Size() uint64  { f.mu.Lock(); defer f.mu.Unlock(); return f.size }
func (f *blockingDisk) Remove() error { f.mu.Lock(); f.size = 0; f.mu.Unlock(); return nil }
func (f *blockingDisk) Reserve(ctx context.Context, n uint64) error {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	close(f.entered)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-f.finish:
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.size = n
	return nil
}
func TestGuardSlowReserveDoesNotBlockReadersOrRelease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fs := &blockingDisk{entered: make(chan struct{}), finish: make(chan struct{})}
		g := NewDiskGuardFS(fs, DefaultThresholds())
		done := make(chan error, 1)
		go func() { done <- g.Check(&pb.DiskStatus{AvailableBytes: 20e9}) }()
		<-fs.entered
		if g.Snapshot() == nil || g.Paused() || g.ReserveSize() != 0 || len(g.Alerts()) != 0 {
			t.Fatal("bad in-flight state")
		}
		if e := g.WriteFailedAt(syscall.ENOSPC, &pb.DiskStatus{AvailableBytes: 1}); e != nil {
			t.Fatal(e)
		}
		if !g.Paused() || len(g.Alerts()) != 1 {
			t.Fatal("critical release blocked")
		}
		if e := <-done; e != nil {
			t.Fatal(e)
		}
		if g.ReserveSize() != 0 {
			t.Fatal("cancelled reserve published")
		}
	})
}
func TestHealthyStateVolumeDoesNotReleaseOrAlert(t *testing.T) {
	fs := &fakeDisk{size: ReserveBytes}
	g := NewDiskGuardFS(fs, DefaultThresholds())
	for _, err := range []error{syscall.ENOSPC, syscall.EDQUOT} {
		if e := g.WriteFailedAt(err, &pb.DiskStatus{AvailableBytes: 50e9}); e != nil {
			t.Fatal(e)
		}
	}
	if fs.removes != 0 || g.Paused() || len(g.Alerts()) != 0 {
		t.Fatal("other destination depleted state reserve")
	}
}
func TestReserveFailureBackoffAndIndependentRecovery(t *testing.T) {
	for _, fault := range []error{syscall.EDQUOT, syscall.ENOSPC, os.ErrPermission, syscall.EROFS} {
		t.Run(fault.Error(), func(t *testing.T) {
			now := time.Unix(1000, 0)
			fs := &fakeDisk{fail: fault}
			g := NewDiskGuardFS(fs, DefaultThresholds())
			g.now = func() time.Time { return now }
			_ = g.Check(&pb.DiskStatus{AvailableBytes: 1})
			if !g.Paused() {
				t.Fatal("not paused")
			}
			for i := 0; i < 10; i++ {
				_ = g.Check(&pb.DiskStatus{AvailableBytes: 50e9})
				now = now.Add(time.Minute)
			}
			if g.Paused() || fs.creates != 1 {
				t.Fatalf("pause=%v attempts=%d", g.Paused(), fs.creates)
			}
			if len(g.Alerts()) != 2 {
				t.Fatal("false repeated full-volume notifications")
			}
		})
	}
}
func TestMetadataOnlyPublishesMeaningfulChanges(t *testing.T) {
	g := NewDiskGuardFS(&fakeDisk{size: ReserveBytes}, DefaultThresholds())
	check := func(a uint64) *pb.DiskStatus {
		_ = g.Check(&pb.DiskStatus{ObservedMs: time.Now().UnixMilli(), AvailableBytes: a, TotalBytes: 100e9, Pressure: DiskPressure(100e9, a)})
		return g.Metadata()
	}
	a := check(50e9)
	for i := 0; i < 5; i++ {
		if !proto.Equal(a, check(50e9-uint64(i)*1e6)) {
			t.Fatal("heartbeat metadata churn")
		}
	}
	b := check(50e9 - 250e6)
	if proto.Equal(a, b) || b.ObservedMs != 0 {
		t.Fatal("meaningful change hidden")
	}
	a = check(2e9)
	b = check(2e9 - 1)
	if a.Pressure == b.Pressure {
		t.Fatal("pressure edge delayed")
	}
}

type cancelEntropy struct {
	cancel context.CancelFunc
	calls  int
}

func (r *cancelEntropy) Read(b []byte) (int, error) {
	r.calls++
	for i := range b {
		b[i] = byte(i)
	}
	r.cancel()
	return len(b), nil
}
func TestReservePartialWriteCleanupAndRandomContent(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	r := &cancelEntropy{cancel: cancel}
	f := reserveFile{path: filepath.Join(dir, "reserve"), entropy: r}
	if e := f.Reserve(ctx, 2<<20); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	if _, e := os.Stat(f.path); !os.IsNotExist(e) || r.calls != 1 {
		t.Fatal("partial file retained")
	}
	f.entropy = nil
	if e := f.Reserve(context.Background(), 2<<20); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(f.path)
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Equal(b[:1<<20], b[1<<20:]) || bytes.Equal(b, make([]byte, len(b))) {
		t.Fatal("compressible or repeated reserve")
	}
	g := NewDiskGuardFS(f, DiskThresholds{Reserve: 2 << 20, Release: 10, Rebuild: 100, Warn: 50, Critical: 20, WarnRearm: 70, CriticalRearm: 60})
	if e = g.Check(&pb.DiskStatus{AvailableBytes: 1}); e != nil {
		t.Fatal(e)
	}
	if f.Size() != 0 {
		t.Fatal("real release failed")
	}
	if e = g.Check(&pb.DiskStatus{AvailableBytes: 200}); e != nil || f.Size() != 2<<20 {
		t.Fatal("real rebuild failed", e)
	}
	_ = io.Discard
}

func TestExternallyRemovedReserveIsRebuilt(t *testing.T) {
	fs := &fakeDisk{size: ReserveBytes}
	g := NewDiskGuardFS(fs, DefaultThresholds())
	_ = g.Check(&pb.DiskStatus{AvailableBytes: 20e9})
	fs.size = 0
	if e := g.Check(&pb.DiskStatus{AvailableBytes: 20e9}); e != nil || g.ReserveSize() != ReserveBytes || fs.creates != 1 {
		t.Fatal("missing reserve not rebuilt", e)
	}
}
