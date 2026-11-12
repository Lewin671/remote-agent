package codex

import (
	"encoding/json"
	"testing"
)

func TestStaticCommandTitles(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"double unchanged", `tools.exec_command({cmd:"echo \"hello\"\nnext"})`, "echo \"hello\"\nnext"},
		{"single", `tools.exec_command({cmd:'echo \'hello\'\nnext'})`, "echo 'hello'\nnext"},
		{"single double quotes", `tools.exec_command({cmd:'echo "hello"'})`, `echo "hello"`},
		{"template heredoc", "tools.exec_command({cmd:`cat <<'EOF'\nfirst\nsecond\nEOF`})", "cat <<'EOF'\nfirst\nsecond\nEOF"},
		{"template escapes", "tools.exec_command({cmd:`echo \\`hi\\` \\${HOME} \\\\ \\nend`})", "echo `hi` ${HOME} \\ \nend"},
		{"unicode and hex", `tools.exec_command({cmd:'echo \x41\u4e2d\uD83D\uDE00\u{1F600}'})`, "echo A中😀😀"},
		{"controls and identity", `tools.exec_command({cmd:'a\tb\vc\bd\fe\rf\0g\/h\z'})`, "a\tb\vc\bd\fe\rf\x00g/hz"},
		{"continuation", "tools.exec_command({cmd:'echo first\\\nsecond'})", "echo firstsecond"},
		{"template CRLF", "tools.exec_command({cmd:`first\r\nsecond\rthird`})", "first\nsecond\nthird"},
		{"interpolation", "tools.exec_command({cmd:`echo ${value}`})", "exec"},
		{"variable", `tools.exec_command({cmd: command})`, "exec"},
		{"empty single", `tools.exec_command({cmd:''})`, "exec"},
		{"invalid escape", `tools.exec_command({cmd:'echo \xZZ'})`, "exec"},
		{"legacy octal", `tools.exec_command({cmd:'echo \123'})`, "exec"},
		{"unclosed", `tools.exec_command({cmd:'echo hi})`, "exec"},
		{"first command", "tools.exec_command({cmd:'first'}); tools.exec_command({cmd:`second`})", "first"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := toolTitle("exec", tc.input); got != tc.want {
				t.Fatalf("title = %q, want %q", got, tc.want)
			}
			p := NewParser()
			raw, _ := json.Marshal(map[string]any{"type": "response_item", "payload": map[string]any{"type": "custom_tool_call", "name": "exec", "call_id": "example", "input": tc.input}})
			evs, err := p.Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			if len(evs) != 1 || evs[0].GetToolStarted().GetTitle() != tc.want || evs[0].GetToolStarted().GetInput() != "" {
				t.Fatalf("start: %v", evs)
			}
			p.Parse([]byte(`{"type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"example","output":"Script running with cell ID 7"}}`))
			evs, err = p.Parse([]byte(`{"type":"event_msg","payload":{"type":"task_complete","turn_id":"turn"}}`))
			if err != nil {
				t.Fatal(err)
			}
			if len(evs) != 2 || evs[0].GetToolCall().GetTitle() != tc.want || evs[0].GetToolCall().GetInput() != "" {
				t.Fatalf("background: %v", evs)
			}
		})
	}
}
