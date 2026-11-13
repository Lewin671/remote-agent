package relay_test

import (
	"context"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"net"
	"testing"
	"time"
)

func TestMainWSReplacementLeavesTerminalAlive(t *testing.T) {
	e := setup(t)
	e.backend.add(t, "sess1")
	e.startRelay()
	s := e.session(t, "sess1", 0)
	e.online(t, true)
	e.streamsMu.Lock()
	var main net.Conn
	for conn := range e.streams {
		main = conn
	}
	e.streamsMu.Unlock()
	if main == nil {
		t.Fatal("main WS missing")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	term, err := e.client.OpenTerminal(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	if _, err := term.Read(ctx); err != nil {
		t.Fatal(err)
	}
	e.backend.mu.Lock()
	native := e.backend.term
	e.backend.mu.Unlock()
	if err := main.Close(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "replacement WS while terminal is live", func() bool { e.streamsMu.Lock(); defer e.streamsMu.Unlock(); return len(e.streams) >= 4 })
	select {
	case <-native.done:
		t.Fatal("main WS closed the terminal")
	default:
	}
	native.out <- []byte("still live")
	frame, err := term.Read(ctx)
	if err != nil || string(frame.GetOutput()) != "still live" {
		t.Fatalf("terminal output: %v", err)
	}
	if err := term.Send(ctx, &pb.TerminalFrame{Kind: &pb.TerminalFrame_Input{Input: []byte("one")}}); err != nil {
		t.Fatal(err)
	}
	select {
	case input := <-native.input:
		if string(input) != "one" {
			t.Fatal("input changed")
		}
	case <-ctx.Done():
		t.Fatal("terminal input stopped")
	}
}
