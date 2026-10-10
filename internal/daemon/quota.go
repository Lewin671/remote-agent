package daemon

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/claude"
	"github.com/Lewin671/remote-agent/internal/paths"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Account usage limits (architecture "账户额度"). Codex reports them in its
// rollouts; Claude Code only in the input of its status line, which
// remote-agent's status line command passes on. Nothing here asks the
// model or any service for them. Claude Code's limits are kept per login
// account (a fingerprint, never the account's IDs), in quota.json, so a
// restart shows the last record; a record of another account than the
// one logged in now is dropped. Limits never reach the log.

// quotaRefresh bounds how often an unchanged report renews observed_ms,
// which would otherwise change the machine metadata every few seconds.
const quotaRefresh = time.Minute

// claudeQuotas are the Claude Code limits by account (or "session:<id>"
// when the account is unknown).
type claudeQuotas map[string]*pb.AgentQuota

// quotaReport is the last status line of a session: the windows it showed
// and the account logged in then.
type quotaReport struct {
	account string
	windows []*pb.QuotaWindow
}

func quotaPath() string { return filepath.Join(paths.Dir(), "quota.json") }

// loadQuotas reads the saved Claude Code limits, keeping only the logged
// in account's.
func (d *Daemon) loadQuotas() {
	b, err := os.ReadFile(quotaPath())
	if err != nil {
		return
	}
	var saved map[string]json.RawMessage
	if json.Unmarshal(b, &saved) != nil {
		return
	}
	account := d.claudeAccount()
	q := claudeQuotas{}
	for key, raw := range saved {
		e := &pb.AgentQuota{}
		if protojson.Unmarshal(raw, e) != nil || e.Account == "" || e.Account != account {
			continue // an unknown or another account's record is not reused
		}
		q[key] = e
	}
	d.mu.Lock()
	d.quotas = q
	d.mu.Unlock()
}

func (d *Daemon) saveQuotasLocked() {
	out := map[string]json.RawMessage{}
	for key, e := range d.quotas {
		if e.Account == "" {
			continue
		}
		b, err := protojson.Marshal(e)
		if err == nil {
			out[key] = b
		}
	}
	b, _ := json.Marshal(out)
	if err := writeFileSync(quotaPath(), b); err != nil {
		slog.Warn("save account limits", "err", errClass(err))
	}
}

func (d *Daemon) claudeAccount() string {
	a, ok := d.agents[pb.Agent_AGENT_CLAUDE].(*claude.Adapter)
	if !ok {
		return ""
	}
	fp, _, _ := a.Account()
	return fp
}

// statusLine records the limits in a Claude Code status line input.
func (d *Daemon) statusLine(sessionID string, raw json.RawMessage, now time.Time) {
	var in struct {
		RateLimits map[string]*struct {
			UsedPercentage *float64 `json:"used_percentage"`
			ResetsAt       int64    `json:"resets_at"`
		} `json:"rate_limits"`
	}
	if json.Unmarshal(raw, &in) != nil || len(in.RateLimits) == 0 {
		return // no limits yet: before the first reply, or none for this login
	}
	e := &pb.AgentQuota{Agent: pb.Agent_AGENT_CLAUDE, State: pb.QuotaState_QUOTA_STATE_OK, ObservedMs: now.UnixMilli()}
	names := make([]string, 0, len(in.RateLimits))
	for name := range in.RateLimits {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		w := in.RateLimits[name]
		if w == nil {
			continue
		}
		qw := &pb.QuotaWindow{Name: name, UsedPercent: w.UsedPercentage}
		if w.ResetsAt > 0 {
			qw.ResetsAtMs = w.ResetsAt * 1000
		}
		switch name {
		case "five_hour":
			qw.WindowMinutes = 300
		case "seven_day":
			qw.WindowMinutes = 7 * 24 * 60
		}
		e.Windows = append(e.Windows, qw)
	}
	if a, ok := d.agents[pb.Agent_AGENT_CLAUDE].(*claude.Adapter); ok {
		var sub bool
		e.Account, e.Plan, sub = a.Account()
		if sub {
			e.Login = pb.QuotaLogin_QUOTA_LOGIN_SUBSCRIPTION
		}
	}
	key := "account:" + e.Account
	if e.Account == "" {
		key, e.SourceSessionId = "session:"+sessionID, sessionID
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.quotas == nil {
		d.quotas = claudeQuotas{}
	}
	dropped := false
	if e.Account != "" {
		// Another account logged in: the old one's record goes.
		for k, old := range d.quotas {
			if old.Account != "" && old.Account != e.Account {
				delete(d.quotas, k)
				dropped = true
			}
		}
	}
	// The status line does not say whose limits it shows, and a session
	// repeats those of its last reply until the next one. The same
	// windows it reported under another login are that login's.
	if d.reported == nil {
		d.reported = map[string]quotaReport{}
	}
	if last, ok := d.reported[sessionID]; ok && last.account != e.Account && proto.Equal(&pb.AgentQuota{Windows: last.windows}, &pb.AgentQuota{Windows: e.Windows}) {
		if dropped {
			d.saveQuotasLocked()
		}
		return
	}
	d.reported[sessionID] = quotaReport{account: e.Account, windows: e.Windows}
	if old := d.quotas[key]; old != nil {
		reported := e.Windows
		e.Windows = newestWindows(old.Windows, reported)
		same := proto.Equal(&pb.AgentQuota{Windows: old.Windows, Plan: old.Plan, Login: old.Login}, &pb.AgentQuota{Windows: e.Windows, Plan: e.Plan, Login: e.Login})
		// A report that is behind the record does not renew its time.
		current := proto.Equal(&pb.AgentQuota{Windows: reported}, &pb.AgentQuota{Windows: e.Windows})
		if same && (!current || now.Sub(time.UnixMilli(old.ObservedMs)) < quotaRefresh) {
			return
		}
	}
	d.quotas[key] = e
	d.saveQuotasLocked()
}

// newestWindows merges a status line's windows into the recorded ones. A
// session's status line repeats the limits of that session's last reply,
// with no time, so an idle session keeps reporting old numbers next to a
// busy one's. Per window the later reset wins, and with the same reset
// the higher usage, which only grows until the reset. Without reset times
// the report wins. A window only the record has is kept: the client marks
// it once its reset has passed.
func newestWindows(old, reported []*pb.QuotaWindow) []*pb.QuotaWindow {
	byName := map[string]*pb.QuotaWindow{}
	for _, w := range old {
		byName[w.Name] = w
	}
	for _, w := range reported {
		o := byName[w.Name]
		if o == nil || w.ResetsAtMs > o.ResetsAtMs ||
			w.ResetsAtMs == o.ResetsAtMs && (w.ResetsAtMs == 0 || o.UsedPercent == nil || w.GetUsedPercent() > o.GetUsedPercent()) {
			byName[w.Name] = w
		}
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]*pb.QuotaWindow, 0, len(names))
	for _, name := range names {
		out = append(out, byName[name])
	}
	return out
}

// quotaEntries lists the limits for the machine metadata. Callers hold
// d.mu.
func (d *Daemon) quotaEntriesLocked() []*pb.AgentQuota {
	var out []*pb.AgentQuota
	if cx := d.agents[pb.Agent_AGENT_CODEX]; cx != nil && cx.Bin() != "" {
		e := &pb.AgentQuota{Agent: pb.Agent_AGENT_CODEX, State: pb.QuotaState_QUOTA_STATE_WAITING}
		if u := d.limits; u != nil {
			e.State, e.Plan, e.ObservedMs = pb.QuotaState_QUOTA_STATE_OK, u.PlanType, d.limitsAt.UnixMilli()
			for _, w := range []struct {
				name string
				l    *pb.RateLimit
			}{{"primary", u.PrimaryLimit}, {"secondary", u.SecondaryLimit}} {
				if w.l == nil {
					continue
				}
				used := w.l.UsedPercent
				qw := &pb.QuotaWindow{Name: w.name, UsedPercent: &used, WindowMinutes: w.l.WindowMinutes}
				if w.l.ResetsAt > 0 {
					qw.ResetsAtMs = w.l.ResetsAt * 1000
				}
				e.Windows = append(e.Windows, qw)
			}
		}
		out = append(out, e)
	}
	if cl, ok := d.agents[pb.Agent_AGENT_CLAUDE].(*claude.Adapter); ok && cl.Bin() != "" {
		keys := make([]string, 0, len(d.quotas))
		for k := range d.quotas {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			out = append(out, proto.CloneOf(d.quotas[k]))
		}
		if len(keys) == 0 {
			e := &pb.AgentQuota{Agent: pb.Agent_AGENT_CLAUDE, State: pb.QuotaState_QUOTA_STATE_WAITING}
			if cl.ManagedStatusLine("") {
				e.State, e.Detail = pb.QuotaState_QUOTA_STATE_UNSUPPORTED, "the status line is set by managed settings"
			}
			out = append(out, e)
		}
	}
	return out
}
