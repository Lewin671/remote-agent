package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/i18n"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Account usage limits (architecture "账户额度"). Codex reports them in its
// rollouts; Claude Code only in the input of its status line, which
// remote-agent's status line command passes on. Nothing here asks the
// model or any service for them. Provider limits are kept per login
// account (a fingerprint, never the account's IDs), in quota.json, so a
// restart shows the last record; a record of another account than the
// one logged in now is dropped. Limits never reach the log.

// quotaStore separates providers even when account fingerprints collide.
type quotaStore map[string]*pb.AgentQuota

func quotaKey(q *pb.AgentQuota) string {
	id := "unknown"
	if spec, ok := FindAgentByKind(q.Agent); ok {
		id = spec.ID
	}
	if q.Account != "" {
		return id + ":account:" + q.Account
	}
	return id + ":session:" + q.SourceSessionId
}

// quotaReport is the last status line of a session: the windows it showed
// and the account logged in then.
type quotaReport struct {
	account string
	windows []*pb.QuotaWindow
}

func (d *Daemon) quotaPath() string { return filepath.Join(d.dir, "quota.json") }

// loadQuotas reads the saved CLI limits, keeping only the logged
// in account's.
func (d *Daemon) loadQuotas() {
	b, err := os.ReadFile(d.quotaPath())
	if err != nil {
		return
	}
	var saved map[string]json.RawMessage
	if json.Unmarshal(b, &saved) != nil {
		return
	}
	q := quotaStore{}
	for key, raw := range saved {
		e := &pb.AgentQuota{}
		if protojson.Unmarshal(raw, e) != nil || e.Account == "" || key != quotaKey(e) {
			continue // an unknown account's record is not reused
		}
		a, ok := d.byKind(e.Agent)
		if !ok {
			continue
		}
		if cl, ok := a.(agent.StatusLineAccount); ok {
			if fp, _, _ := cl.Account(); fp == "" || fp != e.Account {
				continue
			}
		} else {
			continue
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
	if err := writeFileSync(d.quotaPath(), b); err != nil {
		slog.Warn("save account limits", "err", errClass(err))
	}
}

// statusLine dispatches by the session's adapter, never by fields that happen
// to occur in multiple CLIs' JSON contracts.
func (d *Daemon) statusLine(sessionID string, raw json.RawMessage, now time.Time) {
	d.statusLineContext(context.Background(), sessionID, raw, now)
}
func (d *Daemon) statusLineContext(ctx context.Context, sessionID string, raw json.RawMessage, now time.Time) {
	session, err := d.session(sessionID)
	if err != nil {
		return
	}
	source, ok := session.ag.(agent.StatusLineSource)
	if !ok {
		return
	}
	report, err := source.ParseStatusLine(raw, session.dir)
	if ctx.Err() != nil {
		return
	}
	d.mu.Lock()
	if d.quotaErrors == nil {
		d.quotaErrors = map[pb.Agent]string{}
	}
	if err != nil {
		d.quotaErrors[session.ag.Kind()] = i18n.QuotaReadFailed("zh-CN")
	} else {
		delete(d.quotaErrors, session.ag.Kind())
	}
	d.mu.Unlock()
	if err != nil {
		return
	}
	session.observeEmptyIdentity(ctx, report, now)
	if session.observeConversation(ctx, report.ConversationID, report.TranscriptPath, now) {
		session.recordStatusLine(report, now)
	}
	if report.Quota != nil && ctx.Err() == nil {
		d.recordQuotaWithPolicy(sessionID, report.Quota, now, report.QuotaMonotonic)
	}
}

func (d *Daemon) recordQuota(sessionID string, e *pb.AgentQuota, now time.Time) {
	d.recordQuotaWithPolicy(sessionID, e, now, true)
}
func (d *Daemon) recordQuotaWithPolicy(sessionID string, e *pb.AgentQuota, now time.Time, monotonic bool) {
	e.ObservedMs = now.UnixMilli()
	if e.Account == "" {
		e.SourceSessionId = sessionID
	}
	key := quotaKey(e)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.quotas == nil {
		d.quotas = quotaStore{}
	}
	dropped := false
	if e.Account != "" {
		// Another account logged in: the old one's record goes.
		for k, old := range d.quotas {
			if old.Agent == e.Agent && old.Account != "" && old.Account != e.Account {
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
		if now.UnixMilli() < old.ObservedMs {
			return
		}
		if monotonic {
			e.Windows = newestWindows(old.Windows, reported)
		} else {
			e.Windows = latestWindows(old.Windows, reported)
		}
		same := proto.Equal(&pb.AgentQuota{Windows: old.Windows, Plan: old.Plan, Login: old.Login}, &pb.AgentQuota{Windows: e.Windows, Plan: e.Plan, Login: e.Login})
		// Status lines repeat the last reply's limits without a source
		// timestamp. Re-reading identical data cannot prove it is fresh,
		// even when no other session has reported higher usage.
		if same {
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

// latestWindows allows a provider's same-window usage to fall (refunds or
// credit recovery). Older reset cycles cannot overwrite a newer cycle, and
// omitted windows/fields retain their last valid observation.
func latestWindows(old, reported []*pb.QuotaWindow) []*pb.QuotaWindow {
	byName := map[string]*pb.QuotaWindow{}
	for _, w := range old {
		byName[w.Name] = w
	}
	for _, w := range reported {
		o := byName[w.Name]
		if o != nil && w.ResetsAtMs > 0 && o.ResetsAtMs > w.ResetsAtMs {
			continue
		}
		copy := proto.CloneOf(w)
		if o != nil {
			if copy.ResetsAtMs == 0 {
				copy.ResetsAtMs = o.ResetsAtMs
			}
			if copy.UsedPercent == nil && copy.ResetsAtMs == o.ResetsAtMs {
				copy.UsedPercent = o.UsedPercent
			}
			if copy.WindowMinutes == 0 {
				copy.WindowMinutes = o.WindowMinutes
			}
		}
		byName[w.Name] = copy
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []*pb.QuotaWindow
	for _, name := range names {
		out = append(out, byName[name])
	}
	return out
}

// quotaEntries lists the limits for the machine metadata. Callers hold
// d.mu.
func (d *Daemon) quotaEntriesLocked() []*pb.AgentQuota {
	var out []*pb.AgentQuota
	for _, ag := range d.adapterList() {
		if ag.Bin() == "" {
			continue
		}
		if cl, ok := ag.(agent.StatusLineAccount); ok {
			var agentQuotas []*pb.AgentQuota
			keys := make([]string, 0, len(d.quotas))
			for k := range d.quotas {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if d.quotas[k].Agent == ag.Kind() {
					q := proto.CloneOf(d.quotas[k])
					if detail := d.quotaErrors[ag.Kind()]; detail != "" {
						q.State, q.Detail = pb.QuotaState_QUOTA_STATE_FAILED, detail
					}
					if order, ok := ag.(agent.QuotaWindowOrder); ok {
						order.OrderQuotaWindows(q.Windows)
					}
					agentQuotas = append(agentQuotas, q)
				}
			}
			if len(agentQuotas) > 0 {
				out = append(out, agentQuotas...)
			} else {
				e := &pb.AgentQuota{Agent: ag.Kind(), State: pb.QuotaState_QUOTA_STATE_WAITING}
				if detail := d.quotaErrors[ag.Kind()]; detail != "" {
					e.State, e.Detail = pb.QuotaState_QUOTA_STATE_FAILED, detail
				}
				if cl.ManagedStatusLine("") {
					e.State, e.Detail = pb.QuotaState_QUOTA_STATE_UNSUPPORTED, i18n.QuotaManaged("zh-CN")
				}
				out = append(out, e)
			}
		} else {
			e := &pb.AgentQuota{Agent: ag.Kind(), State: pb.QuotaState_QUOTA_STATE_WAITING}
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
	}
	return out
}
