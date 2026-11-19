package antigravity

import (
	"encoding/json"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

func TestStatusModelNamesResumeWithCatalogIdentifiers(t *testing.T) {
	a := New("/bin/true", t.TempDir())
	models, err := ParseModels(sampleAgyModelsOutput)
	if err != nil {
		t.Fatal(err)
	}
	a.models.cached, a.models.bin, a.models.fetchedAt = &pb.ListModelsResponse{Models: models}, a.Bin(), time.Now()
	for _, model := range models {
		t.Run(model.Slug, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"product": "antigravity", "model": map[string]string{"id": model.DisplayName + " (Low)", "effort": "low"}})
			report, err := a.ParseStatusLine(raw, "")
			if err != nil {
				t.Fatal(err)
			}
			if report.Context.Model != model.Slug {
				t.Fatalf("model = %q, want %q", report.Context.Model, model.Slug)
			}
			dir := t.TempDir()
			mustWrite(t, filepath.Join(dir, SettingsFile), []byte(`{"toolPermission":"request-review"}`))
			argv, err := a.Argv(&agent.Launch{Dir: dir, Cwd: dir, Resume: true, SessionID: "example-conversation", Context: report.Context})
			if err != nil {
				t.Fatal(err)
			}
			for i, arg := range argv {
				if arg == "--model" && argv[i+1] == model.Slug {
					return
				}
			}
			t.Fatalf("resume did not preserve catalog model: %v", argv)
		})
	}
	if got := a.models.Resolve(a.Bin(), "Unlisted Model 9.9 (High)"); got != "" {
		t.Fatalf("invented slug %q", got)
	}
	if got := a.models.Resolve("another-cli", "Claude Sonnet 5.5 (Low)"); got != "" {
		t.Fatalf("used another binary's catalog: %q", got)
	}
}

func TestStatusLineObservedContract(t *testing.T) {
	a := New("agy", t.TempDir())
	models, err := ParseModels(sampleAgyModelsOutput)
	if err != nil {
		t.Fatal(err)
	}
	a.models.cached, a.models.bin = &pb.ListModelsResponse{Models: models}, "agy"
	raw := []byte(`{"product":"antigravity","agent_state":"idle","email":"user@example.com","plan_tier":"Google AI Pro","model":{"id":"Claude Sonnet 5.5 (Low)","effort":"low"},"context_window":{"context_window_size":200000},"quota":{"gemini-5h":{"remaining_fraction":0.64207405,"reset_time":"2026-11-03T21:00:07Z","reset_in_seconds":11975},"3p-weekly":{"remaining_fraction":0.9},"gemini-weekly":{}}}`)
	r, err := a.ParseStatusLine(raw, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Context.PermissionMode != "default" || r.Context.Model != "claude-sonnet-5-5" || r.Context.Effort != "low" || r.ContextWindow != 200000 {
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

func TestStatusLineSuppressesUnresolvedStartupModel(t *testing.T) {
	a := New("agy", t.TempDir())
	models, err := ParseModels(sampleAgyModelsOutput)
	if err != nil {
		t.Fatal(err)
	}
	a.models.cached, a.models.bin = &pb.ListModelsResponse{Models: models}, "agy"

	// Startup statusLine before model catalog & CLI flags are applied reports the
	// settings.json default display name with explicit context_window_size: 0 and no effort.
	startupRaw := []byte(`{"product":"antigravity","agent_state":"idle","model":{"id":"Gemini 4 Argon (High)","display_name":"Gemini 4 Argon (High)"},"context_window":{"context_window_size":0},"sandbox":{"enabled":false}}`)
	r, err := a.ParseStatusLine(startupRaw, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Context.Model != "" || r.Context.Effort != "" {
		t.Fatalf("expected unresolved startup model suppressed, got model=%q effort=%q", r.Context.Model, r.Context.Effort)
	}
	if r.Context.PermissionMode != "default" || !r.IdleKnown || r.Context.Sandbox != "disabled" {
		t.Fatalf("startup metadata lost: %+v", r)
	}

	// Subsequent payload once catalog and flags apply resolves effort-bearing models.
	readyRaw := []byte(`{"product":"antigravity","agent_state":"idle","model":{"id":"Gemini 4 Hydrogen (Medium)","display_name":"Gemini 4 Hydrogen (Medium)","effort":"medium"},"context_window":{"context_window_size":1048576},"sandbox":{"enabled":false}}`)
	r, err = a.ParseStatusLine(readyRaw, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Context.Model != "gemini-4-hydrogen" || r.Context.Effort != "medium" || r.ContextWindow != 1048576 {
		t.Fatalf("unexpected resolved model context: %+v", r)
	}

	// Effortless model once window size is populated must also resolve.
	orchRaw := []byte(`{"product":"antigravity","agent_state":"idle","model":{"id":"Orchestrator (Neon)","display_name":"Orchestrator (Neon)"},"context_window":{"context_window_size":1048576}}`)
	r, err = a.ParseStatusLine(orchRaw, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Context.Model != "gemini-pro-orchestrator-neon" || r.Context.Effort != "" || r.ContextWindow != 1048576 {
		t.Fatalf("unexpected effortless model context: %+v", r)
	}
}
