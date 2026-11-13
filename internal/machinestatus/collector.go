// Package machinestatus collects volatile telemetry. It never reads argv,
// environments or working directories and never logs or persists snapshots.
package machinestatus

import (
	"context"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"golang.org/x/sys/unix"
	"google.golang.org/protobuf/proto"
)

const Interval = 2 * time.Second
const IdleTimeout = 30 * time.Second
const Points = 60

type Process struct {
	PID, Parent int
	Start       uint64
	Name        string
	CPUSeconds  float64
	RSS         uint64
}
type Raw struct {
	Total, Idle uint64
	Status      *pb.MachineStatus
	Processes   []Process
}
type Reader func(context.Context) (Raw, error)

// Collector owns a single sampling worker, including across concurrent clients.
// A cancelled page request does not cancel another viewer's sampling lease.
type Collector struct {
	mu             sync.Mutex
	read           Reader
	roots          func() map[int]string
	stateDir       string
	now            func() time.Time
	sampleInterval time.Duration
	idleTimeout    time.Duration
	primeInterval  time.Duration
	lastRequest    time.Time
	previous       Raw
	previousAt     time.Time
	latest         *pb.MachineStatus
	samples        []*pb.MachineSample
	wake           chan struct{}
	ready          chan struct{}
	failure        error
}

func New(dir string, roots func() map[int]string) *Collector {
	return NewReader(dir, roots, readSystem)
}
func NewReader(dir string, roots func() map[int]string, read Reader) *Collector {
	return &Collector{read: read, roots: roots, stateDir: dir, now: time.Now, sampleInterval: Interval, idleTimeout: IdleTimeout, primeInterval: 100 * time.Millisecond, wake: make(chan struct{}, 1), ready: make(chan struct{})}
}

// Run belongs to daemon lifetime. No system reads occur without a current lease.
func (c *Collector) Run(ctx context.Context) {
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()
	active := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.wake:
			if !active {
				c.sample(ctx)
				active = true
				timer.Reset(c.sampleInterval)
			}
		case <-timer.C:
			c.mu.Lock()
			viewing := c.now().Sub(c.lastRequest) < c.idleTimeout
			c.mu.Unlock()
			if viewing {
				started := c.now()
				c.sample(ctx)
				timer.Reset(max(time.Millisecond, c.sampleInterval-c.now().Sub(started)))
			} else {
				active = false
				c.mu.Lock()
				c.previous = Raw{}
				c.previousAt = time.Time{}
				c.mu.Unlock()
			}
		}
	}
}
func (c *Collector) Get(ctx context.Context) (*pb.MachineStatus, error) {
	c.mu.Lock()
	c.lastRequest = c.now()
	ready := c.ready
	// Return only fresh snapshots. A new viewer after a long pause waits for a real sample.
	fresh := c.latest != nil && c.now().UnixMilli()-c.latest.ObservedMs < int64(2*Interval/time.Millisecond)
	var s *pb.MachineStatus
	if fresh {
		s = proto.Clone(c.latest).(*pb.MachineStatus)
	}
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
	if s != nil {
		return s, nil
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-ready:
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failure != nil {
		return nil, c.failure
	}
	if c.latest == nil {
		return nil, fmt.Errorf("machine sampler unavailable")
	}
	return proto.Clone(c.latest).(*pb.MachineStatus), nil
}
func (c *Collector) sample(ctx context.Context) {
	raw, err := c.read(ctx)
	c.mu.Lock()
	cold := c.previousAt.IsZero()
	c.mu.Unlock()
	if err == nil && cold { // A real short first interval, never a fabricated zero point.
		c.mu.Lock()
		c.previous = raw
		c.previousAt = c.now()
		c.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-time.After(c.primeInterval):
		}
		raw, err = c.read(ctx)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failure = err
	if err == nil {
		now := c.now()
		s := raw.Status
		s.ObservedMs = now.UnixMilli()
		dt := raw.Total - c.previous.Total
		idle := raw.Idle - c.previous.Idle
		if dt > 0 && idle <= dt {
			s.CpuPercent = 100 * float64(dt-idle) / float64(dt)
		}
		old := map[int]Process{}
		for _, p := range c.previous.Processes {
			old[p.PID] = p
		}
		roots := map[int]string{}
		if c.roots != nil {
			roots = c.roots()
		}
		s.Sessions, s.Processes = Attribute(raw.Processes, old, roots, now.Sub(c.previousAt).Seconds())
		if disk, e := Volume(c.stateDir); e == nil {
			s.Disk = disk
		}
		// Tree walking is bounded to once per active minute, not every 2 seconds.
		if c.latest != nil {
			s.AgentBytes = c.latest.AgentBytes
		}
		if c.latest == nil || len(c.samples) == 0 || now.UnixMilli()/60000 != c.latest.ObservedMs/60000 {
			if n, e := Allocated(ctx, c.stateDir); e == nil {
				s.AgentBytes = &n
			}
		}
		c.samples = append(c.samples, &pb.MachineSample{TimeMs: s.ObservedMs, CpuPercent: s.CpuPercent, MemoryUsedBytes: s.MemoryUsedBytes})
		cut := now.Add(-2 * time.Minute).UnixMilli()
		for len(c.samples) > Points || len(c.samples) > 0 && c.samples[0].TimeMs <= cut {
			c.samples = c.samples[1:]
		}
		s.Samples = append([]*pb.MachineSample(nil), c.samples...)
		c.latest = s
		c.previous = raw
		c.previousAt = now
	}
	close(c.ready)
	c.ready = make(chan struct{})
}

// Attribute assigns each process to its nearest known live tmux root. Start
// identities reject recycled PIDs for CPU deltas; cycles never hang collection.
func Attribute(ps []Process, old map[int]Process, roots map[int]string, elapsed float64) ([]*pb.ResourceUsage, []*pb.ResourceUsage) {
	byPID := map[int]Process{}
	for _, p := range ps {
		byPID[p.PID] = p
	}
	sessions := map[string]*pb.ResourceUsage{}
	for pid, id := range roots {
		if _, ok := byPID[pid]; ok {
			sessions[id] = &pb.ResourceUsage{Id: id, SessionId: id}
		}
	}
	var processes []*pb.ResourceUsage
	for _, p := range ps {
		seen := map[int]bool{}
		sid := ""
		for pid := p.PID; pid > 0 && !seen[pid]; {
			seen[pid] = true
			if id := roots[pid]; id != "" {
				sid = id
				break
			}
			q, ok := byPID[pid]
			if !ok {
				break
			}
			pid = q.Parent
		}
		cpu := 0.0
		if q, ok := old[p.PID]; ok && q.Start == p.Start && elapsed > 0 {
			cpu = math.Max(0, 100*(p.CPUSeconds-q.CPUSeconds)/elapsed)
		}
		r := &pb.ResourceUsage{Id: fmt.Sprintf("%d:%d", p.PID, p.Start), Executable: filepath.Base(p.Name), SessionId: sid, CpuPercent: cpu, MemoryBytes: p.RSS}
		processes = append(processes, r)
		if s := sessions[sid]; s != nil {
			s.CpuPercent += cpu
			s.MemoryBytes += p.RSS
		}
	}
	sort.Slice(processes, func(i, j int) bool {
		if processes[i].CpuPercent == processes[j].CpuPercent {
			return processes[i].Id < processes[j].Id
		}
		return processes[i].CpuPercent > processes[j].CpuPercent
	})
	top := append([]*pb.ResourceUsage(nil), processes[:min(8, len(processes))]...)
	included := map[string]bool{}
	for _, p := range top {
		included[p.Id] = true
	}
	sort.Slice(processes, func(i, j int) bool {
		if processes[i].MemoryBytes == processes[j].MemoryBytes {
			return processes[i].Id < processes[j].Id
		}
		return processes[i].MemoryBytes > processes[j].MemoryBytes
	})
	for _, p := range processes[:min(8, len(processes))] {
		if !included[p.Id] {
			top = append(top, p)
		}
	}
	out := []*pb.ResourceUsage{}
	for _, s := range sessions {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Id < out[j].Id })
	return out, top
}
func Volume(dir string) (*pb.DiskStatus, error) {
	var s unix.Statfs_t
	if err := unix.Statfs(dir, &s); err != nil {
		return nil, err
	}
	total, available := uint64(s.Blocks)*uint64(s.Bsize), uint64(s.Bavail)*uint64(s.Bsize)
	return &pb.DiskStatus{ObservedMs: time.Now().UnixMilli(), TotalBytes: total, AvailableBytes: available, Pressure: DiskPressure(total, available)}, nil
}
func DiskPressure(total, available uint64) pb.ResourcePressure {
	if available < 2_000_000_000 {
		return pb.ResourcePressure_RESOURCE_PRESSURE_CRITICAL
	}
	if available < 10_000_000_000 || total > 0 && float64(available)/float64(total) < 0.05 {
		return pb.ResourcePressure_RESOURCE_PRESSURE_WARNING
	}
	return pb.ResourcePressure_RESOURCE_PRESSURE_NORMAL
}
func Allocated(ctx context.Context, dir string) (uint64, error) {
	var n uint64
	seen := map[[2]uint64]bool{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		i, e := d.Info()
		if os.IsNotExist(e) {
			return nil
		}
		if e != nil {
			return e
		}
		n += allocatedInfo(i, seen)
		return nil
	})
	return n, err
}
