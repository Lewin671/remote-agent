package daemon

import (
	"context"
	"fmt"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/codex"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"github.com/Lewin671/remote-agent/internal/eventlog"
	"github.com/Lewin671/remote-agent/internal/relay"
	"github.com/Lewin671/remote-agent/internal/remoteclient"
	"github.com/Lewin671/remote-agent/internal/server"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Lewin671/remote-agent/internal/tmux"
)

// A real pane, not a canned snapshot: opening and resizing must recover
// output produced before any terminal client connected.
func TestTerminalSnapshotIncludesTmuxHistory(t *testing.T) {
	tc := &tmux.Client{Socket: fmt.Sprintf("ra-history-%d", time.Now().UnixNano())}
	if _, err := tc.CheckVersion(); err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { _, _ = tc.Run("kill-server") })
	err := tc.NewSession(tmux.NewSessionOptions{Name: "history", Width: 80, Height: 10,
		Argv: []string{"/bin/sh", "-c", `i=1; while [ "$i" -le 2500 ]; do printf 'history-%03d\n' "$i"; i=$((i+1)); done; exec cat`}})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		screen, err := tc.Capture("history", false)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(screen, "history-2500") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pane never finished output: %q", screen)
		}
		time.Sleep(20 * time.Millisecond)
	}
	stream := &TermStream{d: New(Config{Tmux: tc}), name: "history"}
	for _, rows := range []int{10, 15} {
		if _, err := tc.Run("resize-window", "-t", "=history:", "-y", fmt.Sprint(rows)); err != nil {
			t.Fatal(err)
		}
		snapshot, err := stream.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"history-001", "history-050", "history-2500"} {
			if !strings.Contains(string(snapshot.Data), want) {
				t.Errorf("%d rows: missing %s", rows, want)
			}
		}
		if snapshot.Rows != int32(rows) {
			t.Errorf("rows = %d", snapshot.Rows)
		}
	}
}

// The same snapshot crosses a local encrypted relay. Input travels back to
// a real shell/cat process, and reconnecting recovers its output as history.
func TestTerminalHistoryThroughRelay(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	tc := &tmux.Client{Socket: fmt.Sprintf("ra-history-relay-%d", time.Now().UnixNano())}
	if _, err := tc.CheckVersion(); err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { _, _ = tc.Run("kill-server") })
	if err := tc.NewSession(tmux.NewSessionOptions{Name: "history", Width: 80, Height: 10,
		Argv: []string{"/bin/sh", "-c", `i=1; while [ "$i" -le 2500 ]; do printf 'history-%03d\n' "$i"; i=$((i+1)); done; exec cat`}}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	log, err := eventlog.Open(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	d := New(Config{Tmux: tc})
	d.sessions["history"] = &session{d: d, ag: codex.New("", "", ""), dir: dir, log: log,
		queue: &queueState{}, status: pb.SessionStatus_SESSION_STATUS_IDLE,
		meta: meta{ID: "history", Tmux: "history", CreatedAt: time.Now()}}
	srv, err := server.New(server.Config{DBPath: filepath.Join(t.TempDir(), "relay.db"), SetupToken: "history-test"})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	http := httptest.NewServer(srv.Handler())
	defer http.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	account, err := relay.Join(ctx, http.URL, e2e.NewKey(), "history-test")
	if err != nil {
		t.Fatal(err)
	}
	client, err := remoteclient.Register(ctx, account.Secret())
	if err != nil {
		t.Fatal(err)
	}
	r := relay.New(historyBackend{d})
	finished := make(chan struct{})
	go func() { defer close(finished); r.Run(ctx) }()
	defer func() { cancel(); <-finished }()
	var listed []remoteclient.Session
	for ctx.Err() == nil {
		listed, err = client.Sessions(ctx)
		if err == nil && len(listed) == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(listed) != 1 {
		t.Fatalf("session was not synced: %v", err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		terminal, err := client.OpenTerminal(ctx, listed[0])
		if err != nil {
			t.Fatal(err)
		}
		frame, err := terminal.Read(ctx)
		if err != nil {
			terminal.Close()
			t.Fatal(err)
		}
		data := string(frame.GetSnapshot().GetData())
		if !strings.Contains(data, "history-001") || !strings.Contains(data, "history-2500") {
			terminal.Close()
			t.Fatalf("history missing from encrypted snapshot: %q", data)
		}
		if attempt == 0 {
			err = terminal.Send(ctx, &pb.TerminalFrame{Kind: &pb.TerminalFrame_Input{Input: []byte("live-history-check\r")}})
			if err != nil {
				terminal.Close()
				t.Fatal(err)
			}
			var output strings.Builder
			for !strings.Contains(output.String(), "live-history-check") {
				frame, err = terminal.Read(ctx)
				if err != nil {
					terminal.Close()
					t.Fatal(err)
				}
				output.Write(frame.GetOutput())
			}
		} else if !strings.Contains(data, "live-history-check") {
			terminal.Close()
			t.Fatal("reconnect lost the output sent through the terminal")
		}
		terminal.Close()
	}
}

type historyBackend struct{ *Daemon }

func (b historyBackend) OpenTerminal(id string) (relay.Terminal, error) {
	terminal, err := b.Daemon.OpenTerminal(id)
	if err != nil {
		return nil, err
	}
	return terminal, nil
}
func (b historyBackend) Image(sessionID, imageID string) relay.ImageUpload {
	return relay.ImageUpload{Failed: true}
}
