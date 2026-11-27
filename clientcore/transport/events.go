package transport

// Event is an input to Engine.Handle: something the shell, the security layer
// or the session layer observed. Times are the caller's; Handle gets "now"
// separately.
type Event interface{ event() }

// Effect is an action Engine.Handle asks for. Round* effects go to the
// session layer, the rest to the shell.
type Effect interface{ effect() }

// Failure is why a round ended without a path, as far as this side can tell.
type Failure uint8

const (
	FailureNone            Failure = iota
	FailurePeerUnsupported         // the computer answered Unsupported
	FailureNoResponse              // the computer never answered the handshake
	FailureNoTraversal             // the handshake worked but no address was found
	FailureLost                    // at least one path came up, then all paths closed
)

// Caps is the part of a computer's capability snapshot (Control 14) that
// route choice reads.
type Caps struct {
	FileUpload    bool
	ImageUpload   bool
	MaxChunkBytes int64
}

// ---- events from the shell ----

// SetSupported: whether this device can make direct paths (WebRTC available/linked).
type SetSupported struct{ Supported bool }

// Lifecycle: the app or tab went to the foreground or background. Direct
// paths are kept only in the foreground.
type Lifecycle struct{ Foreground bool }

// MainLeg: the main WebSocket that carries signaling came up (a new leg) or went.
type MainLeg struct{ Ready bool }

// NetworkChanged: sync saw a new network generation.
type NetworkChanged struct{}

// DiscoverNow: trigger discovery without invalidating freshness or active rounds.
type DiscoverNow struct{}

// RoundBooted: bootstrap learned the computer process's boot nonce for Round.
type RoundBooted struct {
	Round RoundID
	Boot  string
}

// Tick: the ScheduleTick timer fired.
type Tick struct{}

// Entropy refills the uniform [0,1) floats used for retry jitter.
type Entropy struct{ Units []float64 }

// PreferencesLoaded: the stored per-computer switches (absent means on).
type PreferencesLoaded struct{ Enabled map[string]bool }

// SetEnabled: the user turned this device's direct switch for a computer.
type SetEnabled struct {
	Machine string
	Enabled bool
}

// RetryNow: 「重新尝试直连」.
type RetryNow struct{ Machine string }

// SetWatched: the sessions each computer should push events for over the
// read path (Control 15), in priority order.
type SetWatched struct{ Sessions map[string][]string }

// ---- events from the security layer ----

// SecuritySynced: a security sync that began at StartedAt proved this head,
// lease and member list. Machines are the computer members, in order.
type SecuritySynced struct {
	Member        bool
	Account       string
	Head          string
	StartedAt     int64
	LeaseNotAfter int64
	Machines      []string
}

// SecurityRejected: a security sync did not prove membership or lease.
type SecurityRejected struct{}

// SecurityInstalled: a new security record was stored.
type SecurityInstalled struct {
	Member  bool
	Account string
	Head    string
}

// AuthorizeDone answers Authorize.
type AuthorizeDone struct{ OK bool }

// ---- events from the session layer ----

// RoundOffered: the computer probed with a connection this side does not know.
type RoundOffered struct {
	Machine    string
	Boot       string // the computer process's boot nonce
	DeadlineAt int64
}

// RoundChosen: the round won the pairing (or is about to send its offer);
// it becomes the computer's round and the others retire.
type RoundChosen struct{ Round RoundID }

// PathReady: a use's DataChannel is open and confirmed. Path names the peer
// connection so a later replacement is told apart.
type PathReady struct {
	Round RoundID
	Use   Use
	Path  uint64
}

// PathClosed: one use's path closed while its round goes on.
type PathClosed struct {
	Round RoundID
	Use   Use
}

// RoundEnded: the round closed; Failure names the cause when known.
type RoundEnded struct {
	Round    RoundID
	Failure  Failure
	TimedOut bool
}

// CapabilitySnapshot: Control 14 opened on the round. Caps is nil when the
// snapshot carried none.
type CapabilitySnapshot struct {
	Round     RoundID
	Seq       uint64
	Caps      *Caps
	ExpiresAt int64
}

// Capabilities: Control 1 opened on the round. DownloadUntil is 0 when
// downloads are not offered; EventPush when it lists Control 15; Phase2Commands
// and Terminal when it lists the Phase 2/3 control set (2,3,5,6,7,8,11,12).
type Capabilities struct {
	Round          RoundID
	Seq            uint64
	DownloadUntil  int64
	EventPush      bool
	Phase2Commands bool
	Terminal       bool
}

// ---- effects for the session layer ----

// StartRound: begin an outbound round with the computer.
type StartRound struct {
	Round      RoundID
	Machine    string
	DeadlineAt int64
}

// AcceptRound: take the offered round (the frame that offered it goes to it).
type AcceptRound struct {
	Round      RoundID
	Machine    string
	DeadlineAt int64
}

// RetireRound: stop starting anything new on the round; it closes itself.
type RetireRound struct{ Round RoundID }

// CloseRound: close the round now. No more events about it are read.
type CloseRound struct{ Round RoundID }

// ClosePath: a ready path the engine will not use.
type ClosePath struct {
	Round RoundID
	Use   Use
}

// ResumeRound: the main connection is back; resume signaling at once.
type ResumeRound struct{ Round RoundID }

// SendSubscription: send Control 15 with these session IDs (possibly none).
type SendSubscription struct {
	Round      RoundID
	SessionIDs []string
}

// ---- effects for the shell ----

// Authorize: run a security sync now (the answer is SecuritySynced or
// SecurityRejected, then AuthorizeDone).
type Authorize struct{}

// ScheduleTick: fire Tick after DelayMs, replacing any pending one.
type ScheduleTick struct{ DelayMs int64 }

// CancelTick: no Tick is wanted.
type CancelTick struct{}

// PersistEnabled: store this device's switch for the computer.
type PersistEnabled struct {
	Machine string
	Enabled bool
}

// RequestEntropy: send Entropy with Count unit floats.
type RequestEntropy struct{ Count int }

// Changed: status or diagnostics may have changed; re-read them.
type Changed struct{}

// RetryNowResult answers RetryNow: whether a round was started.
type RetryNowResult struct {
	Machine string
	Started bool
}

func (SetSupported) event()       {}
func (Lifecycle) event()          {}
func (MainLeg) event()            {}
func (NetworkChanged) event()     {}
func (DiscoverNow) event()        {}
func (RoundBooted) event()        {}
func (Tick) event()               {}
func (Entropy) event()            {}
func (PreferencesLoaded) event()  {}
func (SetEnabled) event()         {}
func (RetryNow) event()           {}
func (SetWatched) event()         {}
func (SecuritySynced) event()     {}
func (SecurityRejected) event()   {}
func (SecurityInstalled) event()  {}
func (AuthorizeDone) event()      {}
func (RoundOffered) event()       {}
func (RoundChosen) event()        {}
func (PathReady) event()          {}
func (PathClosed) event()         {}
func (RoundEnded) event()         {}
func (CapabilitySnapshot) event() {}
func (Capabilities) event()       {}

func (StartRound) effect()       {}
func (AcceptRound) effect()      {}
func (RetireRound) effect()      {}
func (CloseRound) effect()       {}
func (ClosePath) effect()        {}
func (ResumeRound) effect()      {}
func (SendSubscription) effect() {}
func (Authorize) effect()        {}
func (ScheduleTick) effect()     {}
func (CancelTick) effect()       {}
func (PersistEnabled) effect()   {}
func (RequestEntropy) effect()   {}
func (Changed) effect()          {}
func (RetryNowResult) effect()   {}
