package transport

import (
	"context"
	"sync"
	"time"
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
	Run           func(context.Context, *Execution)
	Reject        func()
	state         uint8
	stopWait      func() bool
	timer         *time.Timer
}

type Scheduler struct {
	link           *Link
	mu             sync.Mutex
	queues         [4]FairQueue[*Job]
	used           [3]int
	nonApproval    int
	activeSessions map[string]bool
	quarantined    map[string]int
	closed         bool
}

func NewScheduler(link *Link) *Scheduler {
	s := &Scheduler{link: link, activeSessions: map[string]bool{}, quarantined: map[string]int{}}
	for c, count := range []int{32, 16, 64, 16} {
		s.queues[c] = FairQueue[*Job]{Entries: count, Bytes: ExecutionQueueBytes, PeerEntries: count / 4, PeerBytes: ExecutionQueueBytes / 4, EmptyPeerException: true}
	}
	return s
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
	if remain := time.Until(j.Deadline); remain < wait {
		wait = remain
	}
	j.timer = time.AfterFunc(wait, func() { s.expire(j) })
	j.stopWait = context.AfterFunc(waiting, func() { s.expire(j) })
	s.pumpLocked()
	s.mu.Unlock()
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
	j.timer.Stop()
	j.stopWait()
	s.pumpLocked()
	s.mu.Unlock()
	j.Reject()
}
func (s *Scheduler) pumpLocked() {
	limits := [3]int{2, 4, 2}
	for _, class := range []Class{Control, Input, Ordinary, Bulk} {
		pool := class.pool()
		for s.used[pool] < limits[pool] {
			j, ok := s.queues[class].Pop(func(j *Job) bool {
				if s.blocked(j) {
					return false
				}
				if pool == 0 {
					if s.activeSessions[s.session(j)] {
						return false
					}
					// One of the two interactive slots remains available to an
					// approval while input/interrupt traffic is sustained.
					if !j.Approval && s.nonApproval >= 1 {
						return false
					}
				}
				return true
			})
			if !ok {
				break
			}
			j.state = 2
			j.timer.Stop()
			j.stopWait()
			s.used[pool]++
			if pool == 0 {
				s.activeSessions[s.session(j)] = true
				if !j.Approval {
					s.nonApproval++
				}
			}
			ex := &Execution{owner: s, job: j, granted: time.Now()}
			// The link refuses only during shutdown. The existing reservation
			// is released even then; no caller can bypass it with a goroutine.
			if !s.link.Run(func(ctx context.Context) {
				defer ex.Finish()
				deadline := ex.granted.Add(j.Class.Duration())
				if j.Deadline.Before(deadline) {
					deadline = j.Deadline
				}
				run, cancel := context.WithDeadline(ctx, deadline)
				defer cancel()
				j.Run(run, ex)
			}) {
				s.releaseLocked(ex)
				// Shutdown is already observable to every path; no reply worker
				// is created after the lifetime has ended.
			}
		}
	}
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
		if !j.Approval {
			s.nonApproval--
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
	s.pumpLocked()
	s.mu.Unlock()
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
			j.timer.Stop()
			j.stopWait()
			rejected = append(rejected, j)
		}
	}
	s.mu.Unlock()
	for _, j := range rejected {
		j.Reject()
	}
}
