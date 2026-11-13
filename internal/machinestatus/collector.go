// Package machinestatus collects volatile telemetry. It never reads argv,
// environments or working directories and never logs or persists snapshots.
package machinestatus

import (
	"context"
	"errors"
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
	after          func(time.Duration) <-chan time.Time
	allocated      func(context.Context, string) (uint64, error)
	agentAt        time.Time
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
	return &Collector{read: read, roots: roots, stateDir: dir, now: time.Now, after: time.After, allocated: Allocated, sampleInterval: Interval, idleTimeout: IdleTimeout, primeInterval: 100 * time.Millisecond, wake: make(chan struct{}, 1), ready: make(chan struct{})}
}

// Run belongs to daemon lifetime. No system reads occur without a current lease.
func (c *Collector) Run(ctx context.Context) {
	var tick <-chan time.Time
	active := false
	for {
		select {
		case <-ctx.Done():
			return
		case <-c.wake:
			if !active {
				c.sample(ctx)
				active = true
				tick = c.after(c.sampleInterval)
			}
		case <-tick:
			c.mu.Lock()
			viewing := c.now().Sub(c.lastRequest) < c.idleTimeout
			c.mu.Unlock()
			if viewing {
				started := c.now()
				c.sample(ctx)
				tick = c.after(max(time.Millisecond, c.sampleInterval-c.now().Sub(started)))
			} else {
				active = false
				tick = nil
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

var errCounterReset = errors.New("machine CPU counters reset")

func (c *Collector) sample(ctx context.Context) {
	// Capture only in-memory facts. System APIs, tmux and walks never own c.mu.
	c.mu.Lock()
	previous, previousAt := c.previous, c.previousAt
	var agentBytes *uint64
	if c.latest != nil && c.latest.AgentBytes != nil {
		n := *c.latest.AgentBytes
		agentBytes = &n
	}
	walk := c.agentAt.IsZero() || c.now().Sub(c.agentAt) >= time.Minute
	c.mu.Unlock()
	raw, err := c.read(ctx)
	if err == nil && previousAt.IsZero() {
		previous = raw
		previousAt = c.now()
		// Some kernels cache host counters for several short intervals.
		// Await a real delta on first entry; never manufacture a zero point.
		for attempt := 0; attempt < 20; attempt++ {
			select {
			case <-ctx.Done():
				return
			case <-c.after(c.primeInterval):
			}
			raw, err = c.read(ctx)
			if err != nil || raw.Total != previous.Total || raw.Idle != previous.Idle {
				break
			}
		}
	}
	now := c.now()
	valid := err == nil
	if valid && (raw.Total <= previous.Total || raw.Idle < previous.Idle || raw.Idle-previous.Idle > raw.Total-previous.Total) {
		err = errCounterReset
		valid = false
	}
	if valid {
		s := raw.Status
		s.ObservedMs = now.UnixMilli()
		s.CpuPercent = 100 * float64((raw.Total-previous.Total)-(raw.Idle-previous.Idle)) / float64(raw.Total-previous.Total)
		old := map[int]Process{}
		for _, p := range previous.Processes {
			old[p.PID] = p
		}
		roots := map[int]string{}
		if c.roots != nil {
			roots = c.roots()
		}
		s.Sessions, s.Processes = Attribute(raw.Processes, old, roots, now.Sub(previousAt).Seconds())
		if disk, e := Volume(c.stateDir); e == nil {
			s.Disk = disk
		}
		s.AgentBytes = agentBytes
		if walk {
			if n, e := c.allocated(ctx, c.stateDir); e == nil {
				s.AgentBytes = &n
			}
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failure = err
	if valid {
		s := raw.Status
		if walk {
			c.agentAt = now
		}
		c.samples = append(c.samples, &pb.MachineSample{TimeMs: s.ObservedMs, CpuPercent: s.CpuPercent, MemoryUsedBytes: s.MemoryUsedBytes})
		cut := now.Add(-2 * time.Minute).UnixMilli()
		for len(c.samples) > Points || len(c.samples) > 0 && c.samples[0].TimeMs <= cut {
			c.samples = c.samples[1:]
		}
		s.Samples = append([]*pb.MachineSample(nil), c.samples...)
		c.latest = s
	}
	// Counter regressions establish a new baseline but add no invented point.
	if valid || errors.Is(err, errCounterReset) {
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
