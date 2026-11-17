package antigravity

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

func TestParseUsageOutput(t *testing.T) {
	t.Run("valid response", func(t *testing.T) {
		raw := []byte(`{
			"conversation_id":"",
			"status":"SUCCESS",
			"response":"Gemini Models\tWeekly Limit Remaining\t75%\t...",
			"duration_seconds":0,
			"num_turns":0,
			"usage":{"input_tokens":0},
			"command":{
				"name":"usage",
				"data":{
					"groups":[
						{
							"name":"Gemini Models",
							"buckets":[
								{"id":"gemini-weekly","window":"weekly","remaining_fraction":0.7490689158439636,"reset_time":"2026-11-23T16:06:20Z"},
								{"id":"gemini-5h","window":"5h","remaining_fraction":0.8836429119110107,"reset_time":"2026-11-17T17:06:20Z"}
							]
						},
						{
							"name":"Claude and GPT models",
							"buckets":[
								{"id":"3p-weekly","window":"weekly","remaining_fraction":1,"reset_time":"2026-11-24T16:51:32Z"},
								{"id":"3p-5h","window":"5h","remaining_fraction":0.25,"reset_time":"2026-11-17T21:51:32Z"}
							]
						}
					]
				}
			}
		}`)
		q, err := ParseUsageOutput(raw)
		if err != nil {
			t.Fatalf("ParseUsageOutput failed: %v", err)
		}
		if q.Agent != pb.Agent_AGENT_ANTIGRAVITY || q.State != pb.QuotaState_QUOTA_STATE_OK {
			t.Fatalf("unexpected agent/state: %v", q)
		}
		if len(q.Windows) != 4 {
			t.Fatalf("got %d windows, want 4", len(q.Windows))
		}
		byName := map[string]*pb.QuotaWindow{}
		for _, w := range q.Windows {
			byName[w.Name] = w
		}
		if w := byName["gemini-weekly"]; w == nil || w.WindowMinutes != 7*24*60 || math.Abs(w.GetUsedPercent()-(1-0.7490689158439636)*100) > 1e-6 {
			t.Fatalf("gemini-weekly mismatch: %v", w)
		}
		if w := byName["gemini-5h"]; w == nil || w.WindowMinutes != 300 || math.Abs(w.GetUsedPercent()-(1-0.8836429119110107)*100) > 1e-6 {
			t.Fatalf("gemini-5h mismatch: %v", w)
		}
		if w := byName["3p-weekly"]; w == nil || w.WindowMinutes != 7*24*60 || w.GetUsedPercent() != 0 {
			t.Fatalf("3p-weekly mismatch: %v", w)
		}
		if w := byName["3p-5h"]; w == nil || w.WindowMinutes != 300 || w.GetUsedPercent() != 75 {
			t.Fatalf("3p-5h mismatch: %v", w)
		}
	})

	t.Run("missing remaining_fraction treated as zero on SUCCESS", func(t *testing.T) {
		raw := []byte(`{
			"status":"SUCCESS",
			"command":{
				"name":"usage",
				"data":{
					"groups":[
						{
							"name":"Gemini Models",
							"buckets":[
								{"id":"gemini-5h","window":"5h","reset_time":"2026-11-17T17:06:20Z"}
							]
						}
					]
				}
			}
		}`)
		q, err := ParseUsageOutput(raw)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if len(q.Windows) != 1 || q.Windows[0].UsedPercent == nil || q.Windows[0].GetUsedPercent() != 100 {
			t.Fatalf("expected exhausted 100%% window, got %v", q.Windows)
		}
	})

	t.Run("status ERROR", func(t *testing.T) {
		raw := []byte(`{"status":"ERROR","command":{"name":"usage","data":{"groups":[{"buckets":[{"id":"gemini-5h","reset_time":"2026-11-17T17:06:20Z"}]}]}}}`)
		if _, err := ParseUsageOutput(raw); err == nil {
			t.Fatal("expected error when status is ERROR")
		}
	})

	t.Run("missing or wrong command", func(t *testing.T) {
		for _, raw := range [][]byte{
			[]byte(`{"status":"SUCCESS","response":"ok"}`),
			[]byte(`{"status":"SUCCESS","command":{"name":"other","data":{"groups":[{"buckets":[{"id":"gemini-5h"}]}]}}}`),
		} {
			_, err := ParseUsageOutput(raw)
			if !errors.Is(err, ErrUsageUnsupported) {
				t.Fatalf("got %v, want ErrUsageUnsupported", err)
			}
		}
	})

	t.Run("missing groups or buckets", func(t *testing.T) {
		for _, raw := range [][]byte{
			[]byte(`{"status":"SUCCESS","command":{"name":"usage","data":{"groups":[]}}}`),
			[]byte(`{"status":"SUCCESS","command":{"name":"usage","data":{"groups":[{"name":"Empty","buckets":[]}]}}}`),
		} {
			if _, err := ParseUsageOutput(raw); err == nil {
				t.Fatalf("expected error for %s", raw)
			}
		}
	})

	t.Run("non-json", func(t *testing.T) {
		if _, err := ParseUsageOutput([]byte("not json at all")); err == nil {
			t.Fatal("expected error for non-JSON output")
		}
	})
}

func TestProbeUsageExecutionAndAccount(t *testing.T) {
	home := t.TempDir()
	logDir := filepath.Join(home, "log")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// An earlier run's log names another login; only the probe's own counts.
	if err := os.WriteFile(filepath.Join(logDir, "cli-20261117_090000.log"), []byte("I1117 09:00:00.123 server_oauth.go:221] applyAuthResult: email=earlier@example.com, authMethod=consumer\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	envRecord := filepath.Join(t.TempDir(), "env.txt")
	script := writeFakeAgyScript(t, fmt.Sprintf(`#!/bin/sh
printf 'ARGS=%%s\nAUTO=%%s\nPROXY=%%s\nBROWSER=%%s\nPWD=%%s\n' "$*" "$AGY_CLI_DISABLE_AUTO_UPDATE" "$HTTPS_PROXY" "$BROWSER" "$PWD" > %s
if [ -s /dev/stdin ]; then exit 2; fi
printf 'I1117 09:05:00.000 server_oauth.go:221] applyAuthResult: email=probe-user@example.com, authMethod=consumer\n' > %s/cli-own-$$.log
cat <<'EOF'
{"status":"SUCCESS","command":{"name":"usage","data":{"groups":[{"name":"Gemini Models","buckets":[{"id":"gemini-5h","window":"5h","remaining_fraction":0.5,"reset_time":"2026-11-17T17:00:00Z"}]}]}}}
EOF
`, shellQuote(envRecord), shellQuote(logDir)))

	a := New(script, home)
	a.SetCommandEnv(map[string]string{"HTTPS_PROXY": "http://127.0.0.1:7897"})

	q, err := a.ProbeUsage(context.Background())
	if err != nil {
		t.Fatalf("ProbeUsage failed: %v", err)
	}
	b, err := os.ReadFile(envRecord)
	if err != nil {
		t.Fatal(err)
	}
	gotEnv := string(b)
	if !strings.Contains(gotEnv, "ARGS=-p /usage --output-format json") {
		t.Fatalf("unexpected args: %s", gotEnv)
	}
	if !strings.Contains(gotEnv, "AUTO=true") {
		t.Fatalf("AGY_CLI_DISABLE_AUTO_UPDATE must be lowercase true: %s", gotEnv)
	}
	if !strings.Contains(gotEnv, "PROXY=http://127.0.0.1:7897") {
		t.Fatalf("missing proxy env: %s", gotEnv)
	}
	if !strings.Contains(gotEnv, "ra-agy-usage-") {
		t.Fatalf("expected private temp cwd/browser shim: %s", gotEnv)
	}
	if q.Account != EmailFingerprint("probe-user@example.com") {
		t.Fatalf("expected the probe's own log account, got %q", q.Account)
	}
	if strings.Contains(q.String(), "probe-user@example.com") {
		t.Fatal("plaintext email leaked into AgentQuota")
	}

	// The login that answered wins over the status line's; its plan is
	// kept only for the same login.
	for _, tc := range []struct{ statusEmail, wantPlan string }{
		{"switched-away@example.com", ""},
		{"probe-user@example.com", "Google AI Pro"},
	} {
		a2 := New(script, home)
		a2.SetAccountInfo(tc.statusEmail, "Google AI Pro")
		q2, err := a2.ProbeUsage(context.Background())
		if err != nil {
			t.Fatalf("ProbeUsage failed: %v", err)
		}
		if q2.Account != EmailFingerprint("probe-user@example.com") || q2.Plan != tc.wantPlan {
			t.Fatalf("status line %s: got account=%q plan=%q", tc.statusEmail, q2.Account, q2.Plan)
		}
	}
}

func TestProbeUsageOversizeOutput(t *testing.T) {
	script := writeFakeAgyScript(t, `#!/bin/sh
dd if=/dev/zero bs=1024 count=1100 2>/dev/null | tr '\0' 'a'
`)
	a := New(script, t.TempDir())
	_, err := a.ProbeUsage(context.Background())
	if !errors.Is(err, ErrUsageOutputLimit) {
		t.Fatalf("got %v, want ErrUsageOutputLimit", err)
	}
}

func TestProbeUsageBrowserPopupReturnsNotLoggedIn(t *testing.T) {
	script := writeFakeAgyScript(t, `#!/bin/sh
open https://accounts.example.com/oauth
exit 1
`)
	a := New(script, t.TempDir())
	_, err := a.ProbeUsage(context.Background())
	if !errors.Is(err, ErrUsageNotLoggedIn) {
		t.Fatalf("got %v, want ErrUsageNotLoggedIn", err)
	}
}

// Ending a probe (timeout or daemon shutdown) must take its whole process
// group down, not only agy itself.
func TestProbeUsageCancelKillsProcessGroup(t *testing.T) {
	fifo := filepath.Join(t.TempDir(), "child.pid")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	script := writeFakeAgyScript(t, fmt.Sprintf(`#!/bin/sh
sleep 30 &
printf '%%s\n' "$!" > %s
wait
`, shellQuote(fifo)))

	a := New(script, t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := a.ProbeUsage(ctx)
		done <- err
	}()
	rawPID, err := os.ReadFile(fifo) // blocks until the fake agy started its child
	if err != nil {
		t.Fatal(err)
	}
	childPID, err := strconv.Atoi(strings.TrimSpace(string(rawPID)))
	if err != nil {
		t.Fatalf("bad pid %q: %v", rawPID, err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	// The group was SIGKILLed before ProbeUsage returned; a killed child that
	// is not yet reaped is a zombie, which ps reports as state Z.
	out, _ := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(childPID)).Output()
	if st := strings.TrimSpace(string(out)); st != "" && !strings.HasPrefix(st, "Z") {
		t.Fatalf("child process %d survived process-group kill (state %q)", childPID, st)
	}
}

func writeFakeAgyScript(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "agy")
	if err := os.WriteFile(p, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}
