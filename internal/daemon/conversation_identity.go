package daemon

import (
	"context"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"time"
)

// observeConversation distinguishes proving a newly launched/resumed process
// from an already verified process switching conversations locally.
func (s *session) observeConversation(ctx context.Context, id, path string, observed time.Time) bool {
	if ctx.Err() != nil {
		return false
	}
	if !s.ag.Capabilities().ConversationIdentity {
		return true
	}
	s.mu.Lock()
	if observed.Before(s.identityObservedAt) {
		s.mu.Unlock()
		return false
	}
	expected, pending, failed := s.meta.CodexSessionID, s.identityPending, s.identityFailed
	resume := s.identityResume
	if failed {
		s.mu.Unlock()
		return false
	}
	if id == "" {
		s.mu.Unlock()
		return !pending
	}
	if pending && resume && id != expected {
		s.identityFailed = true
		s.pauseConversationQueueLocked("requested conversation was not resumed")
		s.mu.Unlock()
		_ = s.emit(&pb.Event{Kind: &pb.Event_SessionState{SessionState: &pb.SessionState{Status: pb.SessionStatus_SESSION_STATUS_BLOCKED, Detail: "恢复的对话身份不匹配；待发送消息已暂停，请在终端处理"}}})
		s.withdrawNativeRequests("去终端处理：恢复的对话身份不匹配")
		return false
	}
	if pending && !resume && s.identityTarget != "" && id != s.identityTarget && id == expected {
		s.mu.Unlock()
		return false
	}
	if expected == "" && id != "" {
		if s.meta.InputConversations == nil {
			s.meta.InputConversations = map[string]string{}
		}
		changed := false
		for i, in := range s.queue.Queue {
			if in.Inflight != nil && in.CLIConversation == "" {
				s.queue.Queue[i].CLIConversation = id
				s.meta.InputConversations[in.RequestID] = id
				changed = true
			}
		}
		if changed {
			s.pruneInputConversationsLocked()
			_, _ = s.saveQueueLocked()
		}
	}
	currentPath := s.meta.RolloutPath
	if id != expected || (path != "" && path != currentPath) {
		s.identityPending = true
		s.identityTarget = id
	}
	s.mu.Unlock()
	if id == expected && (path == "" || path == currentPath) {
		s.mu.Lock()
		s.identityPending = false
		s.identityResume = false
		s.identityTarget = ""
		s.meta.ExpectedConversation = ""
		_ = s.meta.save(s.dir)
		if observed.After(s.identityObservedAt) {
			s.identityObservedAt = observed
		}
		s.mu.Unlock()
		s.poke()
		return true
	}
	// A remote injection and a native conversation switch cannot cross this
	// boundary while local terminal ownership is being acquired.
	s.ctlMu.Lock()
	defer s.ctlMu.Unlock()
	if ctx.Err() != nil {
		return false
	}
	s.mu.Lock()
	if observed.Before(s.identityObservedAt) {
		s.mu.Unlock()
		return false
	}
	s.identityObservedAt = observed
	previous := s.meta.CodexSessionID
	changed := previous != "" && previous != id
	s.mu.Unlock()
	if changed {
		_ = s.readRollout()
		s.withdrawNativeRequests("去终端处理：CLI 已切换到另一对话")
		s.readMu.Lock()
		s.mu.Lock()
		if s.running {
			_ = s.abortTurnLocked("CLI conversation changed locally")
		}
		var closed []*pb.Event
		for q := range s.questions {
			closed = append(closed, &pb.Event{Origin: originSystem, Kind: &pb.Event_QuestionAnswered{QuestionAnswered: &pb.QuestionAnswered{QuestionId: q}}})
		}
		s.pauseConversationQueueLocked("CLI conversation changed locally")
		s.mu.Unlock()
		s.readMu.Unlock()
		if len(closed) > 0 {
			_ = s.emit(closed...)
		}
	}
	s.bindRollout(id, path)
	s.mu.Lock()
	verified := s.meta.CodexSessionID == id && path != "" && s.meta.RolloutPath == path
	if verified {
		s.identityPending = false
		s.identityResume = false
		s.identityTarget = ""
		s.meta.ExpectedConversation = ""
		_ = s.meta.save(s.dir)
	}
	s.mu.Unlock()
	if !verified {
		return false
	}
	if changed {
		_ = s.emit(&pb.Event{Origin: originLocal, Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Text: "Antigravity 在终端切换了对话；已改为读取新对话，旧对话的远程审批和提问已撤回。", Phase: "commentary"}}})
	}
	s.poke()
	return true
}
func (s *session) pauseConversationQueueLocked(reason string) {
	var evs []*pb.Event
	for i, in := range s.queue.Queue {
		if !in.writing() {
			s.queue.Queue[i].Paused = true
			e := pausedState(s.queue.Queue[i])
			e.GetInputState().Reason = reason
			evs = append(evs, e)
		}
	}
	if len(evs) > 0 {
		_, _ = s.saveQueueLocked(evs...)
	}
}
func (s *session) withdrawNativeRequests(message string) {
	s.mu.Lock()
	var out []*approval
	for id, a := range s.approvals {
		if a.native != nil && !a.claimed {
			a.claimed = true
			a.withdrawMessage = message
			out = append(out, a)
			delete(s.approvals, id)
		}
	}
	clear(s.nativePending)
	clear(s.nativeCalls)
	_ = s.saveNativeLocked()
	s.mu.Unlock()
	for _, a := range out {
		a.decision <- decision{withdrawn: true, message: message}
	}
}
func (s *session) nativeIdentityReady() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.identityPending && !s.identityFailed
}

func (s *session) pruneInputConversationsLocked() {
	if len(s.meta.InputConversations) <= maxSeen {
		return
	}
	keep := map[string]bool{}
	for _, id := range s.queue.Seen {
		keep[id] = true
	}
	for _, list := range [][]input{s.queue.Queue, s.queue.Held, s.recent} {
		for _, in := range list {
			keep[in.RequestID] = true
		}
	}
	for id := range s.meta.InputConversations {
		if !keep[id] {
			delete(s.meta.InputConversations, id)
		}
	}
}
