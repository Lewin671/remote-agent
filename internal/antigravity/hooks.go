package antigravity

import (
	"encoding/json"
	"fmt"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"path/filepath"
	"strings"

	"github.com/Lewin671/remote-agent/internal/agent"
)

// DecodeHook keeps the native payload contract in the adapter. Hook event names
// are set explicitly in our hook command; payload-shape inference would confuse
// Pre/PostToolUse and could mistake future lifecycle hooks for SessionStart.
func (a *Adapter) DecodeHook(raw []byte) (*agent.Hook, error) {
	event, payload := agent.UnwrapHook(raw)
	var standard struct {
		Event string `json:"hook_event_name"`
	}
	_ = json.Unmarshal(payload, &standard)
	if standard.Event == "StatusLine" {
		return agent.DecodeStandardHook(payload)
	}
	var in struct {
		Event        string `json:"hook_event_name"`
		Conversation string `json:"conversationId"`
		Transcript   string `json:"transcriptPath"`
		Model        string `json:"modelName"`
		Step         *int64 `json:"stepIdx"`
		Termination  string `json:"terminationReason"`
		FullyIdle    *bool  `json:"fullyIdle"`
		Tool         *struct {
			Name string          `json:"name"`
			Args json.RawMessage `json:"args"`
		} `json:"toolCall"`
	}
	if err := json.Unmarshal(payload, &in); err != nil {
		return nil, err
	}
	if event != "" {
		in.Event = event
	}
	h := &agent.Hook{SessionID: in.Conversation, TranscriptPath: in.Transcript, Model: in.Model, HookEventName: in.Event, TerminationReason: in.Termination, StepIndex: in.Step, FullyIdle: in.FullyIdle}
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
		h.ToolName, h.ToolInput = in.Tool.Name, in.Tool.Args
		h.ToolUseID = nativeCallID(in.Conversation, *in.Step)
	case "SessionStart", "Stop":
	default:
		return nil, fmt.Errorf("unknown agy hook event")
	}
	return h, nil
}

func (a *Adapter) ApprovalHook(e *pb.Event) *agent.Hook {
	t := e.GetToolStarted()
	if t == nil {
		return nil
	}
	conversation, _, ok := strings.Cut(t.CallId, "/")
	if !ok {
		return nil
	}
	return &agent.Hook{HookEventName: "PreToolUse", SessionID: conversation, ToolName: t.ItemType, ToolInput: json.RawMessage(t.Input), ToolUseID: t.CallId, TurnID: e.TurnId}
}
func nativeCallID(conversation string, step int64) string {
	id := fmt.Sprintf("step_%d", step)
	if conversation != "" {
		return conversation + "/" + id
	}
	return id
}
