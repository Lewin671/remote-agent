package security

import (
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
)

// WorkflowReason identifies a structured, non-localized security workflow error.
// The security layer emits WorkflowReason; screens translates it into corev1.Text.
type WorkflowReason uint8

const (
	WorkflowReasonNone WorkflowReason = iota
	WorkflowReasonPagePaused
	WorkflowReasonFinishCurrentOpFirst
	WorkflowReasonFinishOriginalOpFirst
	WorkflowReasonUnfinishedOpContinueFirst
	WorkflowReasonReturnToCreationOrRecovery
	WorkflowReasonDeviceInfoChangedTransferAborted
	WorkflowReasonUnpublishedPairingDraftExpired
	WorkflowReasonUnpublishedPairingDraftCanceled
	WorkflowReasonPairingCanceledRemoveCandidate
	WorkflowReasonPairingDeliveryExpiredRemoveCandidate
	WorkflowReasonAccountPubKeyMissing
	WorkflowReasonComputerArchiveDeleted
	WorkflowReasonPairingExpiredOrCanceled
	WorkflowReasonPairingCanceled
	WorkflowReasonConfirmPatternsBeforeApproving
	WorkflowReasonInvalidPairingKey
	WorkflowReasonConfirmRecoveryCodeSavedFirst
	WorkflowReasonRegPowInvalidOrExpired
	WorkflowReasonRegPowExpiredRetryDraft
	WorkflowReasonManagerCannotBeRemovedDirectly
	WorkflowReasonGenerateRecoveryCodeOnSuccessor
	WorkflowReasonNamesOnlyChangedOnThatDevice
	WorkflowReasonServerMustUseHTTPS
	WorkflowReasonInvalidRecoveryCode
	WorkflowReasonProtocolMismatch
	WorkflowReasonPairingCodeInvalidOrUsed
	WorkflowReasonRateLimited
	WorkflowReasonClockBehind
	WorkflowReasonSecurityChainFull
	WorkflowReasonSuccessUncertain
	WorkflowReasonSetupTokenRequired
	WorkflowReasonRecoveryNotReady
	WorkflowReasonRecoveryRejected
	WorkflowReasonPermissionDenied
	WorkflowReasonNotAuthority
	WorkflowReasonNotFound
	WorkflowReasonInvalidArgument
	WorkflowReasonFailedPrecondition
	WorkflowReasonOperationCanceled
	WorkflowReasonCommandFailed
)

// ExitAdviceReason identifies the structured warning for leaving/clearing local security state.
type ExitAdviceReason uint8

const (
	ExitAdviceNone ExitAdviceReason = iota
	ExitAdviceDefault
	ExitAdviceUnresolvedOp
	ExitAdviceOnlyManagingClient
)

// WorkflowMeta carries structured reason metadata for one SecurityWorkflowResultEffect.
type WorkflowMeta struct {
	ActionID   uint64
	ErrReason  WorkflowReason
	ErrRaw     string
	ExitReason ExitAdviceReason
}

// BootstrapErrorMeta carries the structured bootstrap error for SecurityWorkflowStatus.
type BootstrapErrorMeta struct {
	Reason WorkflowReason
	Raw    string
}

// NotifyOutcome holds the validated, non-localized push notification fields produced
// by the security layer. screens translates NotifyOutcome into SecurityNotifyDisplay
// containing corev1.Text fields.
type NotifyOutcome struct {
	OpID                  uint64
	VerifyRecordUnchanged bool
	Valid                 bool
	Kind                  v1.NotifyKind
	Tag                   string
	MachineID             string
	MachineName           string
	SessionID             string
	SourceEventSeq        uint64

	// Machine disk notification fields.
	DiskAvailableBytes uint64
	DiskCritical       bool

	// Generic / turn-ended / scheduled / attention fields.
	Title           string
	Text            string
	HasFallbackBody bool
	FallbackBody    string
	SharedFileCount int32

	// Approval attention fields.
	HasApproval         bool
	ApprovalID          string
	ApprovalCommand     string
	ApprovalToolName    string
	ApprovalDescription string
	ApprovalToolInput   string
	ApprovalDeadlineMs  int64
	ApprovalTruncated   bool
}
