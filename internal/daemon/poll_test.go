package daemon

import (
	"context"
	"errors"
	"testing"
)

type pollTerminal struct {
	*fakeTerm
	alive bool
	err   error
	polls int
}

func (p *pollTerminal) Poll(context.Context) (string, bool, error) {
	p.polls++
	return idleScreen, p.alive, p.err
}
func (*pollTerminal) Alive() bool              { panic("poll must not invoke a separate liveness check") }
func (*pollTerminal) Capture() (string, error) { panic("poll must not invoke a separate capture") }

func TestScreenPollDoesNotExitOnTransientFailure(t *testing.T) {
	h := newHarness(t, Config{})
	term := &pollTerminal{fakeTerm: h.term, alive: true, err: errors.New("temporary capture failure")}
	h.s.term = term
	if !h.s.checkScreen() || h.s.meta.Exited {
		t.Fatal("capture failure ended session")
	}
	term.err = nil
	if !h.s.checkScreen() || h.s.meta.Exited {
		t.Fatal("live screen ended session")
	}
	term.alive = false
	if h.s.checkScreen() || !h.s.meta.Exited {
		t.Fatal("confirmed missing session not finalized")
	}
	if term.polls != 3 {
		t.Fatalf("poll count=%d", term.polls)
	}
}
