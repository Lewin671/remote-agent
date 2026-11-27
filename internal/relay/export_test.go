package relay

import (
	"context"
	"crypto/ed25519"
	"net"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/protocol"
	"github.com/Lewin671/remote-agent/internal/transport"
	"github.com/Lewin671/remote-agent/shared/devicesec"
	"google.golang.org/protobuf/proto"
)

// SetNetworkChangesForTest injects OS observations without changing host networking.
func SetNetworkChangesForTest(r *Relay, changes <-chan uint64) {
	r.networkChanges = func(context.Context) <-chan uint64 { return changes }
}

// SetWakeEventsForTest injects scheduler-pause/wake events without waiting for wall jumps.
func SetWakeEventsForTest(r *Relay, wakes <-chan struct{}) {
	r.wakeEvents = func(context.Context) <-chan struct{} { return wakes }
}

// SetRouteAvailableForTest overrides route verification on a new network hint.
func SetRouteAvailableForTest(r *Relay, available func(net.IP) bool) {
	r.routeAvailable = available
}

// SetBeforeDialResultForTest runs inside the dial goroutine just before sending dialResult.
func SetBeforeDialResultForTest(r *Relay, hook func(uint64)) {
	r.beforeDialResult = hook
}

// SetBeforeHelloForTest runs inside dialMain after websocket.Dial and right before StreamHello.
func SetBeforeHelloForTest(r *Relay, hook func()) {
	r.beforeHello = hook
}

// SetOnFrameForTest runs after Liveness.OnFrame updates for each decoded frame on the active leg.
func SetOnFrameForTest(r *Relay, hook func(isHeartbeat bool)) {
	r.onFrame = hook
}

// SetRecoveryNowForTest overrides the clock used by relay leg liveness and recovery budgets.
func SetRecoveryNowForTest(r *Relay, now func() time.Time) {
	r.recoveryNow = now
}

// SetSyncIntervalForTest changes only this relay's polling schedule. Set it
// before Run; production relays retain New's five-second interval.
func SetSyncIntervalForTest(r *Relay, interval time.Duration) { r.syncInterval = interval }

// SetSyncRetryBaseForTest changes retry timing before Run. Backoff and the
// one-minute cap still follow the production algorithm.
func SetSyncRetryBaseForTest(r *Relay, base time.Duration) { r.syncRetryBase = base }

func SetReadyTimeoutForTest(r *Relay, timeout time.Duration) { r.readyTimeout = timeout }

// HasOwedNotificationForTest observes the retained outcome, after Notify's
// response has been processed, rather than racing the server handler's count.
func HasOwedNotificationForTest(r *Relay, id string) bool { return r.hasOwedNotify(id) }

// HasBacklogWorkerForTest is false after an ended worker has returned and
// released its admission slot. It does not require a scheduler sleep.
func HasBacklogWorkerForTest(r *Relay) bool { return len(r.backlog) != 0 }

func SetSecurityIntervalForTest(r *Relay, interval time.Duration) { r.securityInterval = interval }

// SetRetryNowForTest shares a process publication clock across socket changes.
func SetRetryNowForTest(r *Relay, now func() time.Time) { r.timing.now = now }

func ReadyCountForTest(r *Relay) uint64 { return r.readyCount.Load() }

// IsolateRecoveryBudgetForTest gives a simulated daemon process its own global budget.
func IsolateRecoveryBudgetForTest(r *Relay) { r.recoveryBudget = &transport.Budget{} }

// ObserveRecoveryForTest records only lifecycle timing, never frames or identifiers.
func ObserveRecoveryForTest(r *Relay, observer func(string)) {
	r.observeRecovery = func(event recoveryEvent) {
		observer([]string{"hint", "suspect", "lost", "dial", "ready", "aborted"}[event])
	}
}

func ActiveCallsForTest(r *Relay) int {
	if s := r.scheduling.Load(); s != nil {
		n := s.Active()
		return n[0] + n[1] + n[2]
	}
	return 0
}

func ActivePoolsForTest(r *Relay) [3]int {
	if s := r.scheduling.Load(); s != nil {
		return s.Active()
	}
	return [3]int{}
}

func QueuedCallsForTest(r *Relay) [4]int {
	if s := r.scheduling.Load(); s != nil {
		return s.Queued()
	}
	return [4]int{}
}

func SetDirectLoopbackForTest(r *Relay)                   { r.directLoopback = true; r.directSTUN = []string{} }
func SetDirectClockForTest(r *Relay, fn func() time.Time) { r.directClock = fn }
func SetSchedulerClockForTest(r *Relay, now func() time.Time, after func(time.Duration, func()) func() bool) {
	r.SchedulerNow = now
	r.SchedulerAfter = after
}
func DirectFreshForTest(r *Relay) bool {
	s := r.security.Load()
	return s != nil && s.directFresh.Load() != nil
}
func SetTerminalClockForTest(r *Relay, ttl time.Duration, fn func() time.Time) {
	r.termZeroLegTTL = ttl
	r.termClock = fn
}
func TerminalRegistryActiveForTest(r *Relay) int {
	h := r.termHubRef.Load()
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.registry.ActiveCount()
}
func TerminalRelayAttachGenForTest(r *Relay, streamID string) uint64 {
	h := r.termHubRef.Load()
	if h == nil {
		return 0
	}
	lt := h.getLive(streamID)
	if lt == nil {
		return 0
	}
	lt.mu.Lock()
	defer lt.mu.Unlock()
	return lt.relayAttachGen
}
func TerminalHasDirectLegForTest(r *Relay, streamID string) bool {
	h := r.termHubRef.Load()
	if h == nil {
		return false
	}
	lt := h.getLive(streamID)
	if lt == nil {
		return false
	}
	lt.mu.Lock()
	defer lt.mu.Unlock()
	return lt.stream.HasLeg("direct")
}
func TerminalHasRelayLegForTest(r *Relay, streamID string) bool {
	h := r.termHubRef.Load()
	if h == nil {
		return false
	}
	lt := h.getLive(streamID)
	if lt == nil {
		return false
	}
	lt.mu.Lock()
	defer lt.mu.Unlock()
	return lt.relayPath != nil
}
func TerminalOutSeqForTest(r *Relay, streamID string) uint64 {
	h := r.termHubRef.Load()
	if h == nil {
		return 0
	}
	lt := h.getLive(streamID)
	if lt == nil {
		return 0
	}
	lt.mu.Lock()
	defer lt.mu.Unlock()
	return lt.outSeq
}
func TerminalDirectLegIDForTest(r *Relay, streamID string) string {
	h := r.termHubRef.Load()
	if h == nil {
		return ""
	}
	lt := h.getLive(streamID)
	if lt == nil {
		return ""
	}
	lt.mu.Lock()
	defer lt.mu.Unlock()
	return lt.stream.LegID("direct")
}
func TerminalHasPendingDirectForTest(r *Relay, streamID string) bool {
	h := r.termHubRef.Load()
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.pendingDirect[streamID]
	return ok
}
func PeerTerminalDirectForTest(r *Relay, peerID string) bool {
	dm := r.directRef.Load()
	if dm == nil {
		return false
	}
	dp, ok := dm.activePeerFor(peerID)
	return ok && dp.peerTerminalDirect.Load()
}
func TerminalRelayGenForTest(r *Relay, streamID string) uint64 {
	h := r.termHubRef.Load()
	if h == nil {
		return 0
	}
	lt := h.getLive(streamID)
	if lt == nil {
		return 0
	}
	lt.mu.Lock()
	defer lt.mu.Unlock()
	return lt.relayGen
}
func SetTerminalBeforeRecordOutputForTest(r *Relay, fn func(streamID string, seq uint64)) {
	h := r.termHubRef.Load()
	if h == nil {
		return
	}
	if fn == nil {
		h.beforeRecordOutputReplay.Store(nil)
		return
	}
	h.beforeRecordOutputReplay.Store(&fn)
}

// ProbeEchoHarnessForTest wraps an in-process daemon endpoint with an authority
// client and a removable second member for negative admission tests.
type ProbeEchoHarnessForTest struct {
	ep           *endpoint
	keys         *testSecurityKeys
	authority    *deviceauth.Endpoint
	secondClient *deviceauth.Endpoint
	secondMember devicesec.Member
	binding      deviceauth.PeerBinding
	secret3      []byte
	state3       devicesec.State
}

func NewProbeEchoHarnessForTest(t *testing.T, backend Backend) *ProbeEchoHarnessForTest {
	t.Helper()
	ep := testEndpoint(t, New(backend))
	keys := initializeEndpointSecurityKeys(t, ep)
	t.Cleanup(func() {
		ep.link.DrainCalls()
		ep.admissionOwner().close()
	})

	// Add a second client member at epoch 3 so it can be cleanly removed at epoch 4.
	_, c2Ed, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	c2X, c2XPub, err := deviceauth.NewXKey()
	if err != nil {
		t.Fatal(err)
	}
	c2Member, err := devicesec.NewMember(c2Ed.Public().(ed25519.PublicKey), c2XPub, devicesec.Client)
	if err != nil {
		t.Fatal(err)
	}
	secret3 := make([]byte, 32)
	secret3[0] = 3
	nowMs := uint64(time.Now().UnixMilli())
	parent := keys.states[len(keys.states)-1]
	issuedAt := max(nowMs, parent.Core.IssuedAt+1000)
	state3, err := deviceauth.BuildPairState(parent, c2Member, c2XPub, keys.secret, secret3, keys.phone, issuedAt)
	if err != nil {
		t.Fatal(err)
	}
	lease3, err := deviceauth.BuildLease(state3, keys.phone, state3.Core.IssuedAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := ep.security.endpoint.InstallWithLease([]devicesec.State{state3}, []*devicesec.RecoveryStatement{nil}, &lease3, nowMs); err != nil {
		t.Fatal(err)
	}

	states3 := append(append([]devicesec.State(nil), keys.states...), state3)
	proofs3 := []*devicesec.RecoveryStatement{nil, nil, nil}
	pin, head, selfID := ep.security.endpoint.Snapshot()

	authClient, err := deviceauth.InitEndpoint("", pin, states3, proofs3, head.Core.AuthorityID, keys.phone.Seed(), keys.phoneX, lease3)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { authClient.Close() })

	c2Client, err := deviceauth.InitEndpoint("", pin, states3, proofs3, c2Member.ID, c2Ed.Seed(), c2X, lease3)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c2Client.Close() })

	selfMember, _ := head.Core.Member(selfID)
	peerMember, _ := head.Core.Member(head.Core.AuthorityID)
	headHash, err := head.Hash()
	if err != nil {
		t.Fatal(err)
	}

	return &ProbeEchoHarnessForTest{
		ep:           ep,
		keys:         keys,
		authority:    authClient,
		secondClient: c2Client,
		secondMember: c2Member,
		binding:      deviceauth.PeerBinding{Self: selfMember, Peer: peerMember, Head: headHash},
		secret3:      secret3,
		state3:       state3,
	}
}

func (h *ProbeEchoHarnessForTest) MachineID() string { return h.ep.acct.DeviceID }

func (h *ProbeEchoHarnessForTest) AuthorityClient() *deviceauth.Endpoint { return h.authority }

func (h *ProbeEchoHarnessForTest) SecondClient() *deviceauth.Endpoint { return h.secondClient }

// RemoveSecondMember installs epoch 4 removing the second client on the daemon
// (and authority client) while leaving SecondClient on epoch 3.
func (h *ProbeEchoHarnessForTest) RemoveSecondMember(t *testing.T) {
	t.Helper()
	var remaining []devicesec.Member
	for _, m := range h.state3.Core.Members {
		if m.ID != h.secondMember.ID {
			remaining = append(remaining, m)
		}
	}
	secret4 := make([]byte, 32)
	secret4[0] = 4
	nowMs := uint64(time.Now().UnixMilli())
	issuedAt := max(nowMs, h.state3.Core.IssuedAt+1000)
	state4, err := deviceauth.BuildMembershipState(h.state3, remaining, []string{h.secondMember.ID}, h.secret3, secret4, h.keys.phone, issuedAt)
	if err != nil {
		t.Fatal(err)
	}
	lease4, err := deviceauth.BuildLease(state4, h.keys.phone, state4.Core.IssuedAt)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.ep.security.endpoint.InstallWithLease([]devicesec.State{state4}, []*devicesec.RecoveryStatement{nil}, &lease4, nowMs); err != nil {
		t.Fatal(err)
	}
	if err := h.authority.InstallWithLease([]devicesec.State{state4}, []*devicesec.RecoveryStatement{nil}, &lease4, nowMs); err != nil {
		t.Fatal(err)
	}
	_, head, selfID := h.ep.security.endpoint.Snapshot()
	selfMember, _ := head.Core.Member(selfID)
	peerMember, _ := head.Core.Member(head.Core.AuthorityID)
	headHash, err := head.Hash()
	if err != nil {
		t.Fatal(err)
	}
	h.binding = deviceauth.PeerBinding{Self: selfMember, Peer: peerMember, Head: headHash}
}

func (h *ProbeEchoHarnessForTest) SealCommand(t *testing.T, client *deviceauth.Endpoint, reqID, session string, deadline time.Time, cmd *pb.Command) *pb.Envelope {
	t.Helper()
	cmd = proto.Clone(cmd).(*pb.Command)
	cmd.ProtocolVersion = protocol.PeerVersion
	body, err := proto.Marshal(cmd)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := client.Prepare(devicesec.Header{
		Purpose:  devicesec.Command,
		Kind:     uint32(pb.PayloadKind_PAYLOAD_KIND_COMMAND),
		Machine:  h.ep.acct.DeviceID,
		Session:  session,
		Request:  reqID,
		Deadline: uint64(deadline.UnixMilli()),
	}, nil, body)
	if err != nil {
		t.Fatal(err)
	}
	env, err := deviceauth.Envelope(wire)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func (h *ProbeEchoHarnessForTest) DispatchRelay(ctx context.Context, env *pb.Envelope) *pb.ForwardReply {
	ch := make(chan *pb.ForwardReply, 1)
	h.ep.dispatch(ctx, &pb.ForwardCall{CallId: 1, AttemptId: "relay-1", Command: env}, func(r *pb.ForwardReply) { ch <- r })
	return <-ch
}

func (h *ProbeEchoHarnessForTest) DispatchDirect(ctx context.Context, directUse uint64, env *pb.Envelope) *pb.ForwardReply {
	ch := make(chan *pb.ForwardReply, 1)
	h.ep.dispatchDirect(ctx, h.binding, directUse, func() bool { return true }, env, func(r *pb.ForwardReply) { ch <- r })
	return <-ch
}

func (h *ProbeEchoHarnessForTest) OpenResult(t *testing.T, client *deviceauth.Endpoint, rep *pb.ForwardReply) *pb.CommandResult {
	t.Helper()
	return openCommandResult(t, client, rep)
}
