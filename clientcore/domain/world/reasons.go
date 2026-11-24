package world

// ReasonKind identifies a structured, non-localized sync reason or failure cause.
// Sync emits Reason{Kind, Param}; screens translates Reason into corev1.Text.
type ReasonKind uint8

const (
	ReasonNone ReasonKind = iota
	ReasonPlain

	// Connection, transport, and server errors.
	ReasonNoNetwork
	ReasonNoServer
	ReasonNotConnected
	ReasonDisconnectedFromServer
	ReasonConnectionTimedOut
	ReasonConnectionTimedOutReconnecting
	ReasonDeviceUnrecognized
	ReasonServerReturned

	// Security, clock skew, lease, and verification errors.
	ReasonClockBehind
	ReasonRemoteControlExpired
	ReasonReplyUnverified
	ReasonRelayNoVerifiableResult
	ReasonInvalidCiphertext

	// Command lifecycle, peer capability, and observed action errors.
	ReasonPreviousOutcomeUnknown
	ReasonComputerOffline
	ReasonExecutedResultUnavailable
	ReasonRequestIdentityConflict
	ReasonRequestNotVerified
	ReasonRequestChanged
	ReasonPlanDisplayChanged
	ReasonAttachmentUpdateRequired
	ReasonDaemonOutOfDate
	ReasonClientOutOfDate

	// Session history and event stream errors.
	ReasonHistoryKeysUnavailable
	ReasonHistoryLoadFailed
	ReasonHistoryGap

	// Project preferences CAS errors.
	ReasonPrefsConflict
)

// Reason is a structured sync failure or status cause with an optional parameter.
type Reason struct {
	Kind  ReasonKind
	Param string
}

// PlainReason wraps an unlocalized runtime/server string as a Reason.
// Empty strings return the zero Reason.
func PlainReason(s string) Reason {
	if s == "" {
		return Reason{}
	}
	return Reason{Kind: ReasonPlain, Param: s}
}

// IsZero reports whether r represents no reason.
func (r Reason) IsZero() bool {
	return r.Kind == ReasonNone && r.Param == ""
}

// ConnectStep identifies the current connection establishment phase.
type ConnectStep uint8

const (
	ConnectStepNone ConnectStep = iota
	ConnectStepReadingLocalState
	ConnectStepSigningIn
	ConnectStepEstablishingPush
)
