package antigravity

import (
	"math"
	"strings"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

func TestStatusLineObservedContract(t *testing.T) {
	a := New("agy", t.TempDir())
	raw := []byte(`{"product":"antigravity","email":"user@example.com","plan_tier":"Google AI Pro","model":{"id":"Claude Sonnet 5.5 (Low)","effort":"low"},"context_window":{"context_window_size":200000},"quota":{"gemini-5h":{"remaining_fraction":0.64207405,"reset_time":"2026-11-03T21:00:07Z","reset_in_seconds":11975},"3p-weekly":{"remaining_fraction":0.9},"gemini-weekly":{}}}`)
	r, err := a.ParseStatusLine(raw, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Context.PermissionMode != "default" || r.Context.Model != "claude-sonnet-5.5" || r.Context.Effort != "low" || r.ContextWindow != 200000 {
		t.Fatalf("context %v", r)
	}
	if r.Quota.Account == "" || strings.Contains(r.Quota.String(), "user@example.com") || r.Quota.Login != pb.QuotaLogin_QUOTA_LOGIN_SUBSCRIPTION {
		t.Fatal("account not fingerprinted")
	}
	for _, w := range r.Quota.Windows {
		if w.Name == "gemini-5h" {
			if math.Abs(w.GetUsedPercent()-35.792595) > 1e-8 || w.ResetsAtMs != time.Date(2026, 11, 3, 21, 0, 7, 0, time.UTC).UnixMilli() || w.WindowMinutes != 300 {
				t.Fatalf("window %v", w)
			}
		}
		if w.Name == "gemini-weekly" && w.UsedPercent != nil {
			t.Fatal("missing quota became zero")
		}
	}
	r, err = a.ParseStatusLine([]byte(`{"product":"antigravity","cycle_mode":"plan"}`), "")
	if err != nil || r.Quota != nil || r.Context.PermissionMode != "plan" {
		t.Fatal("startup or mode-only report")
	}
	if _, err = a.ParseStatusLine([]byte(`{"context_window":{},"rate_limits":{}}`), ""); err == nil {
		t.Fatal("accepted another CLI contract")
	}
	out := a.DefaultStatusLine([]byte(`{"terminal_width":50,"model":{"display_name":"Gemini 3.8 Flash (Low)"}}`))
	if !strings.HasPrefix(string(out), "? for shortcuts") || !strings.HasSuffix(string(out), "Gemini 3.8 Flash (Low)\n") {
		t.Fatalf("native footer %q", out)
	}
}
