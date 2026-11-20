package transport

import (
	"context"
	"sync"
	"time"

	"github.com/Lewin671/remote-agent/shared/linkcore"
)

type Class uint8

const (
	Control Class = iota
	Input
	Ordinary
	Bulk
)
const ExecutionQueueBytes = 4 << 20
const ControlQueueWait = 75 * time.Millisecond

func (c Class) Duration() time.Duration {
	switch c {
	case Control, Input:
		return 5 * time.Second
	case Ordinary:
		return 60 * time.Second
	default:
		return 10 * time.Second
	}
}
func (c Class) pool() int {
	if c <= Input {
		return 0
	}
	return int(c) - 1
}

// Job callbacks run without scheduler locks. Run owns completion, including any
// backend work that ignores cancellation. It must not return before that work.
type Job struct {
	Class         Class
	Peer, Session string
	Resources     []string
	Bytes         int
	Deadline      time.Time
	Approval      bool
	Snapshot      bool
	Priority      uint8
	Run           func(context.Context, *Execution)
	Reject        func()
	state         uint8
	stopWait      func() bool
	stopTimer     func() bool
}

type Scheduler struct {
	link            *Link
	now             func() time.Time
	after           func(time.Duration, func()) func() bool
	mu              sync.Mutex
	queues          [4]linkcore.FairQueue[*Job]
	used            [3]int
	nonReserved     int
	activeSnapshots map[string]bool
	activeSessions  map[string]bool
	quarantined     map[string]int
	closed          bool
}

func NewScheduler(link *Link) *Scheduler {
	return NewSchedulerWithClock(link, nil, nil)
}

// NewSchedulerWithClock constructs a Scheduler whose queue and execution
// bounds use now and after when provided.
func NewSchedulerWithClock(link *Link, now func() time.Time, after func(time.Duration, func()) func() bool) *Scheduler {
	s := &Scheduler{link: link, now: now, after: after, activeSessions: map[string]bool{}, activeSnapshots: map[string]bool{}, quarantined: map[string]int{}}
	for c, count := range []int{32, 16, 64, 16} {
		s.queues[c] = linkcore.FairQueue[*Job]{Entries: count, Bytes: ExecutionQueueBytes, PeerEntries: count / 4, PeerBytes: ExecutionQueueBytes / 4, EmptyPeerException: true}
	}
	return s
}

func (s *Scheduler) nowTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *Scheduler) afterFunc(d time.Duration, fn func()) func() bool {
	if s.after != nil {
		return s.after(d, fn)
	}
	return time.AfterFunc(d, fn).Stop
}
func (s *Scheduler) session(j *Job) string {
	if j.Session != "" {
		return "session:" + j.Session
	}
	return "peer:" + j.Peer
}
func (s *Scheduler) blocked(j *Job) bool {
	for _, r := range j.Resources {
		if s.quarantined[r] > 0 {
			return true
		}
	}
	return false
}
func (s *Scheduler) Submit(waiting context.Context, j *Job) bool {
	s.mu.Lock()
	if s.closed || waiting.Err() != nil || s.link.Context().Err() != nil || s.blocked(j) || !s.queues[j.Class].Push(j.Peer, s.session(j), j.Bytes, j) {
		s.mu.Unlock()
		return false
	}
	j.state = 1
	wait := j.Class.Duration()
	if j.Class == Control {
		wait = ControlQueueWait
	}
	if remain := j.Deadline.Sub(s.nowTime()); remain < wait {
		wait = remain
	}
	j.stopTimer = s.afterFunc(wait, func() { s.expire(j) })
	j.stopWait = context.AfterFunc(waiting, func() { s.expire(j) })
	dropped := s.pumpLocked()
	s.mu.Unlock()
	for _, dj := range dropped {
		dj.Reject()
	}
	return true
}
func (s *Scheduler) expire(j *Job) {
	s.mu.Lock()
	if j.state != 1 {
		s.mu.Unlock()
		return
	}
	s.queues[j.Class].Remove(j)
	j.state = 3
	j.stopTimer()
	j.stopWait()
	dropped := s.pumpLocked()
	s.mu.Unlock()
	j.Reject()
	for _, dj := range dropped {
		dj.Reject()
	}
}
func (s *Scheduler) pumpLocked() []*Job {
	var dropped []*Job
	limits := [3]int{2, 4, 2}
	for _, class := range []Class{Control, Input, Ordinary, Bulk} {
		pool := class.pool()
		for s.used[pool] < limits[pool] {
			eligible := func(j *Job) bool {
				if s.blocked(j) {
					return false
				}
				if pool == 0 {
					if s.activeSessions[s.session(j)] {
						return false
					}
					// The reserved slot also admits the fresh owner read required
					// before approval. A peer cannot run two snapshots at once.
					if j.Snapshot && s.activeSnapshots[j.Peer] {
						return false
					}
					if !j.Approval && !j.Snapshot && s.nonReserved >= 1 {
						return false
					}
				}
				return true
			}
			var j *Job
			var ok bool
			if class == Control {
				j, ok = s.queues[class].PopPriority(eligible, func(j *Job) uint8 { return j.Priority })
			} else {
				j, ok = s.queues[class].Pop(eligible)
			}
			if !ok {
				break
			}
			j.state = 2
			j.stopTimer()
			j.stopWait()
			s.used[pool]++
			if pool == 0 {
				s.activeSessions[s.session(j)] = true
				if j.Snapshot {
					s.activeSnapshots[j.Peer] = true
				} else if !j.Approval {
					s.nonReserved++
				}
			}
			ex := &Execution{owner: s, job: j, granted: s.nowTime()}
			// The link refuses only during shutdown. The existing reservation
			// is released even then; no caller can bypass it with a goroutine.
			if !s.link.Run(func(ctx context.Context) {
				defer ex.Finish()
				deadline := ex.granted.Add(j.Class.Duration())
				if j.Deadline.Before(deadline) {
					deadline = j.Deadline
				}
				var run context.Context
				var cancel context.CancelFunc
				if s.after == nil {
					run, cancel = context.WithDeadline(ctx, deadline)
				} else {
					run, cancel = context.WithCancel(ctx)
					stop := s.after(deadline.Sub(ex.granted), cancel)
					defer stop()
				}
				defer cancel()
				j.Run(run, ex)
			}) {
				s.releaseLocked(ex)
				j.state = 3
				dropped = append(dropped, j)
			}
		}
	}
	return dropped
}

type Execution struct {
	granted               time.Time
	owner                 *Scheduler
	job                   *Job
	quarantined, finished bool
}

func (x *Execution) GrantedAt() time.Time { return x.granted }

func (x *Execution) Quarantine() {
	s := x.owner
	s.mu.Lock()
	defer s.mu.Unlock()
	if x.finished || x.quarantined {
		return
	}
	x.quarantined = true
	for _, r := range x.job.Resources {
		s.quarantined[r]++
	}
}
func (s *Scheduler) releaseLocked(x *Execution) {
	if x.finished {
		return
	}
	x.finished = true
	j := x.job
	j.state = 3
	s.used[j.Class.pool()]--
	if j.Class <= Input {
		delete(s.activeSessions, s.session(j))
		if j.Snapshot {
			delete(s.activeSnapshots, j.Peer)
		} else if !j.Approval {
			s.nonReserved--
		}
	}
	if x.quarantined {
		for _, r := range j.Resources {
			s.quarantined[r]--
			if s.quarantined[r] == 0 {
				delete(s.quarantined, r)
			}
		}
	}
}
func (x *Execution) Finish() {
	s := x.owner
	s.mu.Lock()
	s.releaseLocked(x)
	dropped := s.pumpLocked()
	s.mu.Unlock()
	for _, dj := range dropped {
		dj.Reject()
	}
}
func (s *Scheduler) Active() [3]int { s.mu.Lock(); defer s.mu.Unlock(); return s.used }
func (s *Scheduler) Close() {
	s.mu.Lock()
	s.closed = true
	var rejected []*Job
	for c := range s.queues {
		for {
			j, ok := s.queues[c].Pop(func(*Job) bool { return true })
			if !ok {
				break
			}
			j.state = 3
			j.stopTimer()
			j.stopWait()
			rejected = append(rejected, j)
		}
	}
	s.mu.Unlock()
	for _, j := range rejected {
		j.Reject()
	}
}
