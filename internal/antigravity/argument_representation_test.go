package antigravity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Lewin671/remote-agent/internal/agent"
)

func TestCompactAndFullTranscriptArgumentsMatchNativeHooks(t *testing.T) {
	for _, command := range []string{"true", "null", "42", `{"nested":1}`, `[1,2]`} {
		for _, full := range []bool{false, true} {
			name := "compact/" + command
			if full {
				name = "full/" + command
			}
			t.Run(name, func(t *testing.T) {
				native := map[string]any{"CommandLine": command, "WaitMsBeforeAsync": 5000, "Enabled": false}
				nativeJSON, err := json.Marshal(native)
				if err != nil {
					t.Fatal(err)
				}
				encoded := map[string]string{}
				for k, v := range native {
					b, _ := json.Marshal(v)
					encoded[k] = string(b)
				}
				record := map[string]any{"step_index": 1, "type": "PLANNER_RESPONSE", "thinking": "cut", "tool_calls": []any{map[string]any{"name": "run_command", "args": encoded}}}
				root := t.TempDir()
				if full {
					complete := map[string]any{"step_index": 1, "type": "PLANNER_RESPONSE", "thinking": "complete", "tool_calls": []any{map[string]any{"name": "run_command", "args": native}}}
					b, _ := json.Marshal(complete)
					if err := os.WriteFile(filepath.Join(root, "transcript_full.jsonl"), append(b, '\n'), 0600); err != nil {
						t.Fatal(err)
					}
					record["truncated_fields"] = []string{"thinking"}
				}
				p := NewParser()
				p.SetTranscriptPath(filepath.Join(root, "transcript.jsonl"))
				raw, _ := json.Marshal(record)
				events, err := p.Parse(raw)
				if err != nil {
					t.Fatal(err)
				}
				for _, e := range events {
					if call := e.GetToolStarted(); call != nil {
						if call.InputDigest != agent.InputDigest("run_command", nativeJSON) {
							t.Fatalf("native hook arguments changed type: %s", call.Input)
						}
						return
					}
				}
				t.Fatal("tool call was not emitted")
			})
		}
	}
}
