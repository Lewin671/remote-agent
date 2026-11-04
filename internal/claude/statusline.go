package claude

import (
	"encoding/json"
	"sort"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

// ParseStatusLine normalizes Claude's existing rate_limits contract. Other
// provider formats are never inferred from fields such as context_window.
func (a *Adapter) ParseStatusLine(raw []byte, _ string) (*agent.StatusLine, error) {
	var in struct {
		RateLimits map[string]*struct {
			Used  *float64 `json:"used_percentage"`
			Reset int64    `json:"resets_at"`
		} `json:"rate_limits"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	out := &agent.StatusLine{QuotaMonotonic: true}
	if len(in.RateLimits) == 0 {
		return out, nil
	}
	q := &pb.AgentQuota{Agent: a.Kind(), State: pb.QuotaState_QUOTA_STATE_OK}
	var sub bool
	q.Account, q.Plan, sub = a.Account()
	if sub {
		q.Login = pb.QuotaLogin_QUOTA_LOGIN_SUBSCRIPTION
	}
	names := make([]string, 0, len(in.RateLimits))
	for n := range in.RateLimits {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		r := in.RateLimits[n]
		if r == nil {
			continue
		}
		w := &pb.QuotaWindow{Name: n, UsedPercent: r.Used}
		if r.Reset > 0 {
			w.ResetsAtMs = r.Reset * 1000
		}
		switch n {
		case "five_hour":
			w.WindowMinutes = 300
		case "seven_day":
			w.WindowMinutes = 7 * 24 * 60
		}
		q.Windows = append(q.Windows, w)
	}
	out.Quota = q
	return out, nil
}

var _ agent.StatusLineSource = (*Adapter)(nil)
