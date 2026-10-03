package claude

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

// A fork resumes the source under a session ID of its own, with the
// source's options as a resume would take them, and remembers which
// records its transcript will repeat.
func TestForkArgvAndCopiedRecords(t *testing.T) {
	a := New("claude", t.TempDir())
	dir := t.TempDir()
	src := filepath.Join(dir, "src.jsonl")
	read := `{"type":"user","uuid":"u-1","message":{"content":"hi"}}` + "\n" +
		`{"type":"mode","mode":"default"}` + "\n" +
		`{"type":"assistant","uuid":"u-2","message":{"content":[{"type":"tool_use","input":{"uuid":"nested"}}]}}` + "\n"
	later := `{"type":"user","uuid":"u-3","message":{"content":"sent after the fork"}}` + "\n"
	os.WriteFile(src, []byte(read+later), 0o600)

	l := &agent.Launch{Dir: dir, Args: []string{"--model", "haiku", "--verbose", "fix it"}, ForkOf: "src-id", Transcript: src, ForkOffset: int64(len(read)),
		Context: &pb.TurnContext{Model: "claude-sonnet-5-5", PermissionMode: "plan"}}
	argv, err := a.Argv(l)
	if err != nil {
		t.Fatal(err)
	}
	j := strings.Join(argv, " ")
	if l.SessionID == "" || l.SessionID == "src-id" || !strings.Contains(j, "--resume src-id --fork-session --session-id "+l.SessionID) ||
		!strings.Contains(j, "--verbose --model claude-sonnet-5-5 --permission-mode plan") || strings.Contains(j, "haiku") || strings.Contains(j, "fix it") {
		t.Fatalf("argv %q", argv)
	}

	copied := a.Copied(dir)
	if copied == nil {
		t.Fatal("no test for copied records")
	}
	for line, want := range map[string]bool{
		`{"type":"user","uuid":"u-1","sessionId":"new"}`: true,
		`{"type":"assistant","uuid":"u-2"}`:              true,
		// Recorded by the source after the daemon read it: the fork's to read.
		`{"type":"user","uuid":"u-3"}`: false,
		`{"type":"user","uuid":"u-9"}`: false,
		// A uuid inside a record is not the record's.
		`{"type":"user","uuid":"nested"}`:  false,
		`{"type":"mode","mode":"default"}`: false,
		`not json`:                         false,
	} {
		if copied([]byte(line)) != want {
			t.Errorf("copied(%s) = %v", line, !want)
		}
	}
	// A session that is not a fork has nothing to skip.
	if a.Copied(t.TempDir()) != nil {
		t.Fatal("a test without a fork")
	}
}
