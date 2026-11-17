package antigravity

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

const (
	usageProbeTimeout   = 60 * time.Second
	maxUsageProbeStdout = 1 << 20 // 1 MiB
	maxUsageProbeStderr = 64 << 10
)

var (
	ErrUsageUnsupported = errors.New("agy /usage unsupported")
	ErrUsageNotLoggedIn = errors.New("agy not logged in")
	ErrUsageOutputLimit = errors.New("agy /usage output exceeded 1 MiB")
)

func quotaWindowMinutes(id, window string) int64 {
	switch id {
	case "gemini-5h", "3p-5h":
		return 300
	case "gemini-weekly", "gemini-argon-wtu-weekly", "3p-weekly":
		return 7 * 24 * 60
	}
	switch {
	case window == "5h" || strings.HasSuffix(id, "-5h"):
		return 300
	case window == "weekly" || strings.HasSuffix(id, "weekly"):
		return 7 * 24 * 60
	}
	return 0
}

// ParseUsageOutput parses the JSON output of `agy -p /usage --output-format json`.
// A missing remaining_fraction inside a valid bucket object with status=="SUCCESS"
// means 0 remaining (100% used), because proto3 JSON omits zero floats.
func ParseUsageOutput(raw []byte) (*pb.AgentQuota, error) {
	var in struct {
		Status   string          `json:"status"`
		Response string          `json:"response"`
		Error    json.RawMessage `json:"error"`
		Command  *struct {
			Name string `json:"name"`
			Data *struct {
				Groups []struct {
					Name    string `json:"name"`
					Buckets []*struct {
						ID        string   `json:"id"`
						Window    string   `json:"window"`
						Remaining *float64 `json:"remaining_fraction"`
						ResetTime string   `json:"reset_time"`
					} `json:"buckets"`
				} `json:"groups"`
			} `json:"data"`
		} `json:"command"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		if classified := classifyUsageText(string(raw)); classified != nil {
			return nil, classified
		}
		return nil, fmt.Errorf("decode agy usage json: %w", err)
	}
	if in.Status != "SUCCESS" {
		if classified := classifyUsageText(in.Response, string(in.Error)); classified != nil {
			return nil, classified
		}
		return nil, fmt.Errorf("agy /usage status %q", in.Status)
	}
	if in.Command == nil || in.Command.Name != "usage" {
		return nil, ErrUsageUnsupported
	}
	if in.Command.Data == nil || len(in.Command.Data.Groups) == 0 {
		return nil, errors.New("agy /usage returned no groups")
	}

	var windows []*pb.QuotaWindow
	for _, g := range in.Command.Data.Groups {
		for _, b := range g.Buckets {
			if b == nil || b.ID == "" {
				continue
			}
			w := &pb.QuotaWindow{
				Name:          b.ID,
				WindowMinutes: quotaWindowMinutes(b.ID, b.Window),
			}
			if b.Remaining == nil {
				// proto3 JSON omits zero floats; exhausted buckets only include reset_time.
				used := 100.0
				w.UsedPercent = &used
			} else if !math.IsNaN(*b.Remaining) && !math.IsInf(*b.Remaining, 0) && *b.Remaining >= 0 && *b.Remaining <= 1 {
				used := (1 - *b.Remaining) * 100
				w.UsedPercent = &used
			}
			if b.ResetTime != "" {
				if reset, err := time.Parse(time.RFC3339Nano, b.ResetTime); err == nil {
					w.ResetsAtMs = reset.UnixMilli()
				}
			}
			windows = append(windows, w)
		}
	}
	if len(windows) == 0 {
		return nil, errors.New("agy /usage returned no buckets")
	}
	return &pb.AgentQuota{
		Agent:   pb.Agent_AGENT_ANTIGRAVITY,
		State:   pb.QuotaState_QUOTA_STATE_OK,
		Windows: windows,
	}, nil
}

// ProbeUsage runs `agy -p /usage --output-format json` in a private temporary
// directory, blocking browser OAuth popups and keeping the process group bounded.
func (a *Adapter) ProbeUsage(ctx context.Context, triggerLogPath string) (*pb.AgentQuota, error) {
	a.probeMu.Lock()
	defer a.probeMu.Unlock()

	bin := a.Bin()
	if bin == "" {
		return nil, exec.ErrNotFound
	}

	timeout := a.usageTimeout
	if timeout <= 0 {
		timeout = usageProbeTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	tmpDir, err := os.MkdirTemp("", "ra-agy-usage-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)

	shimDir := filepath.Join(tmpDir, "bin")
	if err := os.MkdirAll(shimDir, 0o700); err != nil {
		return nil, err
	}
	browserMarker := filepath.Join(tmpDir, ".browser-opened")
	shimScript := "#!/bin/sh\n: > " + shellQuote(browserMarker) + "\nexit 1\n"
	for _, name := range []string{"open", "xdg-open"} {
		if err := os.WriteFile(filepath.Join(shimDir, name), []byte(shimScript), 0o700); err != nil {
			return nil, err
		}
	}

	devNull, err := os.Open(os.DevNull)
	if err != nil {
		return nil, err
	}
	defer devNull.Close()

	cmd := exec.CommandContext(ctx, bin, "-p", "/usage", "--output-format", "json")
	cmd.Dir = tmpDir
	cmd.Stdin = devNull
	cmd.Env = usageProbeEnv(a.execEnv(), shimDir, filepath.Join(shimDir, "open"))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 2 * time.Second

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr limitedBuffer
	stderr.max = maxUsageProbeStderr
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("agy binary not found: %w", err)
		}
		return nil, err
	}
	pgid := cmd.Process.Pid
	defer func() {
		// Reap/terminate any stray subprocesses in the probe's process group only.
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}()

	raw, readErr := io.ReadAll(io.LimitReader(stdout, maxUsageProbeStdout+1))
	if len(raw) > maxUsageProbeStdout {
		cancel()
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		_ = cmd.Wait()
		return nil, ErrUsageOutputLimit
	}
	waitErr := cmd.Wait()

	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if _, statErr := os.Stat(browserMarker); statErr == nil {
		return nil, ErrUsageNotLoggedIn
	}
	if readErr != nil {
		return nil, readErr
	}

	if len(bytes.TrimSpace(raw)) > 0 {
		q, parseErr := ParseUsageOutput(raw)
		if parseErr == nil {
			fp, plan, _ := a.Account()
			if fp == "" && triggerLogPath != "" {
				fp = ExtractLogEmailFingerprint(triggerLogPath)
				a.setAccountFingerprint(fp)
			}
			q.Account = fp
			q.Plan = plan
			if fp != "" {
				q.Login = pb.QuotaLogin_QUOTA_LOGIN_SUBSCRIPTION
			}
			a.OrderQuotaWindows(q.Windows)
			return q, nil
		}
		if errors.Is(parseErr, ErrUsageNotLoggedIn) || errors.Is(parseErr, ErrUsageUnsupported) {
			return nil, parseErr
		}
		if classified := classifyUsageText(stderr.String()); classified != nil {
			return nil, classified
		}
		return nil, parseErr
	}

	if classified := classifyUsageText(stderr.String()); classified != nil {
		return nil, classified
	}
	if waitErr != nil {
		return nil, waitErr
	}
	return nil, errors.New("agy /usage returned empty output")
}

// ExtractLogEmailFingerprint reads only the `applyAuthResult: email=...` line
// from an agy CLI log file and returns its hashed account fingerprint.
func ExtractLogEmailFingerprint(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		idx := strings.Index(line, "applyAuthResult:")
		if idx < 0 {
			continue
		}
		rest := line[idx+len("applyAuthResult:"):]
		eIdx := strings.Index(rest, "email=")
		if eIdx < 0 {
			continue
		}
		val := rest[eIdx+len("email="):]
		if cut := strings.IndexAny(val, ", \t\r\n)"); cut >= 0 {
			val = val[:cut]
		}
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		if val != "" {
			return EmailFingerprint(val)
		}
	}
	return ""
}

func classifyUsageText(parts ...string) error {
	combined := strings.ToLower(strings.Join(parts, "\n"))
	if combined == "" {
		return nil
	}
	for _, needle := range []string{
		"not logged in",
		"not signed in",
		"login required",
		"authentication required",
		"unauthenticated",
		"unauthorized",
		"please sign in",
		"please log in",
		"oauth",
	} {
		if strings.Contains(combined, needle) {
			return ErrUsageNotLoggedIn
		}
	}
	for _, needle := range []string{
		"unknown command",
		"unknown slash command",
		"unrecognized command",
		"unsupported command",
		"no such command",
	} {
		if strings.Contains(combined, needle) {
			return ErrUsageUnsupported
		}
	}
	return nil
}

func usageProbeEnv(base []string, shimDir, browserPath string) []string {
	if len(base) == 0 {
		base = os.Environ()
	}
	var out []string
	var curPath string
	for _, kv := range base {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch k {
		case "PATH":
			curPath = v
		case "BROWSER", "AGY_CLI_DISABLE_AUTO_UPDATE":
			continue
		default:
			out = append(out, kv)
		}
	}
	if curPath == "" {
		out = append(out, "PATH="+shimDir)
	} else {
		out = append(out, "PATH="+shimDir+string(os.PathListSeparator)+curPath)
	}
	out = append(out, "BROWSER="+browserPath, "AGY_CLI_DISABLE_AUTO_UPDATE=true")
	return out
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

type limitedBuffer struct {
	buf bytes.Buffer
	max int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	rem := l.max - l.buf.Len()
	if rem > 0 {
		if len(p) > rem {
			l.buf.Write(p[:rem])
		} else {
			l.buf.Write(p)
		}
	}
	return len(p), nil
}

func (l *limitedBuffer) String() string {
	return l.buf.String()
}
