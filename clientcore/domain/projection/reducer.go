package projection

import (
	"slices"
	"strings"

	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
)

// DefaultAgent is the CLI assumed when an event stream or session leaves Agent unspecified.
const DefaultAgent = v1.Agent_AGENT_CODEX

type modeValue struct {
	known v1.ApprovalMode
	raw   string
}

func (m modeValue) empty() bool {
	return m.known == v1.ApprovalMode_APPROVAL_MODE_UNSPECIFIED && m.raw == ""
}

type compactionState struct {
	before *int32
	after  *int32
}

type digestKey struct {
	name   string
	digest string
}

type approvalReqRecord struct {
	id  string
	seq uint64
}

// Reducer incrementally folds ordered session events into a ProjectionState.
// It is pure in-memory logic: no I/O, goroutines, clocks, or logging.
type Reducer struct {
	agent               v1.Agent
	reconcilesUnclaimed bool

	approvals orderedMap[string, *v1.ApprovalRequested]
	resolved  map[string]*ResolvedApproval

	questions     orderedMap[string, *v1.QuestionAsked]
	questionSeqs  map[string]uint64
	questionTimes map[string]int64
	answered      map[string]*AnsweredQuestion

	inputs       orderedMap[string, v1.InputStatus]
	inputSeqs    map[string]uint64
	inputDetails map[string]*v1.InputState
	accepted     map[string]*AcceptedInput
	failures     map[string]string
	unsent       orderedMap[string, uint64]

	running         bool
	runningSeq      uint64
	scheduledClosed bool
	idleClosed      bool

	usage       *v1.TokenUsage
	compactions map[uint64]compactionState
	plan        *v1.PlanProposed
	plansBySeq  map[uint64]string

	tools           []*RunningTool
	background      []*BackgroundTool
	completedAt     map[uint64]uint64
	unfinished      map[uint64]struct{}
	noResult        map[uint64]struct{}
	typedLocally    map[uint64]struct{}
	terminalChanges map[uint64][]*TerminalChange

	// Continuation state across batches.
	mode           modeValue
	model          string
	hasExpected    bool
	expectedModel  string
	expectedEffort string
	submitted      int
	hasUnclaimed   bool
	unclaimedSeq   uint64
	queuedAt       map[string]uint64
	hasCompacting  bool
	compactingSeq  uint64
	planned        bool
	unfinishedByID map[string]*RunningTool

	// Historical disambiguation index for approval-to-call matching.
	approvalReqsByDigest map[digestKey][]approvalReqRecord
	toolCallsByDigest    map[digestKey][]uint64
}

// NewReducer returns a new Reducer configured for the given CLI agent.
func NewReducer(agent v1.Agent) *Reducer {
	r := &Reducer{}
	r.Reset(agent)
	return r
}

// Reset clears all state and reconfigures the reducer for agent.
func (r *Reducer) Reset(agent v1.Agent) {
	if agent == v1.Agent_AGENT_UNSPECIFIED {
		agent = DefaultAgent
	}
	r.agent = agent
	r.reconcilesUnclaimed = ReconcilesUnclaimedInput(agent)

	r.approvals.clear()
	r.resolved = make(map[string]*ResolvedApproval)
	r.questions.clear()
	r.questionSeqs = make(map[string]uint64)
	r.questionTimes = make(map[string]int64)
	r.answered = make(map[string]*AnsweredQuestion)
	r.inputs.clear()
	r.inputSeqs = make(map[string]uint64)
	r.inputDetails = make(map[string]*v1.InputState)
	r.accepted = make(map[string]*AcceptedInput)
	r.failures = make(map[string]string)
	r.unsent.clear()

	r.running = false
	r.runningSeq = 0
	r.scheduledClosed = false
	r.idleClosed = false

	r.usage = nil
	r.compactions = make(map[uint64]compactionState)
	r.plan = nil
	r.plansBySeq = make(map[uint64]string)

	r.tools = nil
	r.background = nil
	r.completedAt = make(map[uint64]uint64)
	r.unfinished = make(map[uint64]struct{})
	r.noResult = make(map[uint64]struct{})
	r.typedLocally = make(map[uint64]struct{})
	r.terminalChanges = make(map[uint64][]*TerminalChange)

	r.mode = modeValue{}
	r.model = ""
	r.hasExpected = false
	r.expectedModel = ""
	r.expectedEffort = ""
	r.submitted = 0
	r.hasUnclaimed = false
	r.unclaimedSeq = 0
	r.queuedAt = make(map[string]uint64)
	r.hasCompacting = false
	r.compactingSeq = 0
	r.planned = false
	r.unfinishedByID = make(map[string]*RunningTool)
	r.approvalReqsByDigest = make(map[digestKey][]approvalReqRecord)
	r.toolCallsByDigest = make(map[digestKey][]uint64)
}

// Append folds an ordered batch of events into the reducer.
func (r *Reducer) Append(events []*v1.Event) {
	for _, e := range events {
		if e != nil {
			r.reduceOne(e)
		}
	}
}

func (r *Reducer) reduceOne(e *v1.Event) {
	switch k := e.Kind.(type) {
	case *v1.Event_ApprovalRequested:
		a := k.ApprovalRequested
		r.approvals.set(a.ApprovalId, a)
		if a.InputDigest != "" {
			dk := digestKey{a.ToolName, a.InputDigest}
			r.approvalReqsByDigest[dk] = append(r.approvalReqsByDigest[dk], approvalReqRecord{
				id:  a.ApprovalId,
				seq: e.Seq,
			})
		}

	case *v1.Event_ApprovalResolved:
		ar := k.ApprovalResolved
		r.approvals.delete(ar.ApprovalId)
		r.resolved[ar.ApprovalId] = &ResolvedApproval{
			Decision: ar.Decision,
			Message:  ar.Message,
			Origin:   e.Origin,
		}

	case *v1.Event_QuestionAsked:
		q := k.QuestionAsked
		if !q.Async {
			r.questions.set(q.QuestionId, q)
			r.questionSeqs[q.QuestionId] = e.Seq
			r.questionTimes[q.QuestionId] = EventTimeMs(e)
		}

	case *v1.Event_QuestionAnswered:
		qa := k.QuestionAnswered
		r.questions.delete(qa.QuestionId)
		delete(r.questionSeqs, qa.QuestionId)
		delete(r.questionTimes, qa.QuestionId)
		var flat []string
		for _, ans := range qa.Answers {
			flat = append(flat, ans.Answers...)
		}
		r.answered[qa.QuestionId] = &AnsweredQuestion{
			Answers: flat,
			Origin:  e.Origin,
		}

	case *v1.Event_TurnStarted:
		r.running = true
		r.runningSeq = e.Seq
		r.background = nil

	case *v1.Event_TurnEnded:
		r.running = false
		r.runningSeq = e.Seq
		r.questions.clear()
		clear(r.questionSeqs)
		clear(r.questionTimes)
		aborted := k.TurnEnded.Outcome == v1.TurnOutcome_TURN_OUTCOME_ABORTED
		for _, t := range r.tools {
			if aborted {
				r.background = append(r.background, &BackgroundTool{
					Tool:     t,
					Returned: false,
				})
			} else {
				r.noResult[t.Seq] = struct{}{}
			}
			r.unfinished[t.Seq] = struct{}{}
			if t.CallId != "" {
				r.unfinishedByID[t.CallId] = t
			}
		}
		r.tools = nil

	case *v1.Event_InputState:
		st := k.InputState
		prevStatus, hasPrevStatus := r.inputs.get(st.RequestId)
		late := hasPrevStatus && prevStatus == v1.InputStatus_INPUT_STATUS_UNCONFIRMED
		r.inputs.set(st.RequestId, st.Status)
		r.inputSeqs[st.RequestId] = e.Seq
		r.inputDetails[st.RequestId] = retainInputDetails(st, r.inputDetails[st.RequestId])

		prevAcc := r.accepted[st.RequestId]
		carriesInput := st.Text != "" || len(st.ImageIds) > 0 || len(st.Files) > 0
		if (st.Status == v1.InputStatus_INPUT_STATUS_QUEUED || st.Status == v1.InputStatus_INPUT_STATUS_HELD_BY_CLI) && carriesInput {
			obs := &AcceptedInput{
				Text:     st.Text,
				ImageIds: slices.Clone(st.ImageIds),
				Origin:   e.Origin,
				Stage:    st.Stage,
				Files:    slices.Clone(st.Files),
				TimeMs:   EventTimeMs(e),
			}
			if prevAcc == nil {
				r.accepted[st.RequestId] = obs
			} else {
				r.accepted[st.RequestId] = retainAccepted(prevAcc, obs)
			}
		} else if prevAcc != nil && st.Stage != v1.InputStage_INPUT_STAGE_UNSPECIFIED {
			cp := cloneAccepted(prevAcc)
			cp.Stage = st.Stage
			r.accepted[st.RequestId] = cp
		}

		if st.Status == v1.InputStatus_INPUT_STATUS_FAILED || st.Status == v1.InputStatus_INPUT_STATUS_NEEDS_TERMINAL {
			r.failures[st.RequestId] = st.Reason
		}
		if st.Status == v1.InputStatus_INPUT_STATUS_CANCELED && st.Reason == "exited" {
			r.unsent.set(st.RequestId, e.Seq)
		}
		if st.Status == v1.InputStatus_INPUT_STATUS_QUEUED {
			if _, exists := r.queuedAt[st.RequestId]; !exists {
				r.queuedAt[st.RequestId] = e.Seq
			}
		}
		if st.Status == v1.InputStatus_INPUT_STATUS_SUBMITTED {
			since, hasSince := r.queuedAt[st.RequestId]
			if !late && r.reconcilesUnclaimed && r.hasUnclaimed && e.Seq >= r.unclaimedSeq && e.Seq-r.unclaimedSeq <= 3 && (!hasSince || r.unclaimedSeq > since) {
				delete(r.typedLocally, r.unclaimedSeq)
				r.hasUnclaimed = false
				r.unclaimedSeq = 0
			} else {
				r.submitted++
			}
		}

	case *v1.Event_UserMessage:
		um := k.UserMessage
		if e.Origin != nil && e.Origin.Kind != v1.OriginKind_ORIGIN_KIND_UNSPECIFIED {
			if e.Origin.Kind == v1.OriginKind_ORIGIN_KIND_LOCAL {
				r.typedLocally[e.Seq] = struct{}{}
			}
			if r.submitted > 0 && um.RequestId != "" {
				r.submitted--
			}
		} else {
			if r.submitted > 0 {
				r.submitted--
			} else if um.Text != "Implement the plan." {
				r.typedLocally[e.Seq] = struct{}{}
				r.hasUnclaimed = true
				r.unclaimedSeq = e.Seq
			}
		}

	case *v1.Event_ToolStarted:
		ts := k.ToolStarted
		r.tools = append(r.tools, &RunningTool{
			Title:       ts.Title,
			CallId:      ts.CallId,
			SinceMs:     EventTimeMs(e),
			Kind:        ts.Kind,
			ItemType:    ts.ItemType,
			Seq:         e.Seq,
			Input:       ts.Input,
			InputDigest: ts.InputDigest,
		})

	case *v1.Event_ToolCall:
		c := k.ToolCall
		if c.InputDigest != "" {
			dk := digestKey{c.ItemType, c.InputDigest}
			r.toolCallsByDigest[dk] = append(r.toolCallsByDigest[dk], e.Seq)
		}
		id := c.CallId
		matched := false
		if id != "" {
			if j := slices.IndexFunc(r.tools, func(t *RunningTool) bool { return t.CallId == id }); j >= 0 {
				matched = true
				t := r.tools[j]
				r.tools = slices.Delete(r.tools, j, j+1)
				r.completedAt[t.Seq] = e.Seq
				if WentToBackground(c) {
					r.background = append(r.background, &BackgroundTool{
						Tool:         t,
						Returned:     true,
						Background:   c.Background,
						BackgroundId: c.BackgroundId,
					})
				}
			} else if t, ok := r.unfinishedByID[id]; ok {
				if _, done := r.completedAt[t.Seq]; !done {
					matched = true
					r.completedAt[t.Seq] = e.Seq
					delete(r.unfinished, t.Seq)
					delete(r.noResult, t.Seq)
					r.background = slices.DeleteFunc(r.background, func(b *BackgroundTool) bool {
						return b.Tool != nil && b.Tool.Seq == t.Seq
					})
					if WentToBackground(c) {
						r.background = append(r.background, &BackgroundTool{
							Tool:         t,
							Returned:     true,
							Background:   c.Background,
							BackgroundId: c.BackgroundId,
						})
					}
				}
			}
		}
		if !matched && WentToBackground(c) {
			t := &RunningTool{
				Title:       c.Title,
				CallId:      id,
				SinceMs:     EventTimeMs(e),
				Kind:        c.Kind,
				ItemType:    c.ItemType,
				Seq:         e.Seq,
				Input:       c.Input,
				InputDigest: c.InputDigest,
			}
			r.background = append(r.background, &BackgroundTool{
				Tool:         t,
				Returned:     true,
				Background:   c.Background,
				BackgroundId: c.BackgroundId,
			})
		}

	case *v1.Event_TokenUsage:
		r.usage = k.TokenUsage
		if r.hasCompacting {
			if c, ok := r.compactions[r.compactingSeq]; ok {
				c.after = UsagePercent(k.TokenUsage)
				r.compactions[r.compactingSeq] = c
			}
			r.hasCompacting = false
			r.compactingSeq = 0
		}

	case *v1.Event_PlanProposed:
		r.plan = k.PlanProposed
		r.plansBySeq[e.Seq] = k.PlanProposed.Text
		r.planned = true

	case *v1.Event_SettingsChanged:
		c := k.SettingsChanged
		if c.Compacted {
			r.compactions[e.Seq] = compactionState{before: UsagePercent(r.usage)}
			r.compactingSeq = e.Seq
			r.hasCompacting = true
		}
		if c.Model != "" {
			r.hasExpected = true
			r.expectedModel = c.Model
			r.expectedEffort = c.Effort
		}
		if m := ModeOf(c.Mode); m != v1.ApprovalMode_APPROVAL_MODE_UNSPECIFIED {
			r.mode = modeValue{known: m}
		}

	case *v1.Event_TurnContext:
		c := k.TurnContext
		var m modeValue
		if c.PermissionMode != "" {
			if km := PermissionMode(c.PermissionMode); km != v1.ApprovalMode_APPROVAL_MODE_UNSPECIFIED {
				m = modeValue{known: km}
			} else {
				m = modeValue{raw: c.PermissionMode}
			}
		} else if km := ContextMode(c.ApprovalsReviewer, c.ApprovalPolicy, c.Sandbox, c.CollaborationMode); km != v1.ApprovalMode_APPROVAL_MODE_UNSPECIFIED {
			m = modeValue{known: km}
		}
		planning := c.CollaborationMode == "plan" || c.PermissionMode == "plan"
		mm := c.Model + " " + c.Effort
		var changes []*TerminalChange
		if !r.mode.empty() && !m.empty() && m != r.mode && !(r.mode.known == v1.ApprovalMode_APPROVAL_MODE_PLAN && r.planned) {
			changes = append(changes, &TerminalChange{
				Kind: &TerminalChangeMode{
					Mode: &TerminalModeChange{
						Mode:    m.known,
						RawMode: m.raw,
					},
				},
			})
		}
		if !m.empty() && m != r.mode {
			r.planned = false
		}
		var changed bool
		if r.hasExpected {
			changed = !(ModelIs(c.Model, r.expectedModel) &&
				(r.expectedEffort == "" || c.Effort == "" || c.Effort == r.expectedEffort))
		} else {
			changed = mm != r.model
		}
		if (r.model != "" || r.hasExpected) && c.Model != "" && changed && !planning {
			changes = append(changes, &TerminalChange{
				Kind: &TerminalChangeModel{
					Model: &TerminalModelChange{
						Model:  c.Model,
						Effort: c.Effort,
					},
				},
			})
		}
		if c.Model != "" && !planning {
			r.hasExpected = false
			r.expectedModel = ""
			r.expectedEffort = ""
		}
		if len(changes) > 0 {
			r.terminalChanges[e.Seq] = changes
		}
		if !m.empty() {
			r.mode = m
		}
		if !planning && c.Model != "" {
			r.model = mm
		}

	case *v1.Event_SessionState:
		s := k.SessionState
		r.scheduledClosed = s.Status == v1.SessionStatus_SESSION_STATUS_EXITED && s.ScheduledClosed
		r.idleClosed = s.Status == v1.SessionStatus_SESSION_STATUS_EXITED && s.IdleClosed
		if s.Status == v1.SessionStatus_SESSION_STATUS_EXITED {
			r.endOpenState(e.Seq)
		}

	case *v1.Event_SessionForked:
		r.endOpenState(e.Seq)
		r.submitted = 0
		r.hasUnclaimed = false
		r.unclaimedSeq = 0
		clear(r.unfinishedByID)
	}
}

func (r *Reducer) endOpenState(seq uint64) {
	r.running = false
	r.runningSeq = seq
	r.approvals.clear()
	r.questions.clear()
	clear(r.questionSeqs)
	clear(r.questionTimes)
	for _, t := range r.tools {
		r.unfinished[t.Seq] = struct{}{}
		r.noResult[t.Seq] = struct{}{}
		if t.CallId != "" {
			r.unfinishedByID[t.CallId] = t
		}
	}
	r.tools = nil
	r.background = nil
}

// Snapshot returns an immutable ProjectionState representing the reducer's current state.
func (r *Reducer) Snapshot() *State {
	out := &State{
		Running:          r.running,
		RunningSeq:       r.runningSeq,
		ScheduledClosed:  r.scheduledClosed,
		IdleClosed:       r.idleClosed,
		Usage:            r.usage,
		Plan:             r.plan,
		UnfinishedSeqs:   sortedSeqs(r.unfinished),
		NoResultSeqs:     sortedSeqs(r.noResult),
		TypedLocallySeqs: sortedSeqs(r.typedLocally),
	}

	if len(r.approvals.keys) > 0 {
		out.Approvals = make([]*PendingApproval, len(r.approvals.keys))
		for i, id := range r.approvals.keys {
			out.Approvals[i] = &PendingApproval{
				ApprovalId: id,
				Request:    r.approvals.vals[i],
			}
		}
	}

	if len(r.resolved) > 0 {
		out.Resolved = make(map[string]*ResolvedApproval, len(r.resolved))
		for k, v := range r.resolved {
			cp := *v
			out.Resolved[k] = &cp
		}
	}

	if len(r.questions.keys) > 0 {
		out.Questions = make([]*PendingQuestion, len(r.questions.keys))
		for i, id := range r.questions.keys {
			out.Questions[i] = &PendingQuestion{
				QuestionId: id,
				Request:    r.questions.vals[i],
				Seq:        r.questionSeqs[id],
				AskedAtMs:  r.questionTimes[id],
			}
		}
	}

	if len(r.answered) > 0 {
		out.Answered = make(map[string]*AnsweredQuestion, len(r.answered))
		for k, v := range r.answered {
			out.Answered[k] = &AnsweredQuestion{
				Answers: slices.Clone(v.Answers),
				Origin:  v.Origin,
			}
		}
	}

	if len(r.inputs.keys) > 0 {
		out.Inputs = make([]*InputProjection, len(r.inputs.keys))
		for i, id := range r.inputs.keys {
			ip := &InputProjection{
				RequestId: id,
				Status:    r.inputs.vals[i],
				LastSeq:   r.inputSeqs[id],
				Detail:    r.inputDetails[id],
				Accepted:  cloneAccepted(r.accepted[id]),
			}
			if reason, ok := r.failures[id]; ok {
				cp := reason
				ip.FailureReason = &cp
			}
			out.Inputs[i] = ip
		}
		out.PendingInputs = PendingInputs(out.Inputs)
	}

	if len(r.unsent.keys) > 0 {
		out.Unsent = make([]*UnsentInput, len(r.unsent.keys))
		for i, id := range r.unsent.keys {
			out.Unsent[i] = &UnsentInput{
				RequestId: id,
				Seq:       r.unsent.vals[i],
			}
		}
	}

	if len(r.compactions) > 0 {
		out.Compactions = make(map[uint64]*ContextCompaction, len(r.compactions))
		for seq, c := range r.compactions {
			cc := &ContextCompaction{}
			if c.before != nil {
				v := *c.before
				cc.BeforePercent = &v
			}
			if c.after != nil {
				v := *c.after
				cc.AfterPercent = &v
			}
			out.Compactions[seq] = cc
		}
	}

	if len(r.tools) > 0 {
		out.Tools = make([]*RunningTool, len(r.tools))
		for i, t := range r.tools {
			cp := cloneRunningTool(t)
			cp.AwaitingApproval = AwaitingApproval(cp, out.Approvals)
			out.Tools[i] = cp
		}
	}

	if len(r.background) > 0 {
		out.Background = make([]*BackgroundTool, len(r.background))
		for i, b := range r.background {
			out.Background[i] = &BackgroundTool{
				Tool:         cloneRunningTool(b.Tool),
				Returned:     b.Returned,
				Background:   b.Background,
				BackgroundId: b.BackgroundId,
			}
		}
	}

	if len(r.completedAt) > 0 {
		out.CompletedAt = make(map[uint64]uint64, len(r.completedAt))
		for k, v := range r.completedAt {
			out.CompletedAt[k] = v
		}
	}

	if len(r.terminalChanges) > 0 {
		out.TerminalChanges = make(map[uint64]*TerminalChangeList, len(r.terminalChanges))
		for seq, changes := range r.terminalChanges {
			out.TerminalChanges[seq] = &TerminalChangeList{
				Changes: slices.Clone(changes),
			}
		}
	}

	if len(r.approvalReqsByDigest) > 0 && len(r.resolved) > 0 {
		for dk, rs := range r.approvalReqsByDigest {
			if len(rs) != 1 {
				continue
			}
			cs := r.toolCallsByDigest[dk]
			if len(cs) != 1 || cs[0] <= rs[0].seq {
				continue
			}
			if _, ok := r.resolved[rs[0].id]; ok {
				if out.ApprovalCallIds == nil {
					out.ApprovalCallIds = make(map[uint64]string)
				}
				out.ApprovalCallIds[cs[0]] = rs[0].id
			}
		}
	}

	return out
}

// CurrentActions overlays a fresh owner LiveSnapshot onto the reducer's historical snapshot.
func (r *Reducer) CurrentActions(live *v1.LiveSnapshot) *State {
	return applyLiveSnapshot(r.Snapshot(), live, func(seq uint64) (string, bool) {
		txt, ok := r.plansBySeq[seq]
		return txt, ok
	})
}

// Derive runs a full replay over events for agent and returns the resulting ProjectionState.
func Derive(events []*v1.Event, agent v1.Agent) *State {
	r := NewReducer(agent)
	r.Append(events)
	return r.Snapshot()
}

// CurrentActions overlays live onto a historical ProjectionState, looking up plan text from events.
func CurrentActions(history *State, live *v1.LiveSnapshot, events []*v1.Event) *State {
	return applyLiveSnapshot(cloneStateShallowForActions(history), live, func(seq uint64) (string, bool) {
		if seq > 0 && int(seq-1) < len(events) {
			if e := events[seq-1]; e != nil && e.Seq == seq {
				if p := e.GetPlanProposed(); p != nil {
					return p.Text, true
				}
				return "", false
			}
		}
		for i := len(events) - 1; i >= 0; i-- {
			if e := events[i]; e != nil && e.Seq == seq {
				if p := e.GetPlanProposed(); p != nil {
					return p.Text, true
				}
				return "", false
			}
		}
		return "", false
	})
}

func cloneStateShallowForActions(s *State) *State {
	if s == nil {
		return &State{}
	}
	out := &State{
		Resolved:         s.Resolved,
		Answered:         s.Answered,
		Unsent:           s.Unsent,
		Running:          s.Running,
		RunningSeq:       s.RunningSeq,
		ScheduledClosed:  s.ScheduledClosed,
		IdleClosed:       s.IdleClosed,
		Usage:            s.Usage,
		Compactions:      s.Compactions,
		Background:       s.Background,
		CompletedAt:      s.CompletedAt,
		UnfinishedSeqs:   s.UnfinishedSeqs,
		NoResultSeqs:     s.NoResultSeqs,
		TypedLocallySeqs: s.TypedLocallySeqs,
		TerminalChanges:  s.TerminalChanges,
		ApprovalCallIds:  s.ApprovalCallIds,
	}
	if len(s.Inputs) > 0 {
		out.Inputs = make([]*InputProjection, len(s.Inputs))
		for i, in := range s.Inputs {
			cp := *in
			cp.Accepted = cloneAccepted(in.Accepted)
			out.Inputs[i] = &cp
		}
		out.PendingInputs = PendingInputs(out.Inputs)
	}
	if len(s.Tools) > 0 {
		out.Tools = make([]*RunningTool, len(s.Tools))
		for i, t := range s.Tools {
			out.Tools[i] = cloneRunningTool(t)
		}
	}
	return out
}

func applyLiveSnapshot(out *State, live *v1.LiveSnapshot, planText func(seq uint64) (string, bool)) *State {
	out.Approvals = nil
	out.Questions = nil
	out.Plan = nil

	if live == nil {
		for _, t := range out.Tools {
			t.AwaitingApproval = false
		}
		return out
	}

	var snapshotSeq uint64
	if live.Session != nil {
		snapshotSeq = live.Session.LastSeq
	}

	var inputs orderedMap[string, *InputProjection]
	for _, in := range out.Inputs {
		inputs.set(in.RequestId, in)
	}
	newerInput := func(id string) bool {
		in, ok := inputs.get(id)
		return ok && in.LastSeq > snapshotSeq
	}

	observed := Derive(live.Actions, v1.Agent_AGENT_UNSPECIFIED)
	obsInputs := make(map[string]*InputProjection, len(observed.Inputs))
	for _, obsIn := range observed.Inputs {
		obsInputs[obsIn.RequestId] = obsIn
	}

	for _, in := range inputs.vals {
		if (in.Status == v1.InputStatus_INPUT_STATUS_QUEUED || in.Status == v1.InputStatus_INPUT_STATUS_HELD_BY_CLI) &&
			obsInputs[in.RequestId] == nil && !newerInput(in.RequestId) {
			in.Status = v1.InputStatus_INPUT_STATUS_UNCONFIRMED
			in.Detail = nil
		}
	}

	for _, obsIn := range observed.Inputs {
		id := obsIn.RequestId
		if newerInput(id) {
			continue
		}
		cur, exists := inputs.get(id)
		if !exists {
			cur = &InputProjection{
				RequestId: id,
			}
			inputs.set(id, cur)
		}
		cur.Status = obsIn.Status
		if obsIn.Detail != nil {
			cur.Detail = retainInputDetails(obsIn.Detail, cur.Detail)
			if cur.Accepted != nil && obsIn.Detail.Stage != v1.InputStage_INPUT_STAGE_UNSPECIFIED {
				cur.Accepted.Stage = obsIn.Detail.Stage
			}
		}
		if obsIn.Accepted != nil {
			if cur.Accepted == nil {
				cur.Accepted = cloneAccepted(obsIn.Accepted)
			} else if obsIn.Accepted.Stage != v1.InputStage_INPUT_STAGE_UNSPECIFIED {
				cur.Accepted.Stage = obsIn.Accepted.Stage
			}
		}
	}

	if len(inputs.vals) > 0 {
		out.Inputs = slices.Clone(inputs.vals)
		out.PendingInputs = PendingInputs(out.Inputs)
	} else {
		out.Inputs = nil
		out.PendingInputs = nil
	}

	if out.RunningSeq <= snapshotSeq {
		out.Running = live.Running
	}

	var approvals orderedMap[string, *v1.ApprovalRequested]
	var questions orderedMap[string, *v1.QuestionAsked]
	questionSeqs := make(map[string]uint64)
	questionTimes := make(map[string]int64)
	for _, e := range live.Actions {
		if e == nil {
			continue
		}
		switch k := e.Kind.(type) {
		case *v1.Event_ApprovalRequested:
			approvals.set(k.ApprovalRequested.ApprovalId, k.ApprovalRequested)
		case *v1.Event_QuestionAsked:
			if !k.QuestionAsked.Async {
				questions.set(k.QuestionAsked.QuestionId, k.QuestionAsked)
				questionSeqs[k.QuestionAsked.QuestionId] = e.Seq
				questionTimes[k.QuestionAsked.QuestionId] = EventTimeMs(e)
			}
		}
	}
	if len(approvals.keys) > 0 {
		out.Approvals = make([]*PendingApproval, len(approvals.keys))
		for i, id := range approvals.keys {
			out.Approvals[i] = &PendingApproval{
				ApprovalId: id,
				Request:    approvals.vals[i],
			}
		}
	}
	if len(questions.keys) > 0 {
		out.Questions = make([]*PendingQuestion, len(questions.keys))
		for i, id := range questions.keys {
			out.Questions[i] = &PendingQuestion{
				QuestionId: id,
				Request:    questions.vals[i],
				Seq:        questionSeqs[id],
				AskedAtMs:  questionTimes[id],
			}
		}
	}
	for _, t := range out.Tools {
		t.AwaitingApproval = AwaitingApproval(t, out.Approvals)
	}

	if live.Session != nil && live.Session.PlanOpen && live.PlanEventSeq != 0 && len(live.PlanScreenHash) == 32 {
		text, ok := planText(live.PlanEventSeq)
		if !ok || strings.TrimSpace(text) == "" || IsSkeletonText(text) {
			text = live.PlanScreen
		}
		out.Plan = &v1.PlanProposed{Text: text}
	}

	return out
}

// Projection mirrors Kotlin EventProjection: it reuses reducer state across unchanged event prefixes.
type Projection struct {
	previous []*v1.Event
	agent    v1.Agent
	hasAgent bool
	reducer  *Reducer
	result   *State
}

// NewProjection creates an empty Projection.
func NewProjection() *Projection {
	return &Projection{
		reducer: NewReducer(DefaultAgent),
		result:  &State{},
	}
}

// Update advances the projection for events and agent, reusing incremental state when events extend previous.
func (p *Projection) Update(events []*v1.Event, agent v1.Agent) *State {
	if agent == v1.Agent_AGENT_UNSPECIFIED {
		agent = DefaultAgent
	}
	if p.hasAgent && p.agent == agent && len(events) == len(p.previous) && unchangedPrefix(events, p.previous) {
		return p.result
	}
	appendOnly := p.hasAgent && p.agent == agent && unchangedPrefix(events, p.previous)
	if !appendOnly {
		p.reducer.Reset(agent)
		p.reducer.Append(events)
	} else {
		p.reducer.Append(events[len(p.previous):])
	}
	p.result = p.reducer.Snapshot()
	p.previous = slices.Clone(events)
	p.agent = agent
	p.hasAgent = true
	return p.result
}

func unchangedPrefix(events, previous []*v1.Event) bool {
	if len(events) < len(previous) {
		return false
	}
	for i, prev := range previous {
		if events[i] != prev {
			return false
		}
	}
	return true
}
