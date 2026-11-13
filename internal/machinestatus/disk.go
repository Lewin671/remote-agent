package machinestatus

import (
	"context"
	"crypto/rand"
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
	Reserve(context.Context, uint64) error
	Remove() error
	Size() uint64
}
type reserveFile struct {
	path    string
	entropy io.Reader
}

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
func (f reserveFile) Reserve(ctx context.Context, n uint64) error {
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
	// Fresh random bytes resist transparent compression AND block deduplication.
	// Actual blocks, not truncate/sparse allocation. Bound transient memory.
	entropy := f.entropy
	if entropy == nil {
		entropy = rand.Reader
	}
	buf := make([]byte, 1<<20)
	for left := n; left > 0; {
		if err := ctx.Err(); err != nil {
			return err
		}
		k := min(left, uint64(len(buf)))
		if _, err := io.ReadFull(entropy, buf[:k]); err != nil {
			return err
		}
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
	mu                  sync.Mutex
	fs                  DiskFS
	limits              DiskThresholds
	status, published   *pb.DiskStatus
	warned, critical    bool
	alerts              []*pb.DiskStatus
	reserveSize         uint64
	building, releasing bool
	generation          uint64
	reserveCancel       context.CancelFunc
	retryAt             time.Time
	now                 func() time.Time
	volume              func() (*pb.DiskStatus, error)
}

func NewDiskGuard(dir string) *DiskGuard {
	g := NewDiskGuardFS(reserveFile{path: filepath.Join(dir, "emergency-reserve")}, DefaultThresholds())
	g.volume = func() (*pb.DiskStatus, error) { return Volume(dir) }
	return g
}
func NewDiskGuardFS(fs DiskFS, t DiskThresholds) *DiskGuard {
	return &DiskGuard{fs: fs, limits: t, reserveSize: fs.Size(), now: time.Now}
}
func cloneDisk(s *pb.DiskStatus) *pb.DiskStatus {
	if s == nil {
		return nil
	}
	return proto.Clone(s).(*pb.DiskStatus)
}
func (g *DiskGuard) Snapshot() *pb.DiskStatus {
	g.mu.Lock()
	defer g.mu.Unlock()
	return cloneDisk(g.status)
}
func (g *DiskGuard) Paused() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.status != nil && g.status.DeferredWritesPaused
}
func (g *DiskGuard) ReserveSize() uint64 { g.mu.Lock(); defer g.mu.Unlock(); return g.reserveSize }

// Metadata is change driven, without a heartbeat timestamp. Bounds for the
// displayed remaining-space drift: 250 MB normal, 100 MB warning, 50 MB critical.
func (g *DiskGuard) Metadata() *pb.DiskStatus {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.status == nil {
		return nil
	}
	s := cloneDisk(g.status)
	s.ObservedMs = 0
	p := g.published
	step := uint64(250_000_000)
	if s.AvailableBytes < WarnBelow || s.Pressure == pb.ResourcePressure_RESOURCE_PRESSURE_WARNING {
		step = 100_000_000
	}
	if s.AvailableBytes < CriticalBelow || s.Pressure == pb.ResourcePressure_RESOURCE_PRESSURE_CRITICAL {
		step = 50_000_000
	}
	changed := p == nil
	if p != nil {
		delta := max(s.AvailableBytes, p.AvailableBytes) - min(s.AvailableBytes, p.AvailableBytes)
		changed = delta >= step || s.TotalBytes != p.TotalBytes || s.Pressure != p.Pressure || s.EmergencyReserveBytes != p.EmergencyReserveBytes || s.DeferredWritesPaused != p.DeferredWritesPaused
	}
	if changed {
		g.published = s
	}
	return cloneDisk(g.published)
}
func (g *DiskGuard) Check(s *pb.DiskStatus) error { return g.CheckContext(context.Background(), s) }
func (g *DiskGuard) CheckContext(parent context.Context, s *pb.DiskStatus) error {
	observedSize := g.fs.Size() // filesystem observation never owns the state lock
	g.mu.Lock()
	if !g.building && !g.releasing {
		g.reserveSize = observedSize
	}
	if g.status != nil && s.ObservedMs > 0 && s.ObservedMs < g.status.ObservedMs {
		g.mu.Unlock()
		return nil
	}
	paused := g.status != nil && g.status.DeferredWritesPaused
	g.status = cloneDisk(s)
	g.status.DeferredWritesPaused = paused
	g.status.EmergencyReserveBytes = g.reserveSize
	a, t := s.AvailableBytes, g.limits
	if a > t.WarnRearm {
		g.warned = false
	}
	if a > t.CriticalRearm {
		g.critical = false
	}
	// Recovery and reserve creation are separate decisions: permission/quota
	// failures creating the reserve must never permanently pause image writes.
	if a > t.Rebuild {
		g.status.DeferredWritesPaused = false
	}
	if a < t.Warn && !g.warned {
		g.warned = true
		g.alert(pb.ResourcePressure_RESOURCE_PRESSURE_WARNING)
	}
	if a < t.Critical && !g.critical {
		g.critical = true
		g.alert(pb.ResourcePressure_RESOURCE_PRESSURE_CRITICAL)
	}
	if a < t.Release {
		g.mu.Unlock()
		return g.release()
	}
	if a <= t.Rebuild || g.reserveSize == t.Reserve || g.building || g.releasing || g.now().Before(g.retryAt) {
		g.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(parent)
	g.reserveCancel = cancel
	g.building = true
	gen := g.generation
	g.mu.Unlock()
	// No guard state mutex is held for allocation, random filling, fsync or stat.
	err := g.fs.Reserve(ctx, t.Reserve)
	size := g.fs.Size()
	cancel()
	g.mu.Lock()
	g.building = false
	g.reserveCancel = nil
	if gen == g.generation {
		g.reserveSize = size
		g.status.EmergencyReserveBytes = size
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		g.retryAt = g.now().Add(15 * time.Minute)
	} else if err == nil {
		g.retryAt = time.Time{}
	}
	g.mu.Unlock()
	if errors.Is(err, context.Canceled) {
		return nil
	}
	if NoSpace(err) && g.volume != nil {
		if current, e := g.volume(); e == nil {
			_ = g.WriteFailedAt(err, current)
		}
	}
	return err
}
func (g *DiskGuard) alert(p pb.ResourcePressure) {
	s := cloneDisk(g.status)
	s.Pressure = p
	g.alerts = append(g.alerts, s)
	if len(g.alerts) > 2 {
		g.alerts = g.alerts[len(g.alerts)-2:]
	}
}
func (g *DiskGuard) release() error {
	g.mu.Lock()
	g.generation++
	if g.reserveCancel != nil {
		g.reserveCancel()
	}
	g.status.DeferredWritesPaused = true
	if !g.critical {
		g.critical = true
		g.alert(pb.ResourcePressure_RESOURCE_PRESSURE_CRITICAL)
	}
	if g.releasing {
		g.mu.Unlock()
		return nil
	}
	g.releasing = true
	g.mu.Unlock()
	err := g.fs.Remove()
	size := g.fs.Size()
	g.mu.Lock()
	g.releasing = false
	g.reserveSize = size
	g.status.EmergencyReserveBytes = size
	g.mu.Unlock()
	return err
}

// A space error is evidence about the failing destination, not automatically
// about our state volume. Only verified emergency-low state space releases it.
func StateVolumeFull(current *pb.DiskStatus) bool {
	return current != nil && current.AvailableBytes < ReleaseBelow
}
func (g *DiskGuard) WriteFailed(err error) error { return g.WriteFailedAt(err, g.Snapshot()) }
func (g *DiskGuard) WriteFailedAt(err error, current *pb.DiskStatus) error {
	if !NoSpace(err) || !StateVolumeFull(current) {
		return nil
	}
	g.mu.Lock()
	paused := g.status != nil && g.status.DeferredWritesPaused
	g.status = cloneDisk(current)
	g.status.DeferredWritesPaused = paused
	g.status.EmergencyReserveBytes = g.reserveSize
	g.mu.Unlock()
	return g.release()
}
func (g *DiskGuard) Alerts() []*pb.DiskStatus {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]*pb.DiskStatus, len(g.alerts))
	for i, s := range g.alerts {
		out[i] = cloneDisk(s)
	}
	return out
}
func (g *DiskGuard) Ack(at int64, p pb.ResourcePressure) {
	g.mu.Lock()
	defer g.mu.Unlock()
	for i, s := range g.alerts {
		if s.ObservedMs == at && s.Pressure == p {
			g.alerts = append(g.alerts[:i], g.alerts[i+1:]...)
			break
		}
	}
}
