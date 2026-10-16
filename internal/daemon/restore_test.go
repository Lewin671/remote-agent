package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/paths"
	"github.com/Lewin671/remote-agent/internal/tmux"
)

// restoreHarness is a daemon over a home directory of saved sessions and a
// stand-in for tmux that lists the ones named in alive.
type restoreHarness struct {
	t     *testing.T
	d     *Daemon
	alive string // the file tmux's session list is read from
}

func newRestoreHarness(t *testing.T) *restoreHarness {
	t.Helper()
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	bin := t.TempDir()
	alive := filepath.Join(bin, "sessions")
	script := "#!/bin/sh\nshift 2\ncase \"$1\" in\n" +
		"list-sessions) cat '" + alive + "' 2>/dev/null; exit 0 ;;\n" +
		"has-session) grep -q \"^${3#=}\t\" '" + alive + "' 2>/dev/null ;;\n" +
		"capture-pane) printf '\\033[1m›\\033[0m \\033[2mAsk Codex to do anything\\033[0m\\n  GPT-6-Sol medium · /w\\n' ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	d := New(Config{Tmux: &tmux.Client{Bin: filepath.Join(bin, "tmux"), Socket: "ra-test"},
		HistoryPath: filepath.Join(t.TempDir(), "history.jsonl"), PollInterval: time.Millisecond, ScreenInterval: time.Hour})
	d.ctx = ctx
	t.Cleanup(func() {
		cancel()
		d.mu.Lock()
		all := make([]*session, 0, len(d.sessions))
		for _, s := range d.sessions {
			all = append(all, s)
		}
		d.mu.Unlock()
		for _, s := range all {
			s.mu.Lock()
			started := s.stop != nil
			s.mu.Unlock()
			if started {
				<-s.done
			}
		}
	})
	return &restoreHarness{t: t, d: d, alive: alive}
}

// save writes a session as a previous daemon left it and returns its
// directory; alive says whether its tmux session still runs.
func (r *restoreHarness) save(m meta, alive bool) string {
	r.t.Helper()
	dir := paths.SessionDir(m.ID)
	if err := paths.EnsureDir(dir); err != nil {
		r.t.Fatal(err)
	}
	if m.Cwd == "" {
		m.Cwd = dir
	}
	m.Tmux, m.CreatedAt = "ra-"+m.ID[:8], time.Now()
	if err := m.save(dir); err != nil {
		r.t.Fatal(err)
	}
	if alive {
		f, err := os.OpenFile(r.alive, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			r.t.Fatal(err)
		}
		fmt.Fprintf(f, "%s\t%s\t1\n", m.Tmux, m.ID)
		f.Close()
	}
	return dir
}

func (r *restoreHarness) session(id string) *session {
	r.d.mu.Lock()
	defer r.d.mu.Unlock()
	return r.d.sessions[id]
}

// Sessions restored first already run their loops, which look at the
// session list (to trim the image cache) while later ones are added to it.
func TestRestoreRegistersSessionsUnderTheLock(t *testing.T) {
	r := newRestoreHarness(t)
	image := `{"type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"c%d","output":[{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}}`
	for i := range 64 {
		id := fmt.Sprintf("%08x00000000", i+1)
		dir := paths.SessionDir(id)
		rollout := filepath.Join(dir, "rollout.jsonl")
		r.save(meta{ID: id, RolloutPath: rollout}, true)
		var lines []string
		for j := range 2 {
			lines = append(lines, fmt.Sprintf(image, j))
		}
		if err := os.WriteFile(rollout, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.d.restore(); err != nil {
		t.Fatal(err)
	}
	if n := len(r.d.list(true)); n != 64 {
		t.Fatalf("restored %d sessions", n)
	}
	last := r.session(fmt.Sprintf("%08x00000000", 64))
	eventually(t, "transcripts read", func() bool {
		_, off := last.log.Checkpoint()
		return off > 0
	})
}

// The CLI went on and exited while the daemon was down: what it wrote to
// its transcript meanwhile is read before the session is marked exited, as
// nothing reads an exited session's transcript later.
func TestRestoreReadsTheTranscriptOfAGoneSession(t *testing.T) {
	r := newRestoreHarness(t)
	id := "00000000000000aa"
	rollout := filepath.Join(paths.SessionDir(id), "rollout.jsonl")
	r.save(meta{ID: id, RolloutPath: rollout}, false)
	lines := []string{
		taskStarted,
		`{"timestamp":"2026-09-29T10:00:01Z","type":"event_msg","payload":{"type":"item_completed","turn_id":"t1","item":{"type":"AgentMessage","id":"m1","content":[{"text":"done while you were away"}]}}}`,
		`{"timestamp":"2026-09-29T10:00:02Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"t1"}}`,
	}
	if err := os.WriteFile(rollout, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.d.restore(); err != nil {
		t.Fatal(err)
	}
	s := r.session(id)
	evs, _, err := s.log.Since(0)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, e := range evs {
		switch {
		case e.GetAgentMessage() != nil:
			kinds = append(kinds, "message")
		case e.GetTurnEnded() != nil:
			kinds = append(kinds, "end")
		case e.GetSessionState().GetStatus() == pb.SessionStatus_SESSION_STATUS_EXITED:
			kinds = append(kinds, "exited")
		}
	}
	if strings.Join(kinds, " ") != "message end exited" {
		t.Fatalf("events %v", kinds)
	}
	if info := s.info(); info.Status != pb.SessionStatus_SESSION_STATUS_EXITED {
		t.Fatalf("status %v", info.Status)
	}
}
