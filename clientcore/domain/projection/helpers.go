package projection

import (
	"slices"
	"strings"
	"unicode"

	"github.com/Lewin671/remote-agent/clientcore/domain/sortutil"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
)

// SkeletonNote marks terminal-only skeleton events and rewritten oversized IDs.
const SkeletonNote = "\n…（remote-agent：内容过大，请在终端查看/回答）"

// IsSkeletonText reports whether text carries the skeleton truncation marker.
func IsSkeletonText(text string) bool {
	return strings.HasSuffix(text, SkeletonNote)
}

func anySkeletonText(xs ...string) bool {
	for _, s := range xs {
		if IsSkeletonText(s) {
			return true
		}
	}
	return false
}

func isApprovalSkeleton(a *v1.ApprovalRequested) bool {
	if a == nil {
		return false
	}
	return anySkeletonText(a.Description, a.ApprovalId, a.Command, a.ToolInputJson, a.InputDigest)
}

func isQuestionSkeleton(q *v1.QuestionAsked) bool {
	if q == nil {
		return false
	}
	if IsSkeletonText(q.QuestionId) {
		return true
	}
	for _, item := range q.Questions {
		if item == nil {
			continue
		}
		if anySkeletonText(item.Id, item.Text, item.Header) {
			return true
		}
		for _, opt := range item.Options {
			if opt != nil && anySkeletonText(opt.Label, opt.Description) {
				return true
			}
		}
	}
	return false
}

// IsQuestionSkeleton reports whether a pending QuestionAsked carries the skeleton marker.
func IsQuestionSkeleton(q *v1.QuestionAsked) bool {
	return isQuestionSkeleton(q)
}

// IsSkeleton reports whether an event carries SkeletonNote in its payload or rewritten IDs.
func IsSkeleton(e *v1.Event) bool {
	if e == nil {
		return false
	}
	if anySkeletonText(e.SessionId, e.TurnId, e.GetOrigin().GetDeviceId(), e.GetOrigin().GetRequestId()) {
		return true
	}
	switch k := e.Kind.(type) {
	case *v1.Event_UserMessage:
		return IsSkeletonText(k.UserMessage.GetText())
	case *v1.Event_AgentMessage:
		return IsSkeletonText(k.AgentMessage.GetText())
	case *v1.Event_Reasoning:
		return IsSkeletonText(k.Reasoning.GetText())
	case *v1.Event_ToolCall:
		return anySkeletonText(k.ToolCall.GetTitle(), k.ToolCall.GetCallId())
	case *v1.Event_ScheduledNotification:
		return IsSkeletonText(k.ScheduledNotification.GetText())
	case *v1.Event_TurnEnded:
		return IsSkeletonText(k.TurnEnded.GetReason())
	case *v1.Event_TokenUsage:
		return IsSkeletonText(k.TokenUsage.GetPlanType())
	case *v1.Event_TurnContext:
		return IsSkeletonText(k.TurnContext.GetModel())
	case *v1.Event_ApprovalRequested:
		return isApprovalSkeleton(k.ApprovalRequested)
	case *v1.Event_ApprovalResolved:
		return anySkeletonText(k.ApprovalResolved.GetMessage(), k.ApprovalResolved.GetApprovalId())
	case *v1.Event_QuestionAsked:
		return isQuestionSkeleton(k.QuestionAsked)
	case *v1.Event_QuestionAnswered:
		if IsSkeletonText(k.QuestionAnswered.GetQuestionId()) {
			return true
		}
		for _, ans := range k.QuestionAnswered.GetAnswers() {
			if ans != nil && (IsSkeletonText(ans.QuestionId) || anySkeletonText(ans.Answers...)) {
				return true
			}
		}
		return false
	case *v1.Event_SessionState:
		return IsSkeletonText(k.SessionState.GetDetail())
	case *v1.Event_InputState:
		return anySkeletonText(k.InputState.GetText(), k.InputState.GetRequestId())
	case *v1.Event_PlanProposed:
		return anySkeletonText(k.PlanProposed.GetText(), k.PlanProposed.GetCallId())
	case *v1.Event_SettingsChanged:
		return IsSkeletonText(k.SettingsChanged.GetModel())
	case *v1.Event_ToolStarted:
		return anySkeletonText(k.ToolStarted.GetTitle(), k.ToolStarted.GetCallId(), k.ToolStarted.GetInputDigest())
	case *v1.Event_ImageGenerated:
		return anySkeletonText(k.ImageGenerated.GetRevisedPrompt(), k.ImageGenerated.GetCallId())
	case *v1.Event_SessionForked:
		return anySkeletonText(k.SessionForked.GetSourceTitle(), k.SessionForked.GetSourceSessionId())
	default:
		return false
	}
}

// EventTimeMs converts an event timestamp to Unix milliseconds, or 0 if unset.
func EventTimeMs(e *v1.Event) int64 {
	if e == nil || e.Time == nil {
		return 0
	}
	return e.Time.Seconds*1000 + int64(e.Time.Nanos)/1_000_000
}

// WentToBackground reports whether a ToolCall continued running in the background.
func WentToBackground(c *v1.ToolCall) bool {
	return c != nil && c.Background != v1.ToolBackground_TOOL_BACKGROUND_UNSPECIFIED
}

// UsagePercent returns the clamped integer percentage (0..100) of context window occupancy, or nil if unknown.
func UsagePercent(u *v1.TokenUsage) *int32 {
	if u == nil || u.ContextWindow <= 0 {
		return nil
	}
	pct := (u.LastTotalTokens * 100) / u.ContextWindow
	if pct > 100 {
		pct = 100
	}
	v := int32(pct)
	return &v
}

// ModeOf returns m if it is one of the four client approval modes, else APPROVAL_MODE_UNSPECIFIED.
func ModeOf(m v1.ApprovalMode) v1.ApprovalMode {
	switch m {
	case v1.ApprovalMode_APPROVAL_MODE_ASK,
		v1.ApprovalMode_APPROVAL_MODE_AUTO,
		v1.ApprovalMode_APPROVAL_MODE_FULL_ACCESS,
		v1.ApprovalMode_APPROVAL_MODE_PLAN:
		return m
	default:
		return v1.ApprovalMode_APPROVAL_MODE_UNSPECIFIED
	}
}

// PermissionMode maps a Claude Code permission_mode string to ApprovalMode, or APPROVAL_MODE_UNSPECIFIED for CLI-specific modes.
func PermissionMode(pm string) v1.ApprovalMode {
	switch pm {
	case "default":
		return v1.ApprovalMode_APPROVAL_MODE_ASK
	case "auto":
		return v1.ApprovalMode_APPROVAL_MODE_AUTO
	case "plan":
		return v1.ApprovalMode_APPROVAL_MODE_PLAN
	case "bypassPermissions":
		return v1.ApprovalMode_APPROVAL_MODE_FULL_ACCESS
	default:
		return v1.ApprovalMode_APPROVAL_MODE_UNSPECIFIED
	}
}

// ContextMode maps TurnContext approval fields to ApprovalMode, or APPROVAL_MODE_UNSPECIFIED if absent.
func ContextMode(reviewer, policy, sandbox, collaboration string) v1.ApprovalMode {
	if reviewer == "" && policy == "" && collaboration == "" {
		return v1.ApprovalMode_APPROVAL_MODE_UNSPECIFIED
	}
	if collaboration == "plan" {
		return v1.ApprovalMode_APPROVAL_MODE_PLAN
	}
	if sandbox == "danger-full-access" && policy == "never" {
		return v1.ApprovalMode_APPROVAL_MODE_FULL_ACCESS
	}
	if reviewer == "auto_review" {
		return v1.ApprovalMode_APPROVAL_MODE_AUTO
	}
	return v1.ApprovalMode_APPROVAL_MODE_ASK
}

// ModelIs reports whether a CLI-reported model ID matches the chosen slug or alias.
func ModelIs(id, slug string) bool {
	if id == "" || slug == "" {
		return false
	}
	if id == slug {
		return true
	}
	toks := strings.FieldsFunc(id, func(r rune) bool {
		return r == '-' || r == '_' || r == '.' || unicode.IsSpace(r)
	})
	for _, tok := range toks {
		if strings.EqualFold(tok, slug) {
			return true
		}
	}
	return false
}

// ReconcilesUnclaimedInput reports whether the CLI can emit UserMessage before InputState(SUBMITTED).
func ReconcilesUnclaimedInput(agent v1.Agent) bool {
	return agent == v1.Agent_AGENT_CLAUDE
}

// AwaitingApproval reports whether a running tool matches any pending approval request.
func AwaitingApproval(t *corev1.RunningTool, approvals []*corev1.PendingApproval) bool {
	if t == nil {
		return false
	}
	for _, pa := range approvals {
		a := pa.GetRequest()
		if a == nil {
			continue
		}
		if t.InputDigest != "" {
			if a.InputDigest != "" && a.InputDigest == t.InputDigest {
				return true
			}
		} else if a.InputDigest == "" && a.Command != "" && (a.Command == t.Input || a.Command == t.Title) {
			return true
		}
	}
	return false
}

// PendingInputs extracts inputs the model has not received yet, in acceptance order.
func PendingInputs(inputs []*corev1.InputProjection) []*corev1.PendingInput {
	var out []*corev1.PendingInput
	for _, in := range inputs {
		st := in.GetDetail()
		if st == nil {
			continue
		}
		if st.Status != v1.InputStatus_INPUT_STATUS_QUEUED && st.Status != v1.InputStatus_INPUT_STATUS_HELD_BY_CLI {
			continue
		}
		text := st.Text
		var origin *v1.Origin
		var files []*v1.SentFile
		if acc := in.GetAccepted(); acc != nil {
			text = acc.Text
			origin = acc.Origin
			files = acc.Files
		}
		if len(files) == 0 {
			files = st.Files
		}
		mode := st.Mode
		if mode == v1.SendMode_SEND_MODE_UNSPECIFIED {
			mode = v1.SendMode_SEND_MODE_STEER
		}
		out = append(out, &corev1.PendingInput{
			RequestId: st.RequestId,
			Text:      text,
			Mode:      mode,
			Paused:    st.Paused,
			Held:      st.Status == v1.InputStatus_INPUT_STATUS_HELD_BY_CLI,
			Origin:    origin,
			Files:     slices.Clone(files),
		})
	}
	return out
}

// LivePlanText resolves the markdown plan text referenced by a live snapshot from history, falling back to plan_screen.
func LivePlanText(live *v1.LiveSnapshot, events []*v1.Event) string {
	if live == nil {
		return ""
	}
	seq := live.PlanEventSeq
	var e *v1.Event
	if seq > 0 && int(seq-1) < len(events) {
		cand := events[seq-1]
		if cand != nil && cand.Seq == seq {
			e = cand
		}
	}
	if e == nil {
		for i := len(events) - 1; i >= 0; i-- {
			if events[i] != nil && events[i].Seq == seq {
				e = events[i]
				break
			}
		}
	}
	var text string
	if e != nil && e.GetPlanProposed() != nil {
		text = e.GetPlanProposed().Text
	}
	if seq > 0 && strings.TrimSpace(text) != "" && !IsSkeletonText(text) {
		return text
	}
	return live.PlanScreen
}

// ApprovalCallIDs returns unambiguous daemon-digest associations from ToolCall event seq to ApprovalRequested ID.
func ApprovalCallIDs(events []*v1.Event, resolved map[string]*corev1.ResolvedApproval) map[uint64]string {
	type key struct {
		name   string
		digest string
	}
	type req struct {
		id  string
		seq uint64
	}
	reqs := make(map[key][]req)
	calls := make(map[key][]uint64)
	for _, e := range events {
		if e == nil {
			continue
		}
		switch k := e.Kind.(type) {
		case *v1.Event_ApprovalRequested:
			a := k.ApprovalRequested
			if a.InputDigest != "" {
				dk := key{a.ToolName, a.InputDigest}
				reqs[dk] = append(reqs[dk], req{a.ApprovalId, e.Seq})
			}
		case *v1.Event_ToolCall:
			t := k.ToolCall
			if t.InputDigest != "" {
				dk := key{t.ItemType, t.InputDigest}
				calls[dk] = append(calls[dk], e.Seq)
			}
		}
	}
	out := make(map[uint64]string)
	for dk, rs := range reqs {
		cs := calls[dk]
		if len(rs) == 1 && len(cs) == 1 && cs[0] > rs[0].seq {
			if _, ok := resolved[rs[0].id]; ok {
				out[cs[0]] = rs[0].id
			}
		}
	}
	return out
}

func retainInputDetails(st, before *v1.InputState) *v1.InputState {
	res := st.Clone()
	if before != nil {
		if res.Mode == v1.SendMode_SEND_MODE_UNSPECIFIED {
			res.Mode = before.Mode
		}
		if res.Text == "" {
			res.Text = before.Text
		}
		if len(res.Files) == 0 && len(before.Files) > 0 {
			res.Files = slices.Clone(before.Files)
		}
		if len(res.ImageIds) == 0 && len(before.ImageIds) > 0 {
			res.ImageIds = slices.Clone(before.ImageIds)
		}
	}
	return res
}

func retainAccepted(prior, observed *corev1.AcceptedInput) *corev1.AcceptedInput {
	out := &corev1.AcceptedInput{
		Text:     prior.Text,
		ImageIds: prior.ImageIds,
		Origin:   prior.Origin,
		Stage:    prior.Stage,
		Files:    prior.Files,
		TimeMs:   prior.TimeMs,
	}
	if out.Origin == nil {
		out.Origin = observed.Origin
	}
	if observed.Stage != v1.InputStage_INPUT_STAGE_UNSPECIFIED {
		out.Stage = observed.Stage
	}
	if len(out.Files) == 0 {
		out.Files = observed.Files
	}
	if len(out.ImageIds) == 0 {
		out.ImageIds = observed.ImageIds
	}
	return out
}

func cloneAccepted(a *corev1.AcceptedInput) *corev1.AcceptedInput {
	if a == nil {
		return nil
	}
	return &corev1.AcceptedInput{
		Text:     a.Text,
		ImageIds: slices.Clone(a.ImageIds),
		Origin:   a.Origin,
		Stage:    a.Stage,
		Files:    slices.Clone(a.Files),
		TimeMs:   a.TimeMs,
	}
}

func cloneRunningTool(t *corev1.RunningTool) *corev1.RunningTool {
	if t == nil {
		return nil
	}
	cp := *t
	return &cp
}

func sortedSeqs(m map[uint64]struct{}) []uint64 {
	if len(m) == 0 {
		return nil
	}
	out := make([]uint64, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sortutil.Uint64s(out)
	return out
}
