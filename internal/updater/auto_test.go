package updater

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

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
