package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/updater"
)

// Keep the decision next to the state it describes. The updater must not
// reconstruct readiness from a client-facing session summary.
func (d *Daemon) updateBusyReason() string {
	d.mu.Lock()
	starting := len(d.starting) > 0
	all := make([]*session, 0, len(d.sessions))
	for _, s := range d.sessions {
		all = append(all, s)
	}
	d.mu.Unlock()
	if starting {
		return "session starting"
	}
	for _, s := range all {
		s.mu.Lock()
		if s.status == pb.SessionStatus_SESSION_STATUS_EXITED {
			s.mu.Unlock()
			continue
		}
		busy := s.running || s.driving || s.queueLost || len(s.approvals) > 0 || len(s.questions) > 0 || s.planOpen || len(s.unproven) > 0 || len(s.recent) > 0 || s.waitReason != "" || s.queue == nil || len(s.queue.Queue) > 0 || len(s.queue.Held) > 0 || (s.status != pb.SessionStatus_SESSION_STATUS_IDLE && s.status != pb.SessionStatus_SESSION_STATUS_EXITED)
		s.mu.Unlock()
		if busy {
			return "session has active work or pending interaction"
		}
	}
	d.scheduleMu.Lock()
	busy := len(d.lateRuns) > 0
	for _, t := range d.tasks {
		if t.Active != nil {
			busy = true
			break
		}
	}
	d.scheduleMu.Unlock()
	if busy {
		return "scheduled work is active or unconfirmed"
	}
	if d.uploads.updateBusy() {
		return "image upload is pending"
	}
	return ""
}

func (u *uploads) updateBusy() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	entries, err := os.ReadDir(filepath.Join(u.dir, "partial"))
	if os.IsNotExist(err) {
		return false
	}
	if err != nil {
		return true
	}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(u.dir, "partial", e.Name()))
		if err != nil {
			return true
		}
		var t transfer
		if json.Unmarshal(b, &t) != nil {
			return true
		}
		if t.Session == "" && time.Now().UnixMilli() < t.expiresMs() {
			return true
		}
	}
	return false
}

func (d *Daemon) observeUpdateIdle() (string, time.Time) {
	d.updateMu.Lock()
	defer d.updateMu.Unlock()
	reason := d.updateBusyReason()
	if reason != "" {
		d.updateIdleSince = time.Time{}
	} else if d.updateIdleSince.IsZero() {
		d.updateIdleSince = time.Now()
	}
	return reason, d.updateIdleSince
}

func (d *Daemon) watchUpdateIdle(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		d.observeUpdateIdle()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (d *Daemon) updateHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	h := updater.Build()
	h.Executable = d.cfg.Exe
	reason, since := d.observeUpdateIdle()
	h.Busy = reason != ""
	h.BusyReason = reason
	h.IdleSince = since
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(h)
}
