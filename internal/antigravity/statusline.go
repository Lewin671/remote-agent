package antigravity

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

// ParseStatusLine reads the observed agy contract (not Claude's rate_limits).
// reset_time is RFC3339, and the window size is context_window_size.
func (a *Adapter) ParseStatusLine(raw []byte, sessionDir string) (*agent.StatusLine, error) {
	var in struct {
		Conversation string `json:"conversation_id"`
		Transcript   string `json:"transcript_path"`
		State        string `json:"agent_state"`
		Sandbox      struct {
			Enabled *bool `json:"enabled"`
		} `json:"sandbox"`
		Product string `json:"product"`
		Email   string `json:"email"`
		Plan    string `json:"plan_tier"`
		Mode    string `json:"cycle_mode"`
		Model   struct {
			ID     string `json:"id"`
			Effort string `json:"effort"`
		} `json:"model"`
		Window struct {
			Size int64 `json:"context_window_size"`
		} `json:"context_window"`
		Quota map[string]*struct {
			Remaining *float64 `json:"remaining_fraction"`
			Reset     string   `json:"reset_time"`
		} `json:"quota"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	if in.Product != "antigravity" {
		return nil, fmt.Errorf("unexpected status line product")
	}
	mode := in.Mode
	if mode == "" && in.State != "" {
		mode = "default"
	}
	model := in.Model.ID
	// agy reports display names in id, including the effort suffix.
	for _, effort := range efforts {
		model = strings.TrimSuffix(model, " ("+strings.ToUpper(effort[:1])+effort[1:]+")")
	}
	model = strings.ToLower(strings.ReplaceAll(model, " ", "-"))
	model, _ = splitModelEffort(model)
	out := &agent.StatusLine{ConversationID: in.Conversation, TranscriptPath: a.transcriptFor(in.Conversation, in.Transcript), CycleMode: mode, IdleKnown: in.State == "idle" || in.State == "reviewing", Background: in.State != "idle",
		Context:       &pb.TurnContext{Model: model, Effort: in.Model.Effort, PermissionMode: mode},
		ContextWindow: in.Window.Size,
	}
	var settings struct {
		ToolPermission string `json:"toolPermission"`
	}
	if sessionDir != "" {
		b, err := os.ReadFile(filepath.Join(sessionDir, SettingsFile))
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &settings); err != nil {
			return nil, err
		}
	}
	out.Context.ApprovalPolicy = settings.ToolPermission
	if in.Sandbox.Enabled != nil {
		out.Context.Sandbox = "disabled"
		if *in.Sandbox.Enabled {
			out.Context.Sandbox = "enabled"
		}
	}
	a.SetAccountInfo(in.Email, in.Plan)
	if len(in.Quota) == 0 {
		return out, nil
	}
	q := &pb.AgentQuota{Agent: a.Kind(), Account: EmailFingerprint(in.Email), Plan: in.Plan, State: pb.QuotaState_QUOTA_STATE_OK}
	if in.Email != "" {
		q.Login = pb.QuotaLogin_QUOTA_LOGIN_SUBSCRIPTION
	}
	names := make([]string, 0, len(in.Quota))
	for name := range in.Quota {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		v := in.Quota[name]
		if v == nil {
			continue
		}
		w := &pb.QuotaWindow{Name: name}
		if v.Remaining != nil && !math.IsNaN(*v.Remaining) && *v.Remaining >= 0 && *v.Remaining <= 1 {
			used := (1 - *v.Remaining) * 100
			w.UsedPercent = &used
		}
		if reset, err := time.Parse(time.RFC3339Nano, v.Reset); err == nil {
			w.ResetsAtMs = reset.UnixMilli()
		}
		switch name {
		case "gemini-5h", "3p-5h":
			w.WindowMinutes = 300
		case "gemini-weekly", "gemini-argon-wtu-weekly", "3p-weekly":
			w.WindowMinutes = 7 * 24 * 60
		}
		q.Windows = append(q.Windows, w)
	}
	out.Quota = q
	return out, nil
}

// DefaultStatusLine preserves agy's native, minimal footer when no custom
// command is configured. Quota data never becomes terminal output here.
func (a *Adapter) DefaultStatusLine(raw []byte) []byte {
	var in struct {
		Width int `json:"terminal_width"`
		Model struct {
			Name string `json:"display_name"`
		} `json:"model"`
	}
	if json.Unmarshal(raw, &in) != nil {
		return nil
	}
	left := "? for shortcuts"
	gap := in.Width - len(left) - len(in.Model.Name)
	if gap < 1 || gap > 1000 {
		gap = 1
	}
	return []byte(left + strings.Repeat(" ", gap) + in.Model.Name + "\n")
}
