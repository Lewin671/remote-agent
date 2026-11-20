package direct

import (
	"context"
	"reflect"
	"testing"

	"github.com/Lewin671/remote-agent/shared/linkcore"
)

// pump runs session executors on the test goroutine with real Pion PCs. It
// relays frames between paired sessions (through a loss filter), applies SDK
// callbacks as they arrive and ticks every session. The session clock moves
// only when every session is idle, with no frame or callback left to apply,
// and then only to the earliest session timer (a retransmission, candidate
// batch or reorder gap), never to a deadline; with no timer due it blocks
// until Pion reports something. Deadlines are judged on session time, so a
// loaded machine only makes the run slower, never different.
type pump struct {
	t     *testing.T
	ctx   context.Context
	clock *testClock
	start uint64
	ends  []*pumpEnd
}

type pumpEnd struct {
	s      *Session
	peer   *pumpEnd
	out    []Outbound
	drop   func(Outbound) bool
	ready  [3]*Path
	closed map[*Path]bool
	clock  *testClock
}

func newPump(t *testing.T, ctx context.Context) *pump {
	clock := newTestClock()
	return &pump{t: t, ctx: ctx, clock: clock, start: clock.Ms()}
}

// add starts a session that the pump drives; cfg's Now, Send and Ready are
// the pump's.
func (p *pump) add(cfg SessionConfig, initiate bool) *pumpEnd {
	p.t.Helper()
	e := &pumpEnd{closed: map[*Path]bool{}, clock: p.clock}
	cfg.Now = p.clock.Now
	cfg.Send = func(o Outbound) bool { e.out = append(e.out, o); return true }
	cfg.Ready = func(use uint64, path *Path) { e.ready[use] = path }
	s, initial, err := newSession(p.ctx, cfg, initiate)
	if err != nil {
		p.t.Fatal(err)
	}
	e.s = s
	p.ends = append(p.ends, e)
	p.t.Cleanup(s.finish)
	s.apply(initial)
	return e
}

func pair(a, b *pumpEnd) { a.peer, b.peer = b, a }

func (p *pump) elapsed() uint64 { return p.clock.Ms() - p.start }

// run pumps until done.
func (p *pump) run(done func() bool) {
	p.t.Helper()
	for !done() {
		if p.ctx.Err() != nil {
			p.t.Fatal("pump hung")
		}
		if p.round() {
			continue
		}
		if due, ok := p.nextTimer(); ok {
			p.clock.ms.Store(int64(max(due, p.clock.Ms()+1)))
			continue
		}
		p.wait()
	}
}

// round relays frames, applies callbacks and ticks every session once; it
// reports whether anything happened.
func (p *pump) round() bool {
	work := false
	for _, e := range p.ends {
		for len(e.out) > 0 {
			o := e.out[0]
			e.out = e.out[1:]
			if e.drop != nil && e.drop(o) {
				continue
			}
			work = true
			e.peer.s.handle(linkcore.FrameReceived{Raw: o.Raw})
		}
	}
	for _, e := range p.ends {
		for drained := false; !drained; {
			select {
			case ev := <-e.s.events:
				e.s.handle(e.s.admit(ev))
				work = true
			default:
				drained = true
			}
		}
		for _, path := range e.s.paths {
			if !e.closed[path] && isClosed(path) {
				e.closed[path] = true // its pool slot is free: creation may retry
				work = true
			}
		}
	}
	for _, e := range p.ends {
		e.s.tick()
		p.checkView(e)
		work = work || len(e.out) > 0
	}
	return work
}

func isClosed(path *Path) bool {
	select {
	case <-path.closed:
		return true
	default:
		return false
	}
}

func (p *pump) nextTimer() (due uint64, ok bool) {
	for _, e := range p.ends {
		if e.s.stopped {
			continue
		}
		if d, found := e.s.core.NextTimer(); found && (!ok || d < due) {
			due, ok = d, true
		}
	}
	return due, ok
}

// wait blocks until an SDK callback arrives or a closing PC finishes.
func (p *pump) wait() {
	cases := []reflect.SelectCase{{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(p.ctx.Done())}}
	var owners []*pumpEnd
	for _, e := range p.ends {
		cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(e.s.events)})
		owners = append(owners, e)
		for _, path := range e.s.paths {
			if !e.closed[path] {
				cases = append(cases, reflect.SelectCase{Dir: reflect.SelectRecv, Chan: reflect.ValueOf(path.closed)})
				owners = append(owners, nil)
			}
		}
	}
	i, v, ok := reflect.Select(cases)
	if i > 0 && ok && owners[i-1] != nil {
		e := owners[i-1]
		e.s.handle(e.s.admit(v.Interface().(linkcore.Event)))
	}
}

// checkView requires the View mirrored for other goroutines to equal the
// core's after every step.
func (p *pump) checkView(e *pumpEnd) {
	p.t.Helper()
	v := e.s.core.View()
	if e.s.retired.Load() != v.Retired || e.s.established.Load() != v.Established || e.s.retainUntil.Load() != v.RetainUntil {
		p.t.Fatalf("mirrored round state differs from the core: %+v", v)
	}
	for i := range 2 {
		if e.s.generation[i].Load() != v.Generation[i] || e.s.business[i].Load() != v.Business[i] || e.s.current[i].Load() != v.PC[i] {
			p.t.Fatalf("mirrored use %d differs from the core: %+v", i+1, v)
		}
	}
}

func (e *pumpEnd) bothReady() bool { return e.ready[1] != nil && e.ready[2] != nil }
