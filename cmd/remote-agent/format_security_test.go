package main

import (
	"strings"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/codex"
)

// Tool output can originate in a repository or a fetched page. Reading the
// structured event log must not execute that output as terminal commands.
func TestEventLogEscapesTerminalCommands(t *testing.T) {
	p := codex.NewParser()
	events, err := p.Parse([]byte(`{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"AgentMessage","content":[{"text":"你好\n\u001b]52;c;YXR0YWNrZXI=\u0007\u001b[2J\r\b\u009b2J"}]}}}`))
	if err != nil || len(events) != 1 {
		t.Fatalf("parse: %v, %d events", err, len(events))
	}
	got := formatEvent(events[0])
	for _, r := range got {
		if r < 32 && r != '\n' && r != '\t' || r >= 127 && r <= 159 {
			t.Fatalf("event log contains active control %U: %q", r, got)
		}
	}
	if !strings.Contains(got, "你好\n") || !strings.Contains(got, `\x1b]52;c;YXR0YWNrZXI=\a`) {
		t.Fatalf("lost readable content: %q", got)
	}
}

func TestApprovalLogEscapesDisplaySpoofing(t *testing.T) {
	e := &pb.Event{Kind: &pb.Event_ApprovalRequested{ApprovalRequested: &pb.ApprovalRequested{
		ToolName: "shell", Command: "safe\r\x1b[2Kdangerous\u202ereversed\u2066isolated\u2069",
	}}}
	got := formatEvent(e)
	if strings.ContainsAny(got, "\r\x1b\u202e\u2066\u2069") {
		t.Fatalf("unsafe approval display: %q", got)
	}
}

func TestToolLogEscapesClipboardWrite(t *testing.T) {
	e := &pb.Event{Kind: &pb.Event_ToolCall{ToolCall: &pb.ToolCall{
		Title: "read untrusted file", Output: "file contents\n\x1b]52;c;YXR0YWNrZXI=\x1b\\",
	}}}
	got := formatEvent(e)
	if strings.ContainsRune(got, '\x1b') || !strings.Contains(got, `\x1b]52;c;YXR0YWNrZXI=`) {
		t.Fatalf("unsafe tool output: %q", got)
	}
}

func TestTerminalTextFieldPreservesDataWithoutLayout(t *testing.T) {
	input := "目录\tnew\nrow\roverwrite\x1b]52;c;YQ==\a\u009dtitle\u009c"
	got := terminalText(input, false)
	if strings.ContainsAny(got, "\t\n\r\x1b\a\u009d\u009c") {
		t.Fatalf("active controls in field: %q", got)
	}
	if !strings.Contains(got, `目录\tnew\nrow\roverwrite`) {
		t.Fatalf("field changed silently: %q", got)
	}
	if got := terminalText("普通文本 café 👩‍💻\n    indented\tcode", true); got != "普通文本 café 👩‍💻\n    indented\tcode" {
		t.Fatalf("safe text changed: %q", got)
	}
}
