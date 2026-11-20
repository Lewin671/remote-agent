package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/i18n"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// Account usage limits (architecture "账户额度"). Codex reports them in its
// rollouts, read from every run on this machine, not only managed sessions
// (accountLimitsLoop); Claude Code only in the input of its status line, which
// remote-agent's status line command passes on; Antigravity (agy) reports them
// both in its managed session status line and via `agy -p /usage
// --output-format json` (0-token command, never a model prompt) triggered when
// passive log-directory activity is observed or on startup if no record is
// saved. Provider limits are kept per login account (a fingerprint, never the
// account's IDs), in quota.json, so a restart shows the last record; a record
// of another account than the one logged in now is dropped. Limits and emails
// never reach the log.

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

// quotaService owns the account limits and the usage-limit state derived from
// them. It has its own lock, never held together with Daemon.mu, so recording
// a status line or building machine metadata does not contend with sessions.
type quotaService struct {
	dir      string
	adapters func() []agent.Adapter
	byKind   func(pb.Agent) (agent.Adapter, bool)

	mu     sync.Mutex
	limits *pb.TokenUsage // latest usage limits seen in any session
	// limitsAt is when Codex reported them.
	limitsAt time.Time
	// errors is the last status line parse failure per CLI, without private data.
	errors map[pb.Agent]string
	store  quotaStore // CLI account limits
	// reported is each session's last raw status line quota, before merging.
	reported map[string]quotaReport
}

func newQuotaService(dir string, adapters func() []agent.Adapter, byKind func(pb.Agent) (agent.Adapter, bool)) *quotaService {
	return &quotaService{dir: dir, adapters: adapters, byKind: byKind}
}

func (q *quotaService) path() string { return filepath.Join(q.dir, "quota.json") }

// load reads the saved CLI limits, keeping only the logged in account's.
func (q *quotaService) load() {
	b, err := os.ReadFile(q.path())
	if err != nil {
		return
	}
	var saved map[string]json.RawMessage
	if json.Unmarshal(b, &saved) != nil {
		return
	}
	loaded := quotaStore{}
	for key, raw := range saved {
		e := &pb.AgentQuota{}
		if protojson.Unmarshal(raw, e) != nil || e.Account == "" || key != quotaKey(e) {
			continue // an unknown account's record is not reused
		}
		a, ok := q.byKind(e.Agent)
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
		loaded[key] = e
	}
	q.mu.Lock()
	q.store = loaded
	q.mu.Unlock()
}

func (q *quotaService) saveLocked() {
	out := map[string]json.RawMessage{}
	for key, e := range q.store {
		if e.Account == "" {
			continue
		}
		b, err := protojson.Marshal(e)
		if err == nil {
			out[key] = b
		}
	}
	b, _ := json.Marshal(out)
	if err := writeFileSync(q.path(), b); err != nil {
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
	if err != nil {
		d.quota.setError(session.ag.Kind(), i18n.QuotaReadFailed("zh-CN"))
	} else {
		d.quota.setError(session.ag.Kind(), "")
	}
	if err != nil {
		return
	}
	session.observeEmptyIdentity(ctx, report, now)
	if session.observeConversationWithMissing(ctx, report.ConversationID, report.TranscriptPath, now, true) {
		session.recordStatusLine(report, now)
	}
	if report.Quota != nil && ctx.Err() == nil {
		d.quota.record(sessionID, report.Quota, now, report.QuotaMonotonic)
	}
}

// setError records the CLI's latest quota read failure, or clears it when
// detail is empty.
func (q *quotaService) setError(kind pb.Agent, detail string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if detail == "" {
		delete(q.errors, kind)
		return
	}
	if q.errors == nil {
		q.errors = map[pb.Agent]string{}
	}
	q.errors[kind] = detail
}

// hasSaved reports whether any limits record of the CLI exists.
func (q *quotaService) hasSaved(kind pb.Agent) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, e := range q.store {
		if e.Agent == kind {
			return true
		}
	}
	return false
}

func (q *quotaService) record(sessionID string, e *pb.AgentQuota, now time.Time, monotonic bool) {
	e.ObservedMs = now.UnixMilli()
	if e.Account == "" {
		e.SourceSessionId = sessionID
	}
	key := quotaKey(e)
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.store == nil {
		q.store = quotaStore{}
	}
	dropped := false
	if e.Account != "" {
		// Another account logged in: the old one's record goes.
		for k, old := range q.store {
			if old.Agent == e.Agent && old.Account != "" && old.Account != e.Account {
				delete(q.store, k)
				dropped = true
			}
		}
		if e.Agent == pb.Agent_AGENT_ANTIGRAVITY {
			delete(q.store, quotaKey(&pb.AgentQuota{Agent: e.Agent, SourceSessionId: agyUsageProbeSource}))
		}
	}
	// The status line does not say whose limits it shows, and a session
	// repeats those of its last reply until the next one. The same
	// windows it reported under another login are that login's.
	if q.reported == nil {
		q.reported = map[string]quotaReport{}
	}
	last, known := q.reported[sessionID]
	unchanged := known && proto.Equal(&pb.AgentQuota{Windows: last.windows}, &pb.AgentQuota{Windows: e.Windows})
	if monotonic && unchanged && last.account != e.Account {
		if dropped {
			q.saveLocked()
		}
		return
	}
	q.reported[sessionID] = quotaReport{account: e.Account, windows: e.Windows}
	fresh := monotonic && known && !unchanged
	if old := q.store[key]; old != nil {
		reported := e.Windows
		if now.UnixMilli() < old.ObservedMs {
			return
		}
		if monotonic && unchanged {
			return // this session is only repeating its last reply
		}
		if monotonic && !fresh {
			e.Windows = newestWindows(old.Windows, reported)
		} else {
			e.Windows = latestWindows(old.Windows, reported)
		}
		same := proto.Equal(&pb.AgentQuota{Windows: old.Windows, Plan: old.Plan, Login: old.Login}, &pb.AgentQuota{Windows: e.Windows, Plan: e.Plan, Login: e.Login})
		// Status lines repeat the last reply's limits without a source
		// timestamp. Re-reading identical data cannot prove it is fresh,
		// unless this session's own report changed to confirm it.
		if same && !fresh {
			return
		}
	}
	q.store[key] = e
	q.saveLocked()
}

// newestWindows conservatively merges a session's first status line, whose
// age is unknown (also after a daemon restart): later resets win, then higher
// usage for equal resets. Usage CAN fall within a reset cycle, e.g. when the
// provider raises a limit or grants credit, so this is not a freshness rule.
// Once any window changes from that session's own previous report, it has a
// new reply: recordQuotaWithPolicy adopts ALL its reported windows through
// latestWindows, including lower usage in unchanged windows. Later recorded
// resets still win, and omitted windows/fields remain. Repeated reports are
// not new evidence and cannot replace data or renew its observation time.
// Without reset times the first report wins too. A window only the record
// has is kept: the client marks it once its reset has passed.
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

// accountLimitsLoop also picks up limits from runs remote-agent does not
// manage, such as `codex exec` started by another agent. It only reads
// local files; nothing asks a service.
func (q *quotaService) accountLimitsLoop(ctx context.Context) {
	since := map[pb.Agent]time.Time{}
	t := time.NewTicker(accountLimitsInterval)
	defer t.Stop()
	for {
		q.pollAccountLimits(since, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// pollAccountLimits reads what changed since each CLI's last poll.
func (q *quotaService) pollAccountLimits(since map[pb.Agent]time.Time, now time.Time) {
	for _, a := range q.adapters() {
		src, ok := a.(agent.AccountLimitsSource)
		if !ok || a.Bin() == "" {
			continue
		}
		after, ok := since[a.Kind()]
		if !ok {
			after = now.Add(-accountLimitsLookback)
		}
		u, at, seen := src.AccountLimits(after)
		if u != nil {
			q.noteLimits(u, at)
		}
		if seen.After(after) {
			after = seen
		}
		since[a.Kind()] = after
	}
}

const (
	accountLimitsInterval = time.Minute
	// The longest Codex window is a week; older records are past their reset.
	accountLimitsLookback = 8 * 24 * time.Hour
)

// noteLimits keeps the newest usage limits Codex reported.
func (q *quotaService) noteLimits(u *pb.TokenUsage, at time.Time) {
	if u.PrimaryLimit == nil && u.SecondaryLimit == nil {
		return
	}
	q.mu.Lock()
	if at.After(q.limitsAt) || q.limits == nil {
		q.limits, q.limitsAt = u, at
	}
	q.mu.Unlock()
}

// entries lists the limits for the machine metadata.
func (q *quotaService) entries() []*pb.AgentQuota {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []*pb.AgentQuota
	for _, ag := range q.adapters() {
		if ag.Bin() == "" {
			continue
		}
		if cl, ok := ag.(agent.StatusLineAccount); ok {
			var agentQuotas []*pb.AgentQuota
			keys := make([]string, 0, len(q.store))
			for k := range q.store {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if q.store[k].Agent == ag.Kind() {
					cp := proto.CloneOf(q.store[k])
					if detail := q.errors[ag.Kind()]; detail != "" {
						cp.State, cp.Detail = pb.QuotaState_QUOTA_STATE_FAILED, detail
					}
					if order, ok := ag.(agent.QuotaWindowOrder); ok {
						order.OrderQuotaWindows(cp.Windows)
					}
					agentQuotas = append(agentQuotas, cp)
				}
			}
			if len(agentQuotas) > 0 {
				out = append(out, agentQuotas...)
			} else {
				e := &pb.AgentQuota{Agent: ag.Kind(), State: pb.QuotaState_QUOTA_STATE_WAITING}
				if ag.Kind() == pb.Agent_AGENT_ANTIGRAVITY {
					e.Detail = i18n.QuotaAgyWaiting("zh-CN")
				}
				if detail := q.errors[ag.Kind()]; detail != "" {
					e.State, e.Detail = pb.QuotaState_QUOTA_STATE_FAILED, detail
				}
				if cl.ManagedStatusLine("") {
					e.State, e.Detail = pb.QuotaState_QUOTA_STATE_UNSUPPORTED, i18n.QuotaManaged("zh-CN")
				}
				out = append(out, e)
			}
		} else {
			e := &pb.AgentQuota{Agent: ag.Kind(), State: pb.QuotaState_QUOTA_STATE_WAITING}
			if _, ok := ag.(agent.AccountLimitsSource); ok && q.limits == nil {
				e.Detail = i18n.QuotaCodexWaiting("zh-CN")
			}
			if u := q.limits; u != nil {
				e.State, e.Plan, e.ObservedMs = pb.QuotaState_QUOTA_STATE_OK, u.PlanType, q.limitsAt.UnixMilli()
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
