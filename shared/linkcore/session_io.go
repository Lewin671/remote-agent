package linkcore

import "github.com/Lewin671/remote-agent/shared/peerwire"

// Events go from the executor into Session.Handle; Effects come back for the
// executor to perform. A result Event names the Op or PC ID of the Effect it
// answers; results for an ID the session no longer tracks are ignored, so an
// executor may complete them in any order and at any later time.

// Event is an input to Session.Handle.
type Event interface{ sessionEvent() }

// Effect is an action Session.Handle asks the executor to perform.
type Effect interface{ sessionEffect() }

// PCState is the subset of SDK connection states the session acts on.
type PCState int

const (
	PCOther PCState = iota
	PCConnected
	PCDisconnected
	PCFailed
)

type (
	// Tick drives timers: retransmission, candidate batches, gap and round
	// deadlines. The daemon sends one every 25 ms.
	Tick struct{}
	// FrameReceived is a signal frame from the main connection.
	FrameReceived struct{ Raw []byte }
	// DeliveryResult reports the server's verdict on a frame it relayed;
	// the executor drops results from a main connection that is gone.
	DeliveryResult struct {
		ID     []byte
		Status uint32 // 2 offline, 3 limited
		Retry  uint64
	}
	// PeerDiscovered resumes senders paused by an offline result.
	PeerDiscovered struct{}
	// QueueControl sends one authenticated control frame in the control window.
	QueueControl struct {
		Type uint64
		Body []byte
	}
	// RetireRound ends the round: no new SDK work, replies only.
	RetireRound struct{}
	// FrameSigned answers SignFrame; a nil Signature is a failure.
	FrameSigned struct {
		Op        uint64
		Signature []byte
	}
	// ControlHandled answers HandleControl; Type 0 means unsupported.
	ControlHandled struct {
		Op, Type uint64
		Body     []byte
	}
	// PCCreated answers CreatePC.
	PCCreated struct {
		ID uint64
		OK bool
	}
	// PCDescribed answers DescribePC with the local description it set.
	PCDescribed struct {
		ID  uint64
		SDP []byte
		OK  bool
	}
	// PCRemoteSet answers SetRemotePC.
	PCRemoteSet struct {
		ID uint64
		OK bool
	}
	// PCCandidatesAdded answers AddCandidatesPC; OK is false at the first
	// candidate the SDK refused.
	PCCandidatesAdded struct {
		ID uint64
		OK bool
	}
	// PCLocalCandidate is a gathered candidate, or End when gathering is
	// complete. Valid is the PC's own admission at the time of delivery.
	PCLocalCandidate struct {
		ID        uint64
		Candidate []byte
		End       bool
		Valid     bool
	}
	// PCStateChanged reports the SDK connection state.
	PCStateChanged struct {
		ID    uint64
		State PCState
		Valid bool
	}
	// PCClosed reports that the PC has closed, for whatever reason.
	PCClosed struct{ ID uint64 }
)

type (
	// SignFrame asks for the local device's Ed25519 signature over Message;
	// answer with FrameSigned. The frame enters its sender only then.
	SignFrame struct {
		Op      uint64
		Message []byte
	}
	// TransmitFrame hands one sealed frame to the main connection. The
	// executor rechecks Current and its own gates when the frame is written.
	TransmitFrame struct {
		Raw                          []byte
		Deadline                     uint64
		Critical                     bool
		Class, Type, Use, Generation uint64
	}
	// HandleControl asks the owner to handle an authenticated control frame
	// received at Now; answer with ControlHandled.
	HandleControl struct {
		Op    uint64
		Frame peerwire.Opened
		Now   uint64
	}
	// CreatePC creates the SDK PC and its data channels for one use and
	// generation; answer with PCCreated. Callbacks carry ID.
	CreatePC struct{ ID, Use, Generation uint64 }
	// DescribePC creates and sets a local offer or answer; answer with PCDescribed.
	DescribePC struct {
		ID    uint64
		Offer bool
	}
	// SetRemotePC sets the remote offer or answer; answer with PCRemoteSet.
	SetRemotePC struct {
		ID    uint64
		SDP   []byte
		Offer bool
	}
	// AddCandidatesPC adds remote candidates in order, or the end of
	// candidates when End is set; answer with PCCandidatesAdded.
	AddCandidatesPC struct {
		ID         uint64
		Candidates []peerwire.Candidate
		End        bool
	}
	// ConfirmPC opens the verified PC for business and hands it to the owner.
	ConfirmPC struct{ ID, Use uint64 }
	// ClosePC closes the PC; Reason is a fixed word for the log.
	ClosePC struct {
		ID     uint64
		Reason string
	}
	// BeforeOffer runs before the session commits to a PC for an offer.
	BeforeOffer struct{}
	// PCChosen reports that a PC was created for this session.
	PCChosen struct{}
	// RoundDone reports that the establishment round is over (both uses
	// ready, retired, or past the deadline). Emitted once.
	RoundDone struct{}
	// FrameObserved names an authenticated frame received, for the trace.
	FrameObserved struct{ Type, Use uint64 }
	// PeerUnsupported reports that the peer answered Unsupported (control type 10)
	// to a pending frame from this session.
	PeerUnsupported struct{ Class, Type, Use uint64 }
	// SignalRejected reports that an inbound frame failed verification or state rules.
	SignalRejected struct {
		Use    uint64
		Type   uint64
		Reason string
	}
	// SessionEnd is the last effect: close every PC and release the session.
	SessionEnd struct{ Reason string }
)

func (Tick) sessionEvent()              {}
func (FrameReceived) sessionEvent()     {}
func (DeliveryResult) sessionEvent()    {}
func (PeerDiscovered) sessionEvent()    {}
func (QueueControl) sessionEvent()      {}
func (RetireRound) sessionEvent()       {}
func (FrameSigned) sessionEvent()       {}
func (ControlHandled) sessionEvent()    {}
func (PCCreated) sessionEvent()         {}
func (PCDescribed) sessionEvent()       {}
func (PCRemoteSet) sessionEvent()       {}
func (PCCandidatesAdded) sessionEvent() {}
func (PCLocalCandidate) sessionEvent()  {}
func (PCStateChanged) sessionEvent()    {}
func (PCClosed) sessionEvent()          {}

func (SignFrame) sessionEffect()       {}
func (TransmitFrame) sessionEffect()   {}
func (HandleControl) sessionEffect()   {}
func (CreatePC) sessionEffect()        {}
func (DescribePC) sessionEffect()      {}
func (SetRemotePC) sessionEffect()     {}
func (AddCandidatesPC) sessionEffect() {}
func (ConfirmPC) sessionEffect()       {}
func (ClosePC) sessionEffect()         {}
func (BeforeOffer) sessionEffect()     {}
func (PCChosen) sessionEffect()        {}
func (RoundDone) sessionEffect()       {}
func (FrameObserved) sessionEffect()   {}
func (PeerUnsupported) sessionEffect() {}
func (SignalRejected) sessionEffect()  {}
func (SessionEnd) sessionEffect()      {}

// Current reports whether the frame still belongs to the session as seen by
// an executor holding the latest View: round messages stop at retirement and
// PC messages at a generation change.
func (t TransmitFrame) Current(v View) bool {
	if t.Class == 1 && (t.Type == 1 || t.Type >= 3 && t.Type <= 8) && v.Retired {
		return false
	}
	return !(t.Class == 1 && t.Use > 0 && t.Use <= 2 && t.Generation != v.Generation[t.Use-1])
}

// View is the session state an executor mirrors for other goroutines after
// each Handle: SDK callbacks and queued frames are judged against it.
type View struct {
	Retired, Established bool
	RetainUntil          uint64
	Generation           [2]uint64 // the generation each use's PC is bound to
	Business             [2]bool   // the use's PC has been confirmed
	PC                   [2]uint64 // the use's current PC ID, 0 if none
}

// PCGate names the first condition that refuses use's PC created for
// generation, "" if none does. allow and policy are the owner's gates.
func (v View) PCGate(use, generation, now, deadline uint64, policy, allow func() bool) string {
	switch {
	case v.Retired:
		return "session_retired"
	case v.Generation[use-1] != generation:
		return "generation_retired"
	case !v.Business[use-1] && now >= deadline:
		return "round_deadline"
	case policy != nil && !policy():
		return "policy"
	case !allow():
		return "lease_or_process"
	}
	return ""
}
