package updater

import (
	"context"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func autoFixture(t *testing.T) *fixture {
	t.Helper()
	f := setup(t)
	t.Setenv("HOME", t.TempDir())
	// Auto must never reach the real launchd/systemd manager in unit tests.
	f.u.autoCommand = func(context.Context, string, ...string) (string, error) { return "", nil }
	return f
}

func TestAutoChoiceAndFailedEnable(t *testing.T) {
	for _, prior := range []string{"", `{"enabled":false,"time":"06:15","executable":"/old/remote-agent"}`, `{"enabled":true,"time":"06:15","executable":"/old/remote-agent"}`} {
		t.Run(prior, func(t *testing.T) {
			f := autoFixture(t)
			path := filepath.Join(f.u.state, "update-auto.json")
			if err := os.MkdirAll(f.u.state, 0700); err != nil {
				t.Fatal(err)
			}
			if prior != "" {
				if err := os.WriteFile(path, []byte(prior), 0600); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			f.u.autoCommand = func(_ context.Context, _ string, args ...string) (string, error) {
				calls++
				// RunAtLoad must see enabled=true before the task starts.
				c, err := f.u.readAuto()
				if err != nil || !c.Enabled || c.Time != "04:00" {
					t.Fatalf("task started with config=%+v err=%v", c, err)
				}
				return "", errors.New("no user service manager")
			}
			if err := f.u.Auto(context.Background(), true, ""); err == nil || calls == 0 {
				t.Fatalf("failed activation: err=%v calls=%d", err, calls)
			}
			b, err := os.ReadFile(path)
			if prior == "" {
				if !os.IsNotExist(err) {
					t.Fatalf("failed enable left a choice: %q err=%v", b, err)
				}
			} else if err != nil || string(b) != prior {
				t.Fatalf("previous choice changed: %q err=%v", b, err)
			}
			f.u.autoCommand = func(context.Context, string, ...string) (string, error) { return "", nil }
			if err := f.u.Auto(context.Background(), true, ""); err != nil {
				t.Fatalf("retry: %v", err)
			}
			c, err := f.u.readAuto()
			if err != nil || !c.Enabled || c.Time != "04:00" || c.Executable != f.u.exe {
				t.Fatalf("enabled config=%+v err=%v", c, err)
			}
			// Disabling records the choice even if the OS task cannot unload.
			f.u.autoCommand = func(context.Context, string, ...string) (string, error) {
				return "", errors.New("cannot unload")
			}
			_ = f.u.Auto(context.Background(), false, "")
			c, err = f.u.readAuto()
			if err != nil || c.Enabled || c.Executable != f.u.exe {
				t.Fatalf("explicit disable config=%+v err=%v", c, err)
			}
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("explicit disable not recorded: %v", err)
			}
		})
	}
}

func TestAutoWaitsForRestartAndTimesOut(t *testing.T) {
	t.Run("restarting", func(t *testing.T) {
		f := autoFixture(t)
		f.u.healthTimeout = time.Second
		calls := 0
		f.u.health = func(context.Context) (Health, error) {
			calls++
			if calls == 1 {
				return Health{}, errors.New("daemon restarting")
			}
			return f.live, nil
		}
		if err := f.u.Auto(context.Background(), true, ""); err != nil || calls < 2 {
			t.Fatalf("err=%v health calls=%d", err, calls)
		}
	})
	for _, problem := range []string{"source", "unhealthy", "other PID", "old version", "other executable"} {
		t.Run(problem, func(t *testing.T) {
			f := autoFixture(t)
			switch problem {
			case "source":
				f.u.current.Channel = "source"
			case "unhealthy":
				f.u.health = func(context.Context) (Health, error) { return Health{}, errors.New("not ready") }
			case "other PID":
				f.managerPID = f.live.PID + 1
			case "old version":
				f.live.Version = "1.0.0"
			case "other executable":
				f.live.Executable = "/other/remote-agent"
			}
			f.u.autoCommand = func(context.Context, string, ...string) (string, error) {
				t.Fatal("attempted to activate task before daemon was ready")
				return "", nil
			}
			if err := f.u.Auto(context.Background(), true, ""); err == nil {
				t.Fatal("enabled without healthy release service")
			}
			if _, err := os.Stat(filepath.Join(f.u.state, "update-auto.json")); !os.IsNotExist(err) {
				t.Fatalf("failed prerequisite recorded a choice: %v", err)
			}
		})
	}
}

func TestTaskGenerationPreservesStateAndEscapesPaths(t *testing.T) {
	f := setup(t)
	f.u.exe = "/home/test/odd & \"quote\" $name%/remote-agent"
	f.u.state = "/home/test/custom & state"
	t.Setenv("REMOTE_AGENT_TMUX_SOCKET", "my-tmux")
	for _, platform := range []string{"darwin", "linux"} {
		files, err := f.u.autoFiles(platform, "/home/test", "04:35")
		if err != nil {
			t.Fatal(err)
		}
		var all strings.Builder
		for _, body := range files {
			all.WriteString(body)
			if platform == "darwin" {
				var v any
				if err := xml.Unmarshal([]byte(body), &v); err != nil {
					t.Fatalf("invalid plist: %v", err)
				}
			}
		}
		body := all.String()
		for _, needle := range []string{"REMOTE_AGENT_HOME", "REMOTE_AGENT_TMUX_SOCKET", "my-tmux", "--scheduled"} {
			if !strings.Contains(body, needle) {
				t.Fatalf("%s missing %s", platform, needle)
			}
		}
		if platform == "linux" && (!strings.Contains(body, "Persistent=true") || !strings.Contains(body, "$$name%%")) {
			t.Fatal(body)
		}
		if platform == "darwin" && (!strings.Contains(body, "StartCalendarInterval") || !strings.Contains(body, "&amp;")) {
			t.Fatal(body)
		}
	}
	for _, at := range []string{"4:00", "24:00", "04:60", "04:00\nExecStart=evil"} {
		if _, err := f.u.autoFiles("linux", "/tmp", at); err == nil {
			t.Fatalf("accepted time %q", at)
		}
	}
}

func TestStorageGenerationGuard(t *testing.T) {
	dir := t.TempDir()
	if err := EnsureState(dir); err != nil {
		t.Fatal(err)
	}
	if err := EnsureState(dir); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "state-generation")
	for _, text := range []string{"0", strconv.Itoa(BuildPolicy().State + 1), "corrupt"} {
		if err := os.WriteFile(marker, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		if err := EnsureState(dir); err == nil {
			t.Fatalf("accepted storage generation %q", text)
		}
		b, _ := os.ReadFile(marker)
		if string(b) != text {
			t.Fatal("guard rewrote unknown state")
		}
	}
}
