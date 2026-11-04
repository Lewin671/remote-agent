package antigravity

import (
	"encoding/json"
	"fmt"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"os"
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
	h.TranscriptPath = a.transcriptFor(in.Conversation, in.Transcript)
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

// transcriptFor resolves the actual native state file. agy 1.5.0 can report
// antigravity/brain from statusLine while writing antigravity-cli/brain. The
// configured CLI state directory and existing file win over that stale alias.
func (a *Adapter) transcriptFor(conversation, reported string) string {
	if filepath.Base(reported) == "transcript_full.jsonl" {
		reported = filepath.Join(filepath.Dir(reported), "transcript.jsonl")
	}
	actual := ""
	if conversation != "" && filepath.Base(conversation) == conversation && conversation != ".." {
		actual = filepath.Join(a.home, "brain", conversation, ".system_generated", "logs", "transcript.jsonl")
	}
	for _, path := range []string{actual, reported} {
		if path == "" {
			continue
		}
		if st, err := os.Stat(path); err == nil && st.Mode().IsRegular() {
			if canonical, err := filepath.EvalSymlinks(path); err == nil {
				return canonical
			}
			return path
		}
	}
	if actual != "" {
		return actual
	}
	return reported
}
