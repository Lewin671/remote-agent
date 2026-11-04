package antigravity

import (
	"encoding/json"
	"fmt"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"path/filepath"

	"github.com/Lewin671/remote-agent/internal/agent"
)

// DecodeHook keeps the native payload contract in the adapter. Hook event names
// are set explicitly in our hook command; payload-shape inference would confuse
// Pre/PostToolUse and could mistake future lifecycle hooks for SessionStart.
func (a *Adapter) DecodeHook(raw []byte) (*agent.Hook, error) {
	var in struct {
		Event        string `json:"hook_event_name"`
		Conversation string `json:"conversationId"`
		Transcript   string `json:"transcriptPath"`
		Model        string `json:"modelName"`
		Step         *int64 `json:"stepIdx"`
		Termination  string `json:"terminationReason"`
		Tool         *struct {
			Name string          `json:"name"`
			Args json.RawMessage `json:"args"`
		} `json:"toolCall"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	h := &agent.Hook{SessionID: in.Conversation, TranscriptPath: in.Transcript, Model: in.Model, HookEventName: in.Event, TerminationReason: in.Termination}
	// Hooks report transcript_full; incremental reads deliberately use the compact
	// log and resolve only truncated fields from its full, line-aligned sibling.
	if filepath.Base(h.TranscriptPath) == "transcript_full.jsonl" {
		h.TranscriptPath = filepath.Join(filepath.Dir(h.TranscriptPath), "transcript.jsonl")
	}
	switch in.Event {
	case "PreToolUse":
		if in.Tool == nil || in.Step == nil {
			return nil, fmt.Errorf("incomplete tool hook")
		}
		h.ToolName, h.ToolInput = in.Tool.Name, normalizeArgs(in.Tool.Args)
		h.ToolUseID = fmt.Sprintf("step_%d", *in.Step)
	case "SessionStart", "Stop":
	default:
		return nil, fmt.Errorf("unknown agy hook event")
	}
	return h, nil
}

// Transcript args encode values as JSON strings, while hooks carry native
// values. Normalize both before computing digests or parsing questions.
func normalizeArgs(raw json.RawMessage) json.RawMessage {
	var values map[string]json.RawMessage
	if json.Unmarshal(raw, &values) != nil {
		return raw
	}
	for key, v := range values {
		var s string
		if json.Unmarshal(v, &s) == nil && json.Valid([]byte(s)) {
			values[key] = json.RawMessage(s)
		}
	}
	out, _ := json.Marshal(values)
	return out
}

func (a *Adapter) ApprovalHook(e *pb.Event) *agent.Hook {
	if t := e.GetToolStarted(); t != nil {
		return &agent.Hook{HookEventName: "PreToolUse", ToolName: t.ItemType, ToolInput: json.RawMessage(t.Input), ToolUseID: t.CallId, TurnID: e.TurnId}
	}
	return nil
}
