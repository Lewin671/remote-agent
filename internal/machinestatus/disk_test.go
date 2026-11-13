package machinestatus

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

type fakeDisk struct {
	size             uint64
	fail             error
	creates, removes int
}

func (f *fakeDisk) Reserve(_ context.Context, n uint64) error {
	f.creates++
	if f.fail != nil {
		return f.fail
	}
	f.size = n
	return nil
}
func (f *fakeDisk) Remove() error { f.removes++; f.size = 0; return nil }
func (f *fakeDisk) Size() uint64  { return f.size }
func TestDiskThresholdsRearmAndReserve(t *testing.T) {
	fs := &fakeDisk{}
	g := NewDiskGuardFS(fs, DefaultThresholds())
	var sequence int64
	check := func(a uint64) {
		sequence++
		t.Helper()
		if e := g.Check(&pb.DiskStatus{ObservedMs: sequence, AvailableBytes: a, TotalBytes: 100e9}); e != nil {
			t.Fatal(e)
		}
	}
	check(20e9)
	if fs.size != ReserveBytes {
		t.Fatal("not reserved")
	}
	check(9e9)
	check(9e9)
	if len(g.Alerts()) != 1 {
		t.Fatal("warning repeated")
	}
	check(1e9)
	if len(g.Alerts()) != 2 {
		t.Fatal("critical missing")
	}
	for _, s := range g.Alerts() {
		g.Ack(s.ObservedMs, s.Pressure)
	}
	check(2e9)
	check(1e9)
	if len(g.Alerts()) != 0 {
		t.Fatal("critical not latched")
	}
	check(6e9)
	check(1e9)
	if len(g.Alerts()) != 1 {
		t.Fatal("critical not rearmed")
	}
	check(16e9)
	check(9e9)
	if len(g.Alerts()) != 2 {
		t.Fatal("warning not rearmed")
	}
	check(299e6)
	if !g.Paused() || fs.size != 0 {
		t.Fatal("not released")
	}
	check(1e9)
	if !g.Paused() {
		t.Fatal("resumed early")
	}
	check(5e9)
	if fs.size != 0 {
		t.Fatal("rebuilt on boundary")
	}
	check(5e9 + 1)
	if g.Paused() || fs.size != ReserveBytes {
		t.Fatal("not restored")
	}
	if DiskPressure(1e12, 20e9) != pb.ResourcePressure_RESOURCE_PRESSURE_WARNING || DiskPressure(100e9, 2e9) != pb.ResourcePressure_RESOURCE_PRESSURE_WARNING || DiskPressure(100e9, 2e9-1) != pb.ResourcePressure_RESOURCE_PRESSURE_CRITICAL {
		t.Fatal("display pressure")
	}
}
func TestWriteFailureAndReserveFailure(t *testing.T) {
	fs := &fakeDisk{size: ReserveBytes}
	g := NewDiskGuardFS(fs, DefaultThresholds())
	if e := g.WriteFailed(errors.New("other")); e != nil || fs.removes != 0 {
		t.Fatal("non-space error released")
	}
	if e := g.WriteFailedAt(&os.PathError{Op: "write", Path: "fixture", Err: syscall.ENOSPC}, &pb.DiskStatus{AvailableBytes: 1}); e != nil {
		t.Fatal(e)
	}
	if fs.size != 0 || !g.Paused() || len(g.Alerts()) != 1 {
		t.Fatal("write failed guard")
	}
	fs.fail = syscall.EDQUOT
	if e := g.Check(&pb.DiskStatus{AvailableBytes: 20e9}); !NoSpace(e) {
		t.Fatal(e)
	}
	if fs.size != 0 || g.Paused() {
		t.Fatal("reserve failure must not pause recovered storage")
	}
}
func TestReservePhysicalFileLifecycle(t *testing.T) {
	dir := t.TempDir()
	f := reserveFile{path: filepath.Join(dir, "reserve")}
	if e := f.Reserve(context.Background(), 8192); e != nil {
		t.Fatal(e)
	}
	if f.Size() != 8192 {
		t.Fatal("wrong size")
	}
	n, e := Allocated(context.Background(), dir)
	if e != nil || n < 8192 {
		t.Fatalf("sparse reserve: %d %v", n, e)
	}
	if e = f.Reserve(context.Background(), 8192); e != nil {
		t.Fatal(e)
	}
	if e = f.Remove(); e != nil || f.Size() != 0 {
		t.Fatal(e)
	}
}
