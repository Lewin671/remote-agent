package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

// The daemon persists normalized request identities; only the adapter knows
// how they map to native payloads, transcript steps or terminal menus.
func (s *session) nativeRequestPath() string { return filepath.Join(s.dir, "native-requests.json") }
func (s *session) saveNativeLocked() error {
	b, err := json.Marshal(s.nativePending)
	if err != nil {
		return err
	}
	return writeFileSync(s.nativeRequestPath(), b)
}
func (s *session) loadNativeRequests() {
	if !s.ag.Capabilities().NativeApproval {
		return
	}
	b, err := os.ReadFile(s.nativeRequestPath())
	if err != nil {
		return
	}
	var pending map[string]*agent.Hook
	if json.Unmarshal(b, &pending) != nil {
		return
	}
	s.nativePending = map[string]*agent.Hook{}
	for id, h := range pending {
		call := s.nativeCalls[id]
		if h != nil && call != nil && h.SessionID == s.meta.CodexSessionID && sameNativeCall(h, call) {
			s.nativePending[id] = h
		}
	}
}
func sameNativeCall(a, b *agent.Hook) bool {
	return a != nil && b != nil && a.SessionID == b.SessionID && a.ToolUseID == b.ToolUseID && a.ToolName == b.ToolName && agent.InputDigest(a.ToolName, a.ToolInput) == agent.InputDigest(b.ToolName, b.ToolInput)
}
func (s *session) rememberNative(h *agent.Hook) {
	if !s.ag.Capabilities().NativeApproval {
		return
	}
	copy := *h
	copy.ToolInput = append([]byte(nil), h.ToolInput...)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nativePending == nil {
		s.nativePending = map[string]*agent.Hook{}
	}
	if old := s.nativePending[h.ToolUseID]; old != nil {
		copy.Uncertain = copy.Uncertain || old.Uncertain || !sameNativeCall(old, &copy)
	}
	if call := s.nativeCalls[h.ToolUseID]; call != nil && !sameNativeCall(call, &copy) {
		copy.Uncertain = true
	}
	s.nativePending[h.ToolUseID] = &copy
	if s.saveNativeLocked() != nil {
		copy.Uncertain = true
	}
}
func (s *session) observeNative(e *pb.Event) {
	source, ok := s.ag.(agent.NativeApprovalSource)
	if !ok {
		return
	}
	if h := source.ApprovalHook(e); h != nil {
		if s.nativeCalls == nil {
			s.nativeCalls = map[string]*agent.Hook{}
		}
		s.nativeCalls[h.ToolUseID] = h
		if hook := s.nativePending[h.ToolUseID]; hook != nil && !sameNativeCall(h, hook) {
			hook.Uncertain = true
		}
	}
	if call := e.GetToolCall(); call != nil && call.Background != pb.ToolBackground_TOOL_BACKGROUND_UNSPECIFIED && e.Seq > s.meta.BackgroundIdleSeq {
		s.backgroundPossible = true
		value := true
		s.meta.BackgroundPossible = &value
	}
	if call := e.GetToolCall(); call != nil {
		delete(s.nativeCalls, call.CallId)
		delete(s.nativePending, call.CallId)
	}
	if e.GetTurnEnded() != nil {
		clear(s.nativeCalls)
		clear(s.nativePending)
	}
}
func (s *session) nativeCandidatesLocked() []*agent.Hook {
	var out []*agent.Hook
	seen := map[string]bool{}
	for id, call := range s.nativeCalls {
		if call.SessionID != s.meta.CodexSessionID {
			continue
		}
		h := s.nativePending[id]
		if h == nil {
			copy := *call
			copy.Uncertain = true
			out = append(out, &copy)
		} else {
			copy := *h
			copy.Uncertain = copy.Uncertain || !sameNativeCall(h, call)
			out = append(out, &copy)
		}
		seen[id] = true
	}
	for id, h := range s.nativePending {
		if !seen[id] && h.SessionID == s.meta.CodexSessionID {
			copy := *h
			copy.Uncertain = true
			out = append(out, &copy)
		}
	}
	return out
}

func (s *session) withdrawNativeOne(a *approval, message string) {
	if !s.claim(a) {
		return
	}
	_ = s.emit(&pb.Event{TurnId: a.turnID, Kind: &pb.Event_ApprovalResolved{ApprovalResolved: &pb.ApprovalResolved{ApprovalId: a.id, Decision: pb.ApprovalDecision_APPROVAL_DECISION_CANCELED, Message: message}}})
	a.decision <- decision{recorded: true}
}
