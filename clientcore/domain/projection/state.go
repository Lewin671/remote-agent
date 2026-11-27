package projection

import (
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
)

// PendingApproval is an unresolved approval request, preserved in request order.
type PendingApproval struct {
	ApprovalId string
	Request    *v1.ApprovalRequested
}

// ResolvedApproval records how an approval request was resolved and by whom.
type ResolvedApproval struct {
	Decision v1.ApprovalDecision
	Message  string
	Origin   *v1.Origin
}

// PendingQuestion is an unanswered synchronous question, preserved in request order.
type PendingQuestion struct {
	QuestionId string
	Request    *v1.QuestionAsked
	Seq        uint64
	AskedAtMs  int64
}

// AnsweredQuestion records the flattened answer strings for a question and who answered it.
type AnsweredQuestion struct {
	Answers []string
	Origin  *v1.Origin
}

// AcceptedInput is what the daemon accepted for a client message, shared across clients.
type AcceptedInput struct {
	Text     string
	ImageIds []string
	Origin   *v1.Origin
	Stage    v1.InputStage
	Files    []*v1.SentFile
	TimeMs   int64
}

// InputProjection is the per-request input state in acceptance order.
type InputProjection struct {
	RequestId     string
	Status        v1.InputStatus
	LastSeq       uint64
	Detail        *v1.InputState
	Accepted      *AcceptedInput
	FailureReason *string
}

func (x *InputProjection) GetFailureReason() string {
	if x != nil && x.FailureReason != nil {
		return *x.FailureReason
	}
	return ""
}

// PendingInput is an input the model has not received yet (QUEUED or HELD_BY_CLI), in acceptance order.
type PendingInput struct {
	RequestId string
	Text      string
	Mode      v1.SendMode
	Paused    bool
	Held      bool
	Origin    *v1.Origin
	Files     []*v1.SentFile
}

// UnsentInput records a message canceled with reason "exited" in the order it became unsent.
type UnsentInput struct {
	RequestId string
	Seq       uint64
}

// RunningTool is a tool started and not yet completed, or the start metadata for a background tool.
type RunningTool struct {
	Title            string
	CallId           string
	SinceMs          int64
	Kind             v1.ToolKind
	ItemType         string
	Seq              uint64
	Input            string
	InputDigest      string
	AwaitingApproval bool
}

// BackgroundTool is a tool whose completion is unknown in the current or last turn.
type BackgroundTool struct {
	Tool         *RunningTool
	Returned     bool
	Background   v1.ToolBackground
	BackgroundId string
}

// ContextCompaction records context occupancy percentages (nil means unknown) around compaction.
type ContextCompaction struct {
	BeforePercent *int32
	AfterPercent  *int32
}

func (x *ContextCompaction) GetBeforePercent() int32 {
	if x != nil && x.BeforePercent != nil {
		return *x.BeforePercent
	}
	return 0
}

func (x *ContextCompaction) GetAfterPercent() int32 {
	if x != nil && x.AfterPercent != nil {
		return *x.AfterPercent
	}
	return 0
}

// TerminalChangeKind marks variants of TerminalChange.Kind.
type TerminalChangeKind interface {
	isTerminalChangeKind()
}

// TerminalChange records a mode or model change made in the terminal at a TurnContext.
type TerminalChange struct {
	Kind TerminalChangeKind
}

func (x *TerminalChange) GetMode() *TerminalModeChange {
	if x != nil {
		if m, ok := x.Kind.(*TerminalChangeMode); ok {
			return m.Mode
		}
	}
	return nil
}

func (x *TerminalChange) GetModel() *TerminalModelChange {
	if x != nil {
		if m, ok := x.Kind.(*TerminalChangeModel); ok {
			return m.Model
		}
	}
	return nil
}

type TerminalChangeMode struct {
	Mode *TerminalModeChange
}

func (*TerminalChangeMode) isTerminalChangeKind() {}

type TerminalChangeModel struct {
	Model *TerminalModelChange
}

func (*TerminalChangeModel) isTerminalChangeKind() {}

// TerminalModeChange records a terminal approval mode change.
type TerminalModeChange struct {
	Mode    v1.ApprovalMode
	RawMode string
}

// TerminalModelChange records a terminal model or effort change.
type TerminalModelChange struct {
	Model  string
	Effort string
}

// TerminalChangeList wraps the terminal changes observed at a single TurnContext sequence number.
type TerminalChangeList struct {
	Changes []*TerminalChange
}

// State is the reduced domain state of a session's event stream.
type State struct {
	Approvals     []*PendingApproval
	Resolved      map[string]*ResolvedApproval
	Questions     []*PendingQuestion
	Answered      map[string]*AnsweredQuestion
	Inputs        []*InputProjection
	PendingInputs []*PendingInput
	Unsent        []*UnsentInput

	Running         bool
	RunningSeq      uint64
	ScheduledClosed bool
	IdleClosed      bool

	Usage       *v1.TokenUsage
	Compactions map[uint64]*ContextCompaction
	Plan        *v1.PlanProposed

	Tools            []*RunningTool
	Background       []*BackgroundTool
	CompletedAt      map[uint64]uint64
	UnfinishedSeqs   []uint64
	NoResultSeqs     []uint64
	TypedLocallySeqs []uint64
	TerminalChanges  map[uint64]*TerminalChangeList
	ApprovalCallIds  map[uint64]string
}
