package tmux

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestVersionAtLeast(t *testing.T) {
	cases := []struct {
		v, min string
		want   bool
	}{
		{"tmux 3.7c", "3.2", true},
		{"tmux 3.2", "3.2", true},
		{"tmux 3.1c", "3.2", false},
		{"tmux 2.9a", "3.2", false},
		{"tmux next-4.0", "3.2", true},
		{"tmux master", "3.2", false},
	}
	for _, c := range cases {
		if got := VersionAtLeast(c.v, c.min); got != c.want {
			t.Errorf("VersionAtLeast(%q, %q) = %v, want %v", c.v, c.min, got, c.want)
		}
	}
}

// TestSessionCommands runs against a real tmux server on a private socket.
func TestSessionCommands(t *testing.T) {
	t.Parallel()
	c := &Client{Socket: "ra-test-" + randSuffix()}
	if _, err := c.CheckVersion(); err != nil {
		t.Skip(err)
	}
	defer c.run("kill-server")
	if infos, err := c.ListSessions(); err != nil || len(infos) != 0 {
		t.Fatalf("list without server: %v %v", infos, err)
	}
	err := c.NewSession(NewSessionOptions{
		Name: "ra-x", Dir: t.TempDir(), Width: 80, Height: 20,
		Env:  map[string]string{"RA_TEST": "hello world"},
		Argv: []string{"/bin/sh", "-c", `printf '%s\n' "$RA_TEST"; sleep 30`},
	})
	if err != nil {
		t.Fatal(err)
	}
	if limit, err := c.Display("ra-x", "#{history_limit}"); err != nil || limit != strconv.Itoa(HistoryLines) {
		t.Fatalf("history limit = %q: %v", limit, err)
	}
	// A second session whose name has ra-x as a prefix must not be hit by
	// commands aimed at ra-x.
	if err := c.NewSession(NewSessionOptions{Name: "ra-xy", Argv: []string{"sleep", "30"}}); err != nil {
		t.Fatal(err)
	}
	if err := c.SetSessionOption("ra-x", "@ra_session_id", "abc"); err != nil {
		t.Fatal(err)
	}
	infos, err := c.ListSessions()
	if err != nil || len(infos) != 2 {
		t.Fatalf("list: %v %v", infos, err)
	}
	var found bool
	for _, i := range infos {
		if i.Name == "ra-x" && i.SessionID == "abc" && i.PanePID > 0 {
			found = true
		}
		if i.Name == "ra-xy" && i.SessionID != "" {
			t.Errorf("option leaked to ra-xy: %+v", i)
		}
	}
	if !found {
		t.Fatalf("ra-x not listed with its option: %+v", infos)
	}
	var screen string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		screen, _ = c.Capture("ra-x", false)
		if strings.Contains(screen, "hello world") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !strings.Contains(screen, "hello world") {
		t.Fatalf("capture: %q", screen)
	}
	if at, err := c.ClientActivity(context.Background(), "ra-x"); err != nil || !at.IsZero() {
		t.Fatalf("activity without clients = %v: %v", at, err)
	}
	// A control-mode client, like a client's terminal view.
	attach := exec.Command(c.bin(), c.Command("-C", "attach-session", "-t", "=ra-x")[1:]...)
	stdin, _ := attach.StdinPipe()
	if err := attach.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { stdin.Close(); attach.Wait() }()
	var at time.Time
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && at.IsZero(); time.Sleep(20 * time.Millisecond) {
		at, err = c.ClientActivity(context.Background(), "ra-x")
	}
	if err != nil || time.Since(at) > time.Minute {
		t.Fatalf("activity with a client = %v: %v", at, err)
	}
	if err := c.KillSession("ra-x"); err != nil {
		t.Fatal(err)
	}
	if c.HasSession("ra-x") || !c.HasSession("ra-xy") {
		t.Fatal("kill-session hit the wrong session")
	}
}

func randSuffix() string {
	// Tests of several packages (and checkouts) run at the same time on one
	// machine; the PID keeps their private tmux servers apart even when two
	// processes start in the same nanosecond.
	return strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
}
