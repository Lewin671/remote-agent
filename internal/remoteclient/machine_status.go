package remoteclient

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/protocol"
	"google.golang.org/protobuf/proto"
)

func (c *Client) GetMachineStatus(ctx context.Context, machine string) (*pb.MachineStatus, error) {
	r, e := c.DoMachine(ctx, machine, &pb.Command{Kind: &pb.Command_GetMachineStatus{GetMachineStatus: &pb.GetMachineStatusRequest{}}})
	if e != nil {
		return nil, e
	}
	if r.GetMachineStatus() == nil {
		return nil, errors.New("missing machine status")
	}
	return r.GetMachineStatus(), nil
}
func IsDiskFull(err error) bool {
	var e *CommandError
	return errors.As(err, &e) && e.Code == protocol.DiskFull || protocol.MachineErrorCode(err) == protocol.DiskFull
}
func RecentMachineProject(meta *pb.MachineMeta) string {
	var path string
	var latest int64
	for _, p := range meta.GetProjects() {
		at := max(p.LatestCreatedMs, p.LatestEndedMs)
		if path == "" || at > latest {
			path, latest = p.Path, at
		}
	}
	return path
}
func MachineDiskReminder(online bool, meta *pb.MachineMeta) *pb.DiskStatus {
	if !online || !meta.GetCapabilities().GetMachineStatus() {
		return nil
	}
	s := meta.GetDiskStatus()
	if s == nil || s.AvailableBytes >= 10_000_000_000 {
		return nil
	}
	return proto.Clone(s).(*pb.DiskStatus)
}

// StableUsage retains the previous identity order until a challenger exceeds the
// preceding row by >2 percentage points or >50 decimal MB. Call separately for
// session/process lists; reset previous when changing the selected metric.
func StableUsage(previous []string, rows []*pb.ResourceUsage, memory bool) []*pb.ResourceUsage {
	byID := map[string]*pb.ResourceUsage{}
	for _, r := range rows {
		byID[r.Id] = r
	}
	out := []*pb.ResourceUsage{}
	for _, id := range previous {
		if r := byID[id]; r != nil {
			out = append(out, r)
			delete(byID, id)
		}
	}
	fresh := []*pb.ResourceUsage{}
	for _, r := range byID {
		fresh = append(fresh, r)
	}
	sort.Slice(fresh, func(i, j int) bool {
		if memory {
			if fresh[i].MemoryBytes == fresh[j].MemoryBytes {
				return fresh[i].Id < fresh[j].Id
			}
			return fresh[i].MemoryBytes > fresh[j].MemoryBytes
		}
		if fresh[i].CpuPercent == fresh[j].CpuPercent {
			return fresh[i].Id < fresh[j].Id
		}
		return fresh[i].CpuPercent > fresh[j].CpuPercent
	})
	out = append(out, fresh...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0; j-- {
			a, b := out[j-1], out[j]
			swap := b.CpuPercent-a.CpuPercent > 2
			if memory {
				swap = b.MemoryBytes > a.MemoryBytes && b.MemoryBytes-a.MemoryBytes > 50_000_000
			}
			if !swap {
				break
			}
			out[j-1], out[j] = b, a
		}
	}
	return out
}

const MachineStatusRequestTimeout = 15 * time.Second

type MachinePollState string

const (
	MachineIdle        MachinePollState = "idle"
	MachineLoading     MachinePollState = "loading"
	MachineLive        MachinePollState = "live"
	MachineStale       MachinePollState = "stale"
	MachineUnavailable MachinePollState = "unavailable"
)

type MachinePollSnapshot struct {
	State        MachinePollState
	Last         *pb.MachineStatus
	Failures     int
	CurveSinceMs int64
}

// MachineStatusController is thread safe. Run once with an owner context;
// SetActive couples visibility, online presence and the capability bit. Hidden
// transitions cancel and invalidate the request, including late successes.
type MachineStatusController struct {
	mu         sync.Mutex
	active     bool
	generation uint64
	cancel     context.CancelFunc
	wake       chan struct{}
	snapshot   MachinePollSnapshot
	interval   time.Duration
	fetch      func(context.Context) (*pb.MachineStatus, error)
}

func NewMachineStatusController(fetch func(context.Context) (*pb.MachineStatus, error), interval time.Duration) *MachineStatusController {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	return &MachineStatusController{fetch: fetch, interval: interval, wake: make(chan struct{}, 1), snapshot: MachinePollSnapshot{State: MachineIdle}}
}
func (p *MachineStatusController) SetActive(visible, online, supported bool) {
	p.mu.Lock()
	if visible && supported && !online {
		if p.snapshot.Last == nil {
			p.snapshot.State = MachineUnavailable
		} else {
			p.snapshot.State = MachineStale
		}
	}
	active := visible && online && supported
	if p.active == active {
		p.mu.Unlock()
		return
	}
	if p.active != active {
		p.active = active
		p.generation++
		if p.cancel != nil {
			p.cancel()
		}

	}
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}
func (p *MachineStatusController) Retry() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}
func (p *MachineStatusController) Snapshot() MachinePollSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.snapshot
	if s.Last != nil {
		s.Last = proto.Clone(s.Last).(*pb.MachineStatus)
	}
	return s
}
func (p *MachineStatusController) Run(ctx context.Context) {
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	defer timer.Stop()
	due := false
	for {
		p.mu.Lock()
		active, gen := p.active, p.generation
		p.mu.Unlock()
		if active && due {
			req, cancel := context.WithTimeout(ctx, MachineStatusRequestTimeout)
			p.mu.Lock()
			if gen != p.generation || !p.active {
				p.mu.Unlock()
				cancel()
				continue
			}
			p.cancel = cancel
			if p.snapshot.Last == nil && p.snapshot.State == MachineIdle {
				p.snapshot.State = MachineLoading
			}
			p.mu.Unlock()
			started := time.Now()
			s, e := p.fetch(req)
			cancel()
			p.mu.Lock()
			if gen == p.generation && p.active {
				if e == nil {
					if p.snapshot.State == MachineStale || p.snapshot.State == MachineUnavailable {
						p.snapshot.CurveSinceMs = s.ObservedMs
					}
					p.snapshot.Last = proto.Clone(s).(*pb.MachineStatus)
					p.snapshot.Failures = 0
					p.snapshot.State = MachineLive
				} else {
					p.snapshot.Failures = min(3, p.snapshot.Failures+1)
					if p.snapshot.Last == nil {
						p.snapshot.State = MachineUnavailable
					}
					if p.snapshot.Failures >= 3 {
						p.snapshot.State = MachineStale
						if p.snapshot.Last == nil {
							p.snapshot.State = MachineUnavailable
						}
					}
				}
			}
			p.cancel = nil
			p.mu.Unlock()
			timer.Reset(max(0, p.interval-time.Since(started)))
			due = false
		}
		select {
		case <-ctx.Done():
			return
		case <-p.wake:
			due = true
		case <-timer.C:
			due = true
		}
	}
}

// SetMachinePushTarget gates the new kind by an explicit server capability;
// absence never causes an old server to reject the existing subscription.
func (c *Client) SetMachinePushTarget(ctx context.Context, kind pb.PushType, target string, kinds []pb.NotifyKind) (*pb.SetPushTargetResponse, error) {
	selected := []pb.NotifyKind{}
	if target != "" {
		cap, e := c.api.GetPushKey(ctx, connect.NewRequest(&pb.GetPushKeyRequest{}))
		if e != nil {
			return nil, e
		}
		for _, k := range kinds {
			if k != pb.NotifyKind_NOTIFY_KIND_MACHINE_DISK {
				selected = append(selected, k)
			}
		}
		if cap.Msg.MachineDiskNotifications {
			selected = append(selected, pb.NotifyKind_NOTIFY_KIND_MACHINE_DISK)
		}
	}
	r, e := c.api.SetPushTarget(ctx, connect.NewRequest(&pb.SetPushTargetRequest{Type: kind, Target: target, Kinds: selected}))
	if e != nil {
		return nil, e
	}
	return r.Msg, nil
}
