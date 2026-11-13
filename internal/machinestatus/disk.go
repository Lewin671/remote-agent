package machinestatus

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
)

const ReserveBytes uint64 = 256_000_000
const ReleaseBelow uint64 = 300_000_000
const RebuildAbove uint64 = 5_000_000_000
const WarnBelow uint64 = 10_000_000_000
const CriticalBelow uint64 = 2_000_000_000
const WarnRearmAbove uint64 = 15_000_000_000
const CriticalRearmAbove uint64 = 5_000_000_000

func NoSpace(err error) bool { return errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT) }

type DiskFS interface {
	Reserve(uint64) error
	Remove() error
	Size() uint64
}
type reserveFile struct{ path string }

func (f reserveFile) Size() uint64 {
	s, e := os.Lstat(f.path)
	if e != nil || !s.Mode().IsRegular() {
		return 0
	}
	return uint64(s.Size())
}
func (f reserveFile) Remove() error {
	e := os.Remove(f.path)
	if os.IsNotExist(e) {
		return nil
	}
	return e
}
func (f reserveFile) Reserve(n uint64) error {
	// O_EXCL refuses symlinks. Never truncate user data at a reserved filename.
	if f.Size() == n {
		return nil
	}
	if e := f.Remove(); e != nil {
		return e
	}
	out, e := os.OpenFile(f.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	ok := false
	defer func() {
		out.Close()
		if !ok {
			_ = f.Remove()
		}
	}()
	// Actual blocks, not truncate/sparse allocation. Bound transient memory.
	buf := make([]byte, 1<<20)
	for left := n; left > 0; {
		k := min(left, uint64(len(buf)))
		written, e := out.Write(buf[:k])
		if e != nil {
			return e
		}
		if written != int(k) {
			return io.ErrShortWrite
		}
		left -= k
	}
	if e = out.Sync(); e != nil {
		return e
	}
	if e = out.Close(); e != nil {
		return e
	}
	ok = true
	return nil
}

// DiskGuard has injectable thresholds and filesystem; no test fills a volume.
type DiskThresholds struct{ Reserve, Release, Rebuild, Warn, Critical, WarnRearm, CriticalRearm uint64 }

func DefaultThresholds() DiskThresholds {
	return DiskThresholds{ReserveBytes, ReleaseBelow, RebuildAbove, WarnBelow, CriticalBelow, WarnRearmAbove, CriticalRearmAbove}
}

type DiskGuard struct {
	mu               sync.Mutex
	fs               DiskFS
	limits           DiskThresholds
	status           *pb.DiskStatus
	warned, critical bool
	alerts           []*pb.DiskStatus
}

func NewDiskGuard(dir string) *DiskGuard {
	return NewDiskGuardFS(reserveFile{filepath.Join(dir, "emergency-reserve")}, DefaultThresholds())
}
func NewDiskGuardFS(fs DiskFS, t DiskThresholds) *DiskGuard { return &DiskGuard{fs: fs, limits: t} }
func (g *DiskGuard) Snapshot() *pb.DiskStatus {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.status == nil {
		return nil
	}
	return proto.Clone(g.status).(*pb.DiskStatus)
}
func (g *DiskGuard) Paused() bool        { s := g.Snapshot(); return s != nil && s.DeferredWritesPaused }
func (g *DiskGuard) ReserveSize() uint64 { g.mu.Lock(); defer g.mu.Unlock(); return g.fs.Size() }

// Check is called independently of telemetry once per minute. Failed creation
// never claims a reserve exists. Notifications stay in RAM until acknowledged.
func (g *DiskGuard) Check(s *pb.DiskStatus) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	paused := g.status != nil && g.status.DeferredWritesPaused
	g.status = proto.Clone(s).(*pb.DiskStatus)
	g.status.DeferredWritesPaused = paused
	a := s.AvailableBytes
	t := g.limits
	if a > t.WarnRearm {
		g.warned = false
	}
	if a > t.CriticalRearm {
		g.critical = false
	}
	var err error
	if a < t.Release {
		err = g.releaseLocked()
	} else if a > t.Rebuild {
		if g.fs.Size() != t.Reserve {
			err = g.fs.Reserve(t.Reserve)
		}
		if err == nil {
			g.status.DeferredWritesPaused = false
		}
	}
	if a < t.Warn && !g.warned {
		g.warned = true
		g.alert(pb.ResourcePressure_RESOURCE_PRESSURE_WARNING)
	}
	if a < t.Critical && !g.critical {
		g.critical = true
		g.alert(pb.ResourcePressure_RESOURCE_PRESSURE_CRITICAL)
	}
	if NoSpace(err) {
		_ = g.releaseLocked()
	}
	g.status.EmergencyReserveBytes = g.fs.Size()
	return err
}
func (g *DiskGuard) alert(p pb.ResourcePressure) {
	s := proto.Clone(g.status).(*pb.DiskStatus)
	s.Pressure = p
	g.alerts = append(g.alerts, s)
	if len(g.alerts) > 2 {
		g.alerts = g.alerts[len(g.alerts)-2:]
	}
}
func (g *DiskGuard) releaseLocked() error {
	g.status.DeferredWritesPaused = true
	e := g.fs.Remove()
	g.status.EmergencyReserveBytes = g.fs.Size()
	if !g.critical {
		g.critical = true
		g.alert(pb.ResourcePressure_RESOURCE_PRESSURE_CRITICAL)
	}
	return e
}
func (g *DiskGuard) WriteFailed(err error) error { return g.WriteFailedAt(err, nil) }
func (g *DiskGuard) WriteFailedAt(err error, current *pb.DiskStatus) error {
	if !NoSpace(err) {
		return nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if current != nil {
		g.status = proto.Clone(current).(*pb.DiskStatus)
	}
	if g.status == nil {
		g.status = &pb.DiskStatus{ObservedMs: time.Now().UnixMilli()}
	}
	return g.releaseLocked()
}
func (g *DiskGuard) Alerts() []*pb.DiskStatus {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]*pb.DiskStatus, len(g.alerts))
	for i, s := range g.alerts {
		out[i] = proto.Clone(s).(*pb.DiskStatus)
	}
	return out
}
func (g *DiskGuard) Ack(observed int64, pressure pb.ResourcePressure) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for i, s := range g.alerts {
		if s.ObservedMs == observed && s.Pressure == pressure {
			g.alerts = append(g.alerts[:i], g.alerts[i+1:]...)
			break
		}
	}
}
