package daemon

import (
	"context"
	"crypto/ed25519"
	"fmt"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/codex"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/eventlog"
	"github.com/Lewin671/remote-agent/internal/relay"
	"github.com/Lewin671/remote-agent/internal/remoteclient"
	"github.com/Lewin671/remote-agent/internal/server"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Lewin671/remote-agent/internal/tmux"
)

// A real pane, not a canned snapshot: opening and resizing must recover
// output produced before any terminal client connected.
func TestTerminalSnapshotKeepsInitialHistorySmall(t *testing.T) {
	tc := &tmux.Client{Socket: fmt.Sprintf("ra-history-%d-%d", os.Getpid(), time.Now().UnixNano())}
	if _, err := tc.CheckVersion(); err != nil {
		t.Skip(err)
	}
	t.Cleanup(func() { _, _ = tc.Run("kill-server") })
	err := tc.NewSession(tmux.NewSessionOptions{Name: "history", Width: 80, Height: 10,
		Argv: []string{"/bin/sh", "-c", `i=1; while [ "$i" -le 2500 ]; do printf 'history-%03d\n' "$i"; i=$((i+1)); done; printf '\033[31mcolored-tail\033[0m\n'; exec cat`}})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		screen, err := tc.Capture("history", false)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(screen, "colored-tail") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pane never finished output: %q", screen)
		}
		time.Sleep(20 * time.Millisecond)
	}
	d := New(Config{Tmux: tc})
	d.sessions["history"] = &session{meta: meta{Tmux: "history"}}
	stream, err := d.OpenTerminal("history")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { stream.Close() })
	for _, rows := range []int{10, 15} {
		if _, err := tc.Run("resize-window", "-t", "=history:", "-y", fmt.Sprint(rows)); err != nil {
			t.Fatal(err)
		}
		snapshot, err := stream.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"history-2490", "history-2500"} {
			if !strings.Contains(string(snapshot.Data), want) {
				t.Errorf("%d rows: missing %s", rows, want)
			}
		}
		if strings.Contains(string(snapshot.Data), "history-001") {
			t.Error("opening sent old history eagerly")
		}
		if snapshot.Rows != int32(rows) {
			t.Errorf("rows = %d", snapshot.Rows)
		}
	}
	first, err := stream.History(&pb.TerminalHistoryRequest{RequestId: "first"})
	if err != nil || first.Error != "" || first.NextCursor == "" {
		t.Fatalf("capture failed: %v %v", first, err)
	}
	// History keeps colours so clients can draw it like the live screen.
	if !strings.Contains(strings.Join(first.Lines, "\n"), "\x1b[31mcolored-tail") {
		t.Errorf("history lost its SGR colours: %q", first.Lines[len(first.Lines)-3:])
	}
	file := stream.history.file
	if _, err := os.Stat(file.Name()); !os.IsNotExist(err) {
		t.Fatal("history plaintext file is still linked")
	}
	stat, err := file.Stat()
	if err != nil || stat.Mode().Perm() != 0600 {
		t.Fatalf("history file permissions: %v %v", stat, err)
	}
	next, err := stream.History(&pb.TerminalHistoryRequest{RequestId: "new"})
	if err != nil || next.Error != "" {
		t.Fatalf("recapture failed: %v %v", next, err)
	}
	if _, err := file.Stat(); err == nil {
		t.Fatal("recapture leaked the old file")
	}
	stale, _ := stream.History(&pb.TerminalHistoryRequest{RequestId: "stale", Cursor: first.NextCursor})
	if stale.Error == "" {
		t.Fatal("old capture cursor accepted")
	}
	file = stream.history.file
	stream.Close()
	if _, err := file.Stat(); err == nil {
		t.Fatal("closing the stream leaked its history file")
	}

}

// The same snapshot crosses a local encrypted relay. Input travels back to
// a real shell/cat process, and reconnecting recovers its output as history.
func TestTerminalHistoryThroughRelay(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	tc := &tmux.Client{Socket: fmt.Sprintf("ra-history-relay-%d-%d", os.Getpid(), time.Now().UnixNano())}
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
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	authority, err := remoteclient.NewAuthority(ctx, http.URL, "history-test")
	if err != nil {
		t.Fatal(err)
	}
	_, med, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	mx, _, err := deviceauth.NewXKey()
	if err != nil {
		t.Fatal(err)
	}
	machine, err := authority.Enroll(ctx, med, mx, deviceauth.Machine)
	if err != nil {
		t.Fatal(err)
	}
	account := &relay.Account{Server: authority.Server, Root: authority.Root, DeviceSeed: med.Seed(), DeviceID: machine.ID,
		Security: &relay.SecurityConfig{Pin: authority.Pin, XPrivate: mx}}
	if err := machine.Endpoint.Save(filepath.Join(os.Getenv("REMOTE_AGENT_HOME"), "security-v3.db")); err != nil {
		t.Fatal(err)
	}
	if err := account.Save(); err != nil {
		t.Fatal(err)
	}
	r := relay.New(historyBackend{d})
	finished := make(chan struct{})
	go func() { defer close(finished); r.Run(ctx) }()
	defer func() { cancel(); <-finished }()
	for !r.Connected() {
		select {
		case <-ctx.Done():
			t.Fatal("authority stream did not become ready")
		case <-time.After(10 * time.Millisecond):
		}
	}
	_, ced, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	cx, _, err := deviceauth.NewXKey()
	if err != nil {
		t.Fatal(err)
	}
	enrolled, err := authority.Enroll(ctx, ced, cx, deviceauth.Client)
	if err != nil {
		t.Fatal(err)
	}
	client := enrolled.Client(authority)

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
		if !strings.Contains(data, "history-2500") || strings.Contains(data, "history-001") {
			terminal.Close()
			t.Fatalf("history missing from encrypted snapshot: %q", data)
		}
		var all []string
		cursor := ""
		for pageNumber := 0; ; pageNumber++ {
			id := fmt.Sprintf("page-%d", pageNumber)
			if err := terminal.Send(ctx, &pb.TerminalFrame{Kind: &pb.TerminalFrame_HistoryRequest{HistoryRequest: &pb.TerminalHistoryRequest{RequestId: id, Cursor: cursor}}}); err != nil {
				t.Fatal(err)
			}
			var page *pb.TerminalHistoryPage
			for page == nil {
				frame, err := terminal.Read(ctx)
				if err != nil {
					t.Fatal(err)
				}
				page = frame.GetHistoryPage()
			}
			if page.RequestId != id || page.Error != "" || len(page.Lines) > historyPageLines {
				t.Fatalf("invalid page: %v", page)
			}
			all = append(append([]string{}, page.Lines...), all...)
			if attempt == 0 && pageNumber == 0 {
				if err := terminal.Send(ctx, &pb.TerminalFrame{Kind: &pb.TerminalFrame_Input{Input: []byte("live-history-check\r")}}); err != nil {
					t.Fatal(err)
				}
				var live strings.Builder
				for !strings.Contains(live.String(), "live-history-check") {
					next, err := terminal.Read(ctx)
					if err != nil {
						t.Fatal(err)
					}
					live.Write(next.GetOutput())
				}
				if err := terminal.Send(ctx, &pb.TerminalFrame{Kind: &pb.TerminalFrame_Resize{Resize: &pb.TerminalResize{Cols: 70, Rows: 12, Fit: true}}}); err != nil {
					t.Fatal(err)
				}
			}
			cursor = page.NextCursor
			if cursor == "" {
				break
			}
		}
		numbered := make([]string, 0)
		for _, line := range all {
			if strings.HasPrefix(line, "history-") {
				numbered = append(numbered, line)
			}
		}
		if len(numbered) != 2500 || numbered[0] != "history-001" || numbered[2499] != "history-2500" {
			t.Fatalf("paged history missing, duplicated, or out of order: %d lines", len(numbered))
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
