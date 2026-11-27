package transport

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/Lewin671/remote-agent/shared/linkcore"
	"github.com/Lewin671/remote-agent/shared/peerwire"
)

// fixedEntropy returns mid-range bytes so jittered delays land well inside
// their bounds, never at zero.
func fixedEntropy(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = 0x80 | byte(i)
	}
	return b
}

func testOSRandom(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

type coordActor struct {
	t        *testing.T
	id       string
	role     uint64
	signPriv ed25519.PrivateKey
	signPub  [32]byte
	xPriv    *ecdh.PrivateKey
	xPub     [32]byte
}

func newCoordActor(t *testing.T, tag byte, role uint64) *coordActor {
	t.Helper()
	seed := sha256.Sum256([]byte{tag, byte(role)})
	signPriv := ed25519.NewKeyFromSeed(seed[:])
	signPub := signPriv.Public().(ed25519.PublicKey)
	xSeed := sha256.Sum256([]byte{tag, byte(role), 0x77})
	xPriv, err := ecdh.X25519().NewPrivateKey(xSeed[:])
	if err != nil {
		t.Fatal(err)
	}
	idHash := sha256.Sum256(signPub)
	a := &coordActor{
		t:        t,
		id:       hex.EncodeToString(idHash[:16]),
		role:     role,
		signPriv: signPriv,
		xPriv:    xPriv,
	}
	copy(a.signPub[:], signPub)
	copy(a.xPub[:], xPriv.PublicKey().Bytes())
	return a
}

func (a *coordActor) agree(peerX [32]byte) []byte {
	pk, err := ecdh.X25519().NewPublicKey(peerX[:])
	if err != nil {
		a.t.Fatal(err)
	}
	s, err := a.xPriv.ECDH(pk)
	if err != nil {
		a.t.Fatal(err)
	}
	return s
}

func fakeCoordSDP(seed int) []byte {
	var fp [95]byte
	pos := 0
	for i := 0; i < 32; i++ {
		if i > 0 {
			fp[pos] = ':'
			pos++
		}
		b := byte(seed*17 + i)
		const h = "0123456789ABCDEF"
		fp[pos] = h[b>>4]
		fp[pos+1] = h[b&0x0f]
		pos += 2
	}
	return []byte("v=0\r\no=- 1 2 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\na=group:BUNDLE 0\r\nm=application 9 UDP/DTLS/SCTP webrtc-datachannel\r\nc=IN IP4 0.0.0.0\r\n" +
		fmt.Sprintf("a=ice-ufrag:uf%04d\r\na=ice-pwd:pwdpwdpwdpwdpwdpwdpwdpwd\r\na=fingerprint:sha-256 %s\r\na=setup:actpass\r\na=mid:0\r\na=sctp-port:5000\r\n", seed, string(fp[:])))
}

func TestCoordinatorLoopbackHandshakeControlAndDiagnostics(t *testing.T) {
	client := newCoordActor(t, 1, 1)
	daemon := newCoordActor(t, 2, 2)
	domain := sha256.Sum256([]byte("test-domain"))
	headHash := sha256.Sum256([]byte("test-head"))
	codec, err := peerwire.NewCodec("00000000000000000000000000000001", domain, headHash, 1, 17)
	if err != nil {
		t.Fatal(err)
	}
	now := int64(1_700_000_000_000)

	coord := NewCoordinator(CoordConfig{
		Supported:       true,
		Account:         "00000000000000000000000000000001",
		ProtocolVersion: 17,
		ServerHost:      "127.0.0.1",
	})
	coord.ProvideRandomBytes(now, testOSRandom(CoordEntropyRefillBytes))
	coord.SetMainLeg(now, true)
	coord.SetWatched(now, map[string][]string{daemon.id: {"sess-1", "sess-2"}})

	effs := coord.OnSecuritySnapshot(now, CoordSecuritySnapshot{
		Member:        true,
		Account:       "00000000000000000000000000000001",
		Domain:        domain,
		Head:          "head-1",
		HeadHash:      headHash,
		Epoch:         1,
		StartedAt:     now,
		LeaseNotAfter: now + 60_000,
		SelfID:        client.id,
		SelfEdPub:     client.signPub,
		SelfXPub:      client.xPub,
		Members: []CoordMember{
			{ID: client.id, EdPub: client.signPub, XPub: client.xPub, Self: true, Kind: MemberKindClient},
			{ID: daemon.id, EdPub: daemon.signPub, XPub: daemon.xPub, Kind: MemberKindMachine},
		},
	})

	var agreeOps []CoordKeyAgree
	var signOps []CoordKeySign
	var signalsToDaemon [][]byte
	for _, e := range effs {
		if a, ok := e.(CoordKeyAgree); ok {
			agreeOps = append(agreeOps, a)
		}
	}
	if len(agreeOps) != 1 || agreeOps[0].PeerID != daemon.id {
		t.Fatalf("expected 1 KeyAgree for daemon, got %+v", agreeOps)
	}
	effs = coord.OnKeyAgreed(now, agreeOps[0].OpID, client.agree(daemon.xPub), true)

	var daemonSess *linkcore.Session
	daemonInst := make([]byte, 16)
	daemonBoot := make([]byte, 32)
	for i := range daemonInst {
		daemonInst[i] = byte(i + 1)
		daemonBoot[i] = byte(i + 33)
	}
	daemonPub := peerwire.Public{ID: []byte(daemon.id), Role: 2, Instance: daemonInst, Boot: daemonBoot, Sign: daemon.signPub[:], Box: daemon.xPub[:]}
	clientMember := peerwire.Public{ID: []byte(client.id), Role: 1, Sign: client.signPub[:], Box: client.xPub[:]}

	type dPC struct {
		use       uint64
		sdp       []byte
		remote    []byte
		connected bool
	}
	daemonPCs := map[uint64]*dPC{}
	clientPCs := map[uint64]*dPC{}
	var clientReadyCount int
	var daemonToClient [][]byte
	var daemonCapSent bool
	var daemonSawSub bool

	var handleCoordEffs func([]CoordEffect)
	handleCoordEffs = func(list []CoordEffect) {
		for _, e := range list {
			switch e := e.(type) {
			case CoordKeyAgree:
				agreeOps = append(agreeOps, e)
			case CoordKeySign:
				signOps = append(signOps, e)
			case CoordSignalSend:
				raw := peerwire.PB(
					peerwire.B(1, []byte(client.id)),
					peerwire.B(2, e.ToDevice),
					peerwire.U(3, uint64(e.FrameClass)),
					peerwire.B(4, e.FrameID),
					peerwire.B(5, e.Sealed),
				)
				signalsToDaemon = append(signalsToDaemon, raw)
			case CoordPcCreate:
				clientPCs[e.PCID] = &dPC{use: uint64(e.Use)}
				handleCoordEffs(coord.OnPcCreated(now, e.PCID, true))
			case CoordPcDescribe:
				sdp := fakeCoordSDP(int(e.PCID))
				clientPCs[e.PCID].sdp = sdp
				handleCoordEffs(coord.OnPcDescription(now, e.PCID, sdp, true))
				handleCoordEffs(coord.OnPcLocalCandidate(now, e.PCID, "candidate:1 1 udp 2130706431 203.0.113.5 50000 typ srflx raddr 192.168.1.2 rport 50000", false))
				handleCoordEffs(coord.OnPcLocalCandidate(now, e.PCID, "", true))
			case CoordPcSetRemote:
				clientPCs[e.PCID].remote = e.SDP
				handleCoordEffs(coord.OnPcRemoteSet(now, e.PCID, true, ""))
			case CoordPcAddCandidates:
				handleCoordEffs(coord.OnPcCandidatesAdded(now, e.PCID, true, ""))
			case CoordPcConfirm:
				clientReadyCount++
			}
		}
	}

	var handleDaemonEffs func([]linkcore.Effect)
	handleDaemonEffs = func(list []linkcore.Effect) {
		for _, e := range list {
			switch e := e.(type) {
			case linkcore.SignFrame:
				sig := ed25519.Sign(daemon.signPriv, e.Message)
				handleDaemonEffs(daemonSess.Handle(linkcore.FrameSigned{Op: e.Op, Signature: sig}, uint64(now)))
			case linkcore.TransmitFrame:
				daemonToClient = append(daemonToClient, e.Raw)
			case linkcore.CreatePC:
				daemonPCs[e.ID] = &dPC{use: e.Use}
				handleDaemonEffs(daemonSess.Handle(linkcore.PCCreated{ID: e.ID, OK: true}, uint64(now)))
			case linkcore.DescribePC:
				sdp := fakeCoordSDP(int(e.ID) + 100)
				daemonPCs[e.ID].sdp = sdp
				handleDaemonEffs(daemonSess.Handle(linkcore.PCDescribed{ID: e.ID, SDP: sdp, OK: true}, uint64(now)))
				handleDaemonEffs(daemonSess.Handle(linkcore.PCLocalCandidate{ID: e.ID, Candidate: []byte("candidate:1 1 udp 2130706431 203.0.113.9 50001 typ srflx"), Valid: true}, uint64(now)))
				handleDaemonEffs(daemonSess.Handle(linkcore.PCLocalCandidate{ID: e.ID, End: true, Valid: true}, uint64(now)))
			case linkcore.SetRemotePC:
				daemonPCs[e.ID].remote = e.SDP
				handleDaemonEffs(daemonSess.Handle(linkcore.PCRemoteSet{ID: e.ID, OK: true}, uint64(now)))
			case linkcore.AddCandidatesPC:
				handleDaemonEffs(daemonSess.Handle(linkcore.PCCandidatesAdded{ID: e.ID, OK: true}, uint64(now)))
			case linkcore.HandleControl:
				if e.Frame.Type == 1 || e.Frame.Type == 15 {
					if e.Frame.Type == 15 {
						daemonSawSub = true
					}
					handleDaemonEffs(daemonSess.Handle(linkcore.ControlHandled{Op: e.Op, Type: 4, Body: ackPayload(e.Frame)}, uint64(now)))
				} else {
					handleDaemonEffs(daemonSess.Handle(linkcore.ControlHandled{Op: e.Op, Type: 0}, uint64(now)))
				}
			case linkcore.ConfirmPC:
				if !daemonCapSent && e.Use == 1 {
					daemonCapSent = true
					mc := peerwire.PB(peerwire.U(2, 1), peerwire.U(5, 65536), peerwire.U(13, 1))
					cap14 := peerwire.PB(peerwire.B(1, make([]byte, 16)), peerwire.B(2, mc), peerwire.U(3, uint64(now+30_000)))
					handleDaemonEffs(daemonSess.Handle(linkcore.QueueControl{Type: 14, Body: cap14}, uint64(now)))
					cap1 := peerwire.PB(peerwire.B(1, make([]byte, 16)), peerwire.U(2, 15), peerwire.U(4, uint64(now+30_000)), peerwire.U(5, 1), peerwire.U(6, 1048576))
					handleDaemonEffs(daemonSess.Handle(linkcore.QueueControl{Type: 1, Body: cap1}, uint64(now)))
				}
			}
		}
	}

	handleCoordEffs(effs)

	for step := 0; step < 30 && (!daemonSawSub || clientReadyCount < 2 || !coord.Ready(now, daemon.id, Hint{Use: UseBulk, Upload: UploadFile, Bytes: 4096})); step++ {
		for len(signOps) > 0 {
			s := signOps[0]
			signOps = signOps[1:]
			sig := ed25519.Sign(client.signPriv, s.Message)
			handleCoordEffs(coord.OnKeySigned(now, s.OpID, sig))
		}
		for len(signalsToDaemon) > 0 {
			raw := signalsToDaemon[0]
			signalsToDaemon = signalsToDaemon[1:]
			if daemonSess == nil {
				opened, err := codec.Open(raw, clientMember, daemonPub, daemon.agree(client.xPub), uint64(now))
				if err != nil {
					t.Fatalf("daemon open initial frame: %v", err)
				}
				var sess *linkcore.Session
				var initEffs []linkcore.Effect
				sess, initEffs, err = linkcore.NewSession(linkcore.SessionConfig{
					Self: daemonPub,
					Peer: peerwire.Public{
						ID:       []byte(client.id),
						Role:     1,
						Instance: opened.Context.SenderInstance,
						Boot:     opened.Context.SenderBoot,
						Sign:     client.signPub[:],
						Box:      client.xPub[:],
					},
					Shared:     daemon.agree(client.xPub),
					Codec:      codec,
					Connection: opened.Context.Connection,
					Deadline:   opened.Context.Deadline,
					Entropy:    rand.Reader,
					Allow:      func() bool { return true },
				}, false, uint64(now))
				if err != nil {
					t.Fatal(err)
				}
				daemonSess = sess
				handleDaemonEffs(initEffs)
			}
			handleDaemonEffs(daemonSess.Handle(linkcore.FrameReceived{Raw: raw}, uint64(now)))
		}
		for len(daemonToClient) > 0 {
			raw := daemonToClient[0]
			daemonToClient = daemonToClient[1:]
			handleCoordEffs(coord.OnSignalReceived(now, raw))
		}
		for pcID, cpc := range clientPCs {
			for dID, dpc := range daemonPCs {
				if !cpc.connected && !dpc.connected && cpc.sdp != nil && string(dpc.remote) == string(cpc.sdp) && dpc.sdp != nil && string(cpc.remote) == string(dpc.sdp) {
					cpc.connected, dpc.connected = true, true
					handleCoordEffs(coord.OnPcState(now, pcID, StateConn, "connected"))
					handleCoordEffs(coord.OnDcState(now, pcID, true))
					handleDaemonEffs(daemonSess.Handle(linkcore.PCStateChanged{ID: dID, State: linkcore.PCConnected, Valid: true}, uint64(now)))
				}
			}
		}
		now += 25
		if engAt, ok, _, _, _, _ := coord.NextTimers(now); ok && now >= engAt {
			handleCoordEffs(coord.OnEngineTimer(now))
		}
		handleCoordEffs(coord.OnSessionTimer(now))
		if daemonSess != nil {
			handleDaemonEffs(daemonSess.Handle(linkcore.Tick{}, uint64(now)))
		}
	}

	if clientReadyCount != 2 {
		t.Fatalf("clientReadyCount=%d, want 2", clientReadyCount)
	}
	st := coord.Status(now, daemon.id)
	if st.State != StateConnected || st.Read != Direct || st.File != Direct {
		t.Fatalf("unexpected direct status after handshake: %+v", st)
	}
	if !coord.Ready(now, daemon.id, Hint{Use: UseBulk, Upload: UploadFile, Bytes: 4096}) {
		t.Fatal("expected bulk upload ready after Control 14 CapabilitySnapshot")
	}
	if !daemonSawSub {
		t.Fatal("expected daemon to receive Control 15 DirectEventSubscription")
	}

	// Verify command route tickets & diagnostics rounds.
	dec := coord.PrepareAttempt(now, daemon.id, Hint{Use: UseRead}, now+3000, false)
	if !dec.UseDirect || dec.PCID == 0 || dec.TicketID == 0 {
		t.Fatalf("expected direct read decision, got %+v", dec)
	}
	coord.ObserveTicket(now+20, dec.TicketID, true, 512, 12.0)
	rounds, _, _ := coord.Diagnostics(daemon.id)
	if len(rounds) == 0 || !rounds[0].HasEstablished || VerdictOf(rounds[0], false) != VerdictConnected {
		t.Fatalf("unexpected diagnostics rounds: %+v", rounds)
	}
}

func TestCoordinatorUnsupportedAndTerminalReopen(t *testing.T) {
	client := newCoordActor(t, 3, 1)
	daemon := newCoordActor(t, 4, 2)
	domain := sha256.Sum256([]byte("d2"))
	headHash := sha256.Sum256([]byte("h2"))
	codec, _ := peerwire.NewCodec("00000000000000000000000000000001", domain, headHash, 1, 17)
	now := int64(1_700_000_000_000)

	coord := NewCoordinator(CoordConfig{Supported: true, Account: "00000000000000000000000000000001", ProtocolVersion: 17})
	// Fixed entropy: reconnect backoff uses full jitter, so random bytes can
	// draw a delay of ~0 ms and reopen immediately, failing the "backoff before
	// second reopen" check about once in a few hundred runs.
	coord.ProvideRandomBytes(now, fixedEntropy(CoordEntropyRefillBytes))
	coord.SetMainLeg(now, true)
	coord.OnSecuritySnapshot(now, CoordSecuritySnapshot{
		Member: true, Account: "00000000000000000000000000000001", Domain: domain,
		Head: "h2", HeadHash: headHash, Epoch: 1, StartedAt: now, LeaseNotAfter: now + 60_000,
		SelfID: client.id, SelfEdPub: client.signPub, SelfXPub: client.xPub,
		Members: []CoordMember{
			{ID: client.id, EdPub: client.signPub, XPub: client.xPub, Self: true, Kind: MemberKindClient},
			{ID: daemon.id, EdPub: daemon.signPub, XPub: daemon.xPub, Kind: MemberKindMachine},
		},
	})
	var opID uint64
	for k := range coord.agreeOps {
		opID = k
	}
	effs := coord.OnKeyAgreed(now, opID, client.agree(daemon.xPub), true)
	var sign CoordKeySign
	for _, e := range effs {
		if s, ok := e.(CoordKeySign); ok {
			sign = s
		}
	}
	effs = coord.OnKeySigned(now, sign.OpID, ed25519.Sign(client.signPriv, sign.Message))
	if len(effs) == 0 {
		t.Fatal("expected initial Probe signal")
	}

	// Simulate relay returning Limited/Offline for Probe.
	var probeID []byte
	for _, e := range effs {
		if s, ok := e.(CoordSignalSend); ok {
			probeID = s.FrameID
		}
	}
	coord.OnSignalDelivery(now, probeID, 2, 0)
	rounds, _, _ := coord.Diagnostics(daemon.id)
	if len(rounds) != 1 || rounds[0].Offline != 1 {
		t.Fatalf("expected Offline count 1 in diag, got %+v", rounds)
	}

	// Terminal reopen flows through shared budget & NextTimers.
	effs = coord.TerminalClosed(now, "term-1", true, true)
	if len(effs) != 1 {
		t.Fatalf("expected immediate CoordTerminalOpen on first close after live, got %+v", effs)
	}
	if effs := coord.TerminalClosed(now+1, "term-1", true, true); len(effs) != 0 {
		t.Fatalf("expected backoff before second reopen, got %+v", effs)
	}
	_, _, _, _, termAt, termOK := coord.NextTimers(now + 1)
	if !termOK || termAt <= now+1 {
		t.Fatalf("expected terminal timer scheduled, got ok=%v at=%d", termOK, termAt)
	}
	effs = coord.OnTerminalTimer(termAt)
	if len(effs) != 1 {
		t.Fatalf("expected CoordTerminalOpen when timer fires, got %+v", effs)
	}
	_ = codec
}

func TestCoordinatorEntropyPoolLowWaterExhaustionAndRefill(t *testing.T) {
	client := newCoordActor(t, 5, 1)
	daemon := newCoordActor(t, 6, 2)
	domain := sha256.Sum256([]byte("ent-domain"))
	headHash := sha256.Sum256([]byte("ent-head"))
	now := int64(1_700_000_000_000)

	coord := NewCoordinator(CoordConfig{
		Supported:       true,
		Account:         "00000000000000000000000000000001",
		ProtocolVersion: 17,
	})
	effs := coord.Configure(now, CoordConfig{
		Supported:       true,
		Account:         "00000000000000000000000000000001",
		ProtocolVersion: 17,
	})
	// Re-check initial low-water request on fresh Coordinator via Configure flip or direct ProvideRandomBytes.
	effs = append(effs, coord.ProvideRandomBytes(now, testOSRandom(32))...) // < 48 B instance requirement
	var sawNeed int
	for _, e := range effs {
		if ne, ok := e.(CoordNeedEntropy); ok {
			sawNeed = ne.MinRandomBytes
		}
	}
	if sawNeed < CoordEntropyRefillBytes {
		t.Fatalf("expected CoordNeedEntropy >= %d when pool has < 48 B, got %d", CoordEntropyRefillBytes, sawNeed)
	}
	if coord.HasInstance() {
		t.Fatal("expected HasInstance() == false with only 32 B")
	}

	// Engine RequestEntropy while pool lacks 16*8=128 B queues the jitter refill;
	// also queue an outbound StartRound before instance/entropy is sufficient.
	coord.SetMainLeg(now, true)
	snapEffs := coord.OnSecuritySnapshot(now, CoordSecuritySnapshot{
		Member: true, Account: "00000000000000000000000000000001", Domain: domain,
		Head: "head-ent", HeadHash: headHash, Epoch: 1, StartedAt: now, LeaseNotAfter: now + 60_000,
		SelfID: client.id, SelfEdPub: client.signPub, SelfXPub: client.xPub,
		Members: []CoordMember{
			{ID: client.id, EdPub: client.signPub, XPub: client.xPub, Self: true, Kind: MemberKindClient},
			{ID: daemon.id, EdPub: daemon.signPub, XPub: daemon.xPub, Kind: MemberKindMachine},
		},
	})
	for _, e := range snapEffs {
		if a, ok := e.(CoordKeyAgree); ok {
			coord.OnKeyAgreed(now, a.OpID, client.agree(daemon.xPub), true)
		}
	}
	// Outbound start should be queued waiting for instance/entropy, not lost.
	if len(coord.pendingStarts) != 1 || len(coord.rounds) != 0 {
		t.Fatalf("expected queued pendingStart while pool insufficient, pendingStarts=%d rounds=%d", len(coord.pendingStarts), len(coord.rounds))
	}

	// Provide just 80 bytes: 32 + 80 = 112 B total. Enough for instance (48 B),
	// 16 conn + 40 Seal = 56 B? 112-48 = 64 B left, Wait: Engine also wants 128 B!
	// Engine gets skipped on 64 B -> round uses 16+40=56 B, leaving 8 B < low-water,
	// so SignFrame IS emitted AND a low-water refill is requested!
	effs = coord.ProvideRandomBytes(now, testOSRandom(80))
	if !coord.HasInstance() {
		t.Fatal("expected HasInstance() == true after >= 48 B")
	}
	var sawSign, sawRefill bool
	for _, e := range effs {
		if _, ok := e.(CoordKeySign); ok {
			sawSign = true
		}
		if ne, ok := e.(CoordNeedEntropy); ok && ne.MinRandomBytes >= CoordEntropyRefillBytes {
			sawRefill = true
		}
	}
	if !sawSign || !sawRefill {
		t.Fatalf("expected queued round to emit CoordKeySign (%v) and request low-water refill (%v)", sawSign, sawRefill)
	}

	// Refill a full 4096 B chunk: pool satisfies pending Engine units and stays above low-water.
	effs = coord.ProvideRandomBytes(now, testOSRandom(CoordEntropyRefillBytes))
	if coord.pendingEngineUnits != 0 {
		t.Fatalf("expected pendingEngineUnits drained after 4096 B refill, got %d", coord.pendingEngineUnits)
	}
	if coord.EntropyAvailable() < CoordEntropyLowWater {
		t.Fatalf("expected EntropyAvailable >= %d after refill, got %d", CoordEntropyLowWater, coord.EntropyAvailable())
	}
	for _, e := range effs {
		if _, ok := e.(CoordNeedEntropy); ok {
			t.Fatal("did not expect redundant CoordNeedEntropy while pool is healthy")
		}
	}
}

func driveEstablishedCoordRound(t *testing.T, serverHost, configuredStun string) (*Coordinator, *coordActor, *coordActor, int64, []string, RoundID, uint64) {
	t.Helper()
	client := newCoordActor(t, 11, 1)
	daemon := newCoordActor(t, 12, 2)
	domain := sha256.Sum256([]byte("d-helper"))
	headHash := sha256.Sum256([]byte("h-helper"))
	acct := "00000000000000000000000000000001"
	codec, err := peerwire.NewCodec(acct, domain, headHash, 1, 17)
	if err != nil {
		t.Fatal(err)
	}
	now := int64(1_700_000_000_000)
	coord := NewCoordinator(CoordConfig{
		Supported:       true,
		Account:         acct,
		ProtocolVersion: 17,
		ServerHost:      serverHost,
		ConfiguredStun:  configuredStun,
	})
	coord.ProvideRandomBytes(now, testOSRandom(CoordEntropyRefillBytes))
	coord.SetMainLeg(now, true)
	effs := coord.OnSecuritySnapshot(now, CoordSecuritySnapshot{
		Member: true, Account: acct, Domain: domain,
		Head: "head-helper", HeadHash: headHash, Epoch: 1, StartedAt: now, LeaseNotAfter: now + 600_000,
		SelfID: client.id, SelfEdPub: client.signPub, SelfXPub: client.xPub,
		Members: []CoordMember{
			{ID: client.id, EdPub: client.signPub, XPub: client.xPub, Self: true, Kind: MemberKindClient},
			{ID: daemon.id, EdPub: daemon.signPub, XPub: daemon.xPub, Kind: MemberKindMachine},
		},
	})
	for _, e := range effs {
		if a, ok := e.(CoordKeyAgree); ok {
			effs = coord.OnKeyAgreed(now, a.OpID, client.agree(daemon.xPub), true)
		}
	}

	daemonInst := make([]byte, 16)
	daemonBoot := make([]byte, 32)
	for i := range daemonInst {
		daemonInst[i] = byte(i + 21)
		daemonBoot[i] = byte(i + 53)
	}
	daemonPub := peerwire.Public{ID: []byte(daemon.id), Role: 2, Instance: daemonInst, Boot: daemonBoot, Sign: daemon.signPub[:], Box: daemon.xPub[:]}
	clientMember := peerwire.Public{ID: []byte(client.id), Role: 1, Sign: client.signPub[:], Box: client.xPub[:]}

	type pcState struct {
		use       uint64
		sdp       []byte
		remote    []byte
		connected bool
	}
	clientPCs := map[uint64]*pcState{}
	daemonPCs := map[uint64]*pcState{}
	var daemonSess *linkcore.Session
	var toDaemon, toClient [][]byte
	var gotStun []string
	var readPC uint64
	var signQ []CoordKeySign

	var handleC func([]CoordEffect)
	var txD [][]byte
	var handleD func([]linkcore.Effect)
	handleD = func(effs []linkcore.Effect) {
		for _, e := range effs {
			switch e := e.(type) {
			case linkcore.SignFrame:
				sig := ed25519.Sign(daemon.signPriv, e.Message)
				handleD(daemonSess.Handle(linkcore.FrameSigned{Op: e.Op, Signature: sig}, uint64(now)))
			case linkcore.TransmitFrame:
				txD = append(txD, e.Raw)
			case linkcore.CreatePC:
				daemonPCs[e.ID] = &pcState{use: e.Use}
				handleD(daemonSess.Handle(linkcore.PCCreated{ID: e.ID, OK: true}, uint64(now)))
			case linkcore.DescribePC:
				p := daemonPCs[e.ID]
				p.sdp = fakeCoordSDP(int(e.ID + 200))
				handleD(daemonSess.Handle(linkcore.PCDescribed{ID: e.ID, OK: true, SDP: p.sdp}, uint64(now)))
			case linkcore.SetRemotePC:
				daemonPCs[e.ID].remote = e.SDP
				handleD(daemonSess.Handle(linkcore.PCRemoteSet{ID: e.ID, OK: true}, uint64(now)))
			case linkcore.AddCandidatesPC:
				handleD(daemonSess.Handle(linkcore.PCCandidatesAdded{ID: e.ID, OK: true}, uint64(now)))
			}
		}
	}
	handleC = func(list []CoordEffect) {
		for _, e := range list {
			switch e := e.(type) {
			case CoordKeySign:
				signQ = append(signQ, e)
			case CoordSignalSend:
				toDaemon = append(toDaemon, peerwire.PB(
					peerwire.B(1, []byte(client.id)),
					peerwire.B(2, e.ToDevice),
					peerwire.U(3, uint64(e.FrameClass)),
					peerwire.B(4, e.FrameID),
					peerwire.B(5, e.Sealed),
				))
				coord.OnSignalDelivery(now, e.FrameID, 1, 0)
			case CoordPcCreate:
				if e.Use == UseRead && gotStun == nil {
					gotStun = append([]string(nil), e.STUN...)
					readPC = e.PCID
				}
				clientPCs[e.PCID] = &pcState{use: uint64(e.Use)}
				handleC(coord.OnPcCreated(now, e.PCID, true))
			case CoordPcDescribe:
				p := clientPCs[e.PCID]
				p.sdp = fakeCoordSDP(int(e.PCID))
				handleC(coord.OnPcDescription(now, e.PCID, p.sdp, true))
			case CoordPcSetRemote:
				clientPCs[e.PCID].remote = e.SDP
				handleC(coord.OnPcRemoteSet(now, e.PCID, true, ""))
			case CoordPcAddCandidates:
				handleC(coord.OnPcCandidatesAdded(now, e.PCID, true, ""))
			}
		}
	}
	handleC(effs)
	_ = toClient
	for step := 0; step < 30 && !coord.Ready(now, daemon.id, Hint{Use: UseRead}); step++ {
		for len(signQ) > 0 {
			s := signQ[0]
			signQ = signQ[1:]
			handleC(coord.OnKeySigned(now, s.OpID, ed25519.Sign(client.signPriv, s.Message)))
		}
		for len(toDaemon) > 0 {
			raw := toDaemon[0]
			toDaemon = toDaemon[1:]
			if daemonSess == nil {
				opened, err := codec.Open(raw, clientMember, daemonPub, daemon.agree(client.xPub), uint64(now))
				if err != nil {
					t.Fatal(err)
				}
				sess, initEffs, err := linkcore.NewSession(linkcore.SessionConfig{
					Self: daemonPub,
					Peer: peerwire.Public{
						ID: []byte(client.id), Role: 1,
						Instance: opened.Context.SenderInstance, Boot: opened.Context.SenderBoot,
						Sign: client.signPub[:], Box: client.xPub[:],
					},
					Shared:     daemon.agree(client.xPub),
					Codec:      codec,
					Connection: opened.Context.Connection,
					Deadline:   opened.Context.Deadline,
					Entropy:    rand.Reader,
					Allow:      func() bool { return true },
				}, false, uint64(now))
				if err != nil {
					t.Fatal(err)
				}
				daemonSess = sess
				handleD(initEffs)
			}
			handleD(daemonSess.Handle(linkcore.FrameReceived{Raw: raw}, uint64(now)))
		}
		for len(txD) > 0 {
			raw := txD[0]
			txD = txD[1:]
			handleC(coord.OnSignalReceived(now, raw))
		}
		for cid, cpc := range clientPCs {
			for did, dpc := range daemonPCs {
				if !cpc.connected && !dpc.connected && cpc.use == dpc.use && cpc.sdp != nil && dpc.sdp != nil &&
					string(dpc.remote) == string(cpc.sdp) && string(cpc.remote) == string(dpc.sdp) {
					cpc.connected, dpc.connected = true, true
					handleC(coord.OnPcState(now, cid, StateConn, "connected"))
					handleC(coord.OnDcState(now, cid, true))
					handleD(daemonSess.Handle(linkcore.PCStateChanged{ID: did, State: linkcore.PCConnected, Valid: true}, uint64(now)))
				}
			}
		}
		now += 25
		handleC(coord.OnEngineTimer(now))
		handleC(coord.OnSessionTimer(now))
		if daemonSess != nil {
			handleD(daemonSess.Handle(linkcore.Tick{}, uint64(now)))
		}
	}
	if !coord.Ready(now, daemon.id, Hint{Use: UseRead}) {
		t.Fatalf("expected coordinator read path ready")
	}
	var rid RoundID
	for id := range coord.rounds {
		rid = id
	}
	return coord, client, daemon, now, gotStun, rid, readPC
}

func TestCoordinatorNextTimersNoBusyLoopAfterDeadline(t *testing.T) {
	coord, _, _, now, _, rid, readPC := driveEstablishedCoordRound(t, "2001:db8::1", "")
	r := coord.rounds[rid]
	if r == nil {
		t.Fatal("expected active round")
	}
	// Advance past RoundMs deadline AND past RetainUntil (Deadline + 300_000 ms).
	afterDeadline := int64(r.session.Deadline()) + 100
	coord.OnSessionTimer(afterDeadline)
	_, _, sessAt, sessOK, _, _ := coord.NextTimers(afterDeadline)
	if sessOK && sessAt <= afterDeadline {
		t.Fatalf("established live round past Deadline must not spin NextTimers, got sessOK=%v sessAt=%d (now=%d)", sessOK, sessAt, afterDeadline)
	}

	pastRetain := int64(r.session.RetainUntil()) + 100
	coord.OnSessionTimer(pastRetain)
	_, _, sessAt, sessOK, _, _ = coord.NextTimers(pastRetain)
	if sessOK && sessAt <= pastRetain {
		t.Fatalf("unretired live round past RetainUntil must not spin NextTimers, got sessOK=%v sessAt=%d (now=%d)", sessOK, sessAt, pastRetain)
	}

	// Closing all remaining PCs after deadlineTicked ends the session immediately.
	coord.OnPcClosed(pastRetain+10, readPC)
	if _, _, _, sessOK, _, _ = coord.NextTimers(pastRetain + 10); sessOK {
		t.Fatalf("expected no session timers after live PCs close")
	}
	for id, rem := range coord.rounds {
		if id == rid && !rem.ended {
			t.Fatalf("expected post-deadline round to end once live PCs close")
		}
	}

	// Retired round must schedule RetainUntil and end cleanly when RetainUntil fires.
	client2 := newCoordActor(t, 13, 1)
	daemon2 := newCoordActor(t, 14, 2)
	dom2 := sha256.Sum256([]byte("d-retire"))
	hh2 := sha256.Sum256([]byte("h-retire"))
	acct2 := "00000000000000000000000000000001"
	t2 := int64(1_700_000_000_000)
	coord2 := NewCoordinator(CoordConfig{Supported: true, Account: acct2, ProtocolVersion: 17, ServerHost: "127.0.0.1"})
	coord2.ProvideRandomBytes(t2, testOSRandom(CoordEntropyRefillBytes))
	coord2.SetMainLeg(t2, true)
	effs2 := coord2.OnSecuritySnapshot(t2, CoordSecuritySnapshot{
		Member: true, Account: acct2, Domain: dom2, Head: "head-retire", HeadHash: hh2, Epoch: 1,
		StartedAt: t2, LeaseNotAfter: t2 + 600_000, SelfID: client2.id, SelfEdPub: client2.signPub, SelfXPub: client2.xPub,
		Members: []CoordMember{
			{ID: client2.id, EdPub: client2.signPub, XPub: client2.xPub, Self: true, Kind: MemberKindClient},
			{ID: daemon2.id, EdPub: daemon2.signPub, XPub: daemon2.xPub, Kind: MemberKindMachine},
		},
	})
	for _, e := range effs2 {
		if a, ok := e.(CoordKeyAgree); ok {
			coord2.OnKeyAgreed(t2, a.OpID, client2.agree(daemon2.xPub), true)
		}
	}
	if len(coord2.rounds) != 1 {
		t.Fatalf("expected 1 unconfirmed round before retire, got %d", len(coord2.rounds))
	}
	var rid2 RoundID
	var r2 *coordRound
	for id, r := range coord2.rounds {
		rid2, r2 = id, r
	}
	retireAt := t2 + 100
	_ = coord2.stepSession(retireAt, r2, linkcore.RetireRound{})
	if !r2.session.View().Retired || r2.ended {
		t.Fatalf("expected unconfirmed round retired without ending, retired=%v ended=%v", r2.session.View().Retired, r2.ended)
	}
	_, _, retAt, retOK, _, _ := coord2.NextTimers(retireAt)
	if !retOK || retAt != int64(r2.session.RetainUntil()) {
		t.Fatalf("expected retired round NextTimers at RetainUntil=%d, got ok=%v at=%d", r2.session.RetainUntil(), retOK, retAt)
	}
	coord2.OnSessionTimer(retAt)
	if !r2.ended || coord2.rounds[rid2] != nil {
		t.Fatalf("expected retired round to terminate when RetainUntil timer fires, ended=%v", r2.ended)
	}
	_ = now
}

func TestCoordinatorFiltersNonMachineMembers(t *testing.T) {
	client := newCoordActor(t, 21, 1)
	phone := newCoordActor(t, 22, 1)
	daemon := newCoordActor(t, 23, 2)
	domain := sha256.Sum256([]byte("filter-domain"))
	headHash := sha256.Sum256([]byte("filter-head"))
	acct := "00000000000000000000000000000001"
	codec, _ := peerwire.NewCodec(acct, domain, headHash, 1, 17)
	now := int64(1_700_000_000_000)

	coord := NewCoordinator(CoordConfig{Supported: true, Account: acct, ProtocolVersion: 17, ServerHost: "ra.example.com"})
	coord.ProvideRandomBytes(now, testOSRandom(CoordEntropyRefillBytes))
	coord.SetMainLeg(now, true)
	effs := coord.OnSecuritySnapshot(now, CoordSecuritySnapshot{
		Member: true, Account: acct, Domain: domain,
		Head: "head-filter", HeadHash: headHash, Epoch: 1, StartedAt: now, LeaseNotAfter: now + 60_000,
		SelfID: client.id, SelfEdPub: client.signPub, SelfXPub: client.xPub,
		Members: []CoordMember{
			{ID: client.id, EdPub: client.signPub, XPub: client.xPub, Self: true, Kind: MemberKindClient},
			{ID: phone.id, EdPub: phone.signPub, XPub: phone.xPub, Kind: MemberKindClient},
			{ID: daemon.id, EdPub: daemon.signPub, XPub: daemon.xPub, Kind: MemberKindMachine},
		},
	})
	for _, e := range effs {
		if a, ok := e.(CoordKeyAgree); ok && a.PeerID == phone.id {
			t.Fatalf("must not initiate key agree with non-machine client member %s", phone.id)
		}
	}
	if len(coord.engine.machines) != 1 || coord.engine.machines[0] != daemon.id {
		t.Fatalf("expected only machine member %s in engine.machines, got %v", daemon.id, coord.engine.machines)
	}

	// Inbound signal from a non-machine member must be dropped before opening or starting a round.
	phonePub := peerwire.Public{ID: []byte(phone.id), Role: 1, Instance: testOSRandom(16), Boot: testOSRandom(32), Sign: phone.signPub[:], Box: phone.xPub[:]}
	clientPub := peerwire.Public{ID: []byte(client.id), Role: 1, Sign: client.signPub[:], Box: client.xPub[:]}
	c := peerwire.NewContext(phonePub, clientPub, testOSRandom(16), 0, 1, 1, uint64(now), 2, false)
	c.Deadline = uint64(now + 9000)
	unsigned, err := codec.Prepare(phonePub, clientPub, phone.agree(client.xPub), c, 1, nil, testOSRandom(peerwire.SealEntropy))
	if err != nil {
		t.Fatal(err)
	}
	sig := ed25519.Sign(phone.signPriv, unsigned.Message)
	raw, err := unsigned.Finish(sig)
	if err != nil {
		t.Fatal(err)
	}
	roundsBefore := len(coord.rounds)
	coord.OnSignalReceived(now, raw)
	if len(coord.rounds) != roundsBefore || len(coord.pendingSignals) != 0 {
		t.Fatalf("expected inbound signal from non-machine %s to be ignored", phone.id)
	}
}

func TestCoordinatorSecurityInstalledAuthorizeDoneAndStun(t *testing.T) {
	coord, _, daemon, now, stun, _, _ := driveEstablishedCoordRound(t, "2001:db8::1", "")
	if len(stun) != 1 || stun[0] != "stun:[2001:db8::1]:3478" {
		t.Fatalf("expected Core-computed IPv6 STUN list, got %v", stun)
	}
	// Configure with override STUN caps at 2 and updates CoordPcCreate list.
	coord.Configure(now+1, CoordConfig{
		Supported: true, Account: "00000000000000000000000000000001", ProtocolVersion: 17,
		ServerHost: "ignored.example.com", ConfiguredStun: "stun:a:3478, stun:b:3478, stun:c:3478",
	})
	if len(coord.stunServers) != 2 || coord.stunServers[0] != "stun:a:3478" || coord.stunServers[1] != "stun:b:3478" {
		t.Fatalf("expected capped override STUN list, got %v", coord.stunServers)
	}

	// Matching OnSecurityInstalled must be a no-op (keep existing direct readiness).
	effs := coord.OnSecurityInstalled(now+2, true, "00000000000000000000000000000001", "head-helper")
	if len(effs) != 0 || !coord.Ready(now+2, daemon.id, Hint{Use: UseRead}) {
		t.Fatalf("matching SecurityInstalled must preserve readiness and emit no effects, got %+v", effs)
	}

	// Mismatched head OnSecurityInstalled invalidates active rounds and asks Authorize (without granting freshness).
	effs = coord.OnSecurityInstalled(now+3, true, "00000000000000000000000000000001", "head-new")
	var sawAuth bool
	for _, e := range effs {
		if _, ok := e.(CoordAuthorize); ok {
			sawAuth = true
		}
	}
	if !sawAuth || coord.Ready(now+3, daemon.id, Hint{Use: UseRead}) || !coord.engine.authorizing {
		t.Fatalf("expected SecurityInstalled with new head to invalidate & trigger Authorize, sawAuth=%v authorizing=%v", sawAuth, coord.engine.authorizing)
	}

	// OnAuthorizeDone closes the authorizing gate even when sync was skipped/failed.
	coord.OnAuthorizeDone(now+4, false)
	if coord.engine.authorizing {
		t.Fatal("expected OnAuthorizeDone to clear authorizing gate")
	}
}

func TestCoordinatorPerUsePeerUnsupported(t *testing.T) {
	coord, _, _, now, _, rid, _ := driveEstablishedCoordRound(t, "127.0.0.1", "")
	r := coord.rounds[rid]
	if r == nil || !r.everReady {
		t.Fatal("expected everReady round")
	}
	// Suppose peer reports Unsupported for Bulk (Use=2) after Read (Use=1) already succeeded.
	coord.applySessionEffects(now, r, []linkcore.Effect{linkcore.PeerUnsupported{Class: 1, Type: 3, Use: uint64(UseBulk)}})
	effs := func() []CoordEffect {
		coord.finishSession(now+1, r, "paths_closed")
		return coord.take()
	}()
	// Round must NOT be classified as EndPeerUnsupported while Read had succeeded.
	_ = effs
	rounds, _, _ := coord.Diagnostics(r.machine)
	if len(rounds) == 0 {
		t.Fatal("expected diagnostic round recorded")
	}
	if rounds[len(rounds)-1].End == EndPeerUnsupported {
		t.Fatalf("per-use Unsupported on UseBulk after Read became ready must not mark EndPeerUnsupported: %+v", rounds[len(rounds)-1])
	}
}

func TestCoordinatorProbeLivenessAndTimeout(t *testing.T) {
	coord, _, _, now, _, rid, _ := driveEstablishedCoordRound(t, "127.0.0.1", "")
	r := coord.rounds[rid]
	if r == nil {
		t.Fatal("expected active round")
	}
	pcID := r.globalPC[UseRead]
	if pcID == 0 {
		t.Fatal("expected read PC")
	}
	// Ensure probe tracker started once DC opened + confirmed.
	_, _, sessAt, sessOK, _, _ := coord.NextTimers(now)
	if !sessOK || sessAt > now+int64(linkcore.ProbeIntervalMs) {
		t.Fatalf("expected probe timer scheduled within %dms, got ok=%v at=%d (now=%d)", linkcore.ProbeIntervalMs, sessOK, sessAt, now)
	}
	// First probe fires at +1000ms and gets echoed back -> keeps PC alive.
	t1 := now + int64(linkcore.ProbeIntervalMs)
	effs := coord.OnSessionTimer(t1)
	var probeRaw []byte
	for _, e := range effs {
		if sp, ok := e.(CoordPcSendProbe); ok && sp.PCID == pcID {
			probeRaw = append([]byte(nil), sp.Raw...)
		}
	}
	if len(probeRaw) != linkcore.ProbeFrameBytes || probeRaw[0] != 0 {
		t.Fatalf("expected 17-byte probe request, got len=%d", len(probeRaw))
	}
	probeRaw[0] = 1
	coord.OnPcProbeReceived(t1+20, pcID, probeRaw)

	// Verify the probe RTT sample is retained on Status.Measured.
	st := coord.Status(t1+20, r.machine)
	if st.Measured.ProbeSampleCount != 1 || st.Measured.ProbeSamples[0].RttMs != 20 {
		t.Fatalf("expected 1 probe sample of 20ms, got count=%d samples=%+v", st.Measured.ProbeSampleCount, st.Measured.ProbeSamples)
	}

	// Send 5 more probes to verify capping at 5 most recent samples.
	curSample := t1 + 20
	for _, rtt := range []int64{12, 18, 25, 31, 9} {
		curSample += int64(linkcore.ProbeIntervalMs)
		tickEffs := coord.OnSessionTimer(curSample)
		var raw []byte
		for _, e := range tickEffs {
			if sp, ok := e.(CoordPcSendProbe); ok && sp.PCID == pcID {
				raw = append([]byte(nil), sp.Raw...)
			}
		}
		if len(raw) != linkcore.ProbeFrameBytes {
			t.Fatalf("expected probe frame at %d", curSample)
		}
		raw[0] = 1
		coord.OnPcProbeReceived(curSample+rtt, pcID, raw)
	}
	st = coord.Status(curSample+50, r.machine)
	wantRTTs := []float64{12, 18, 25, 31, 9}
	if st.Measured.ProbeSampleCount != len(wantRTTs) {
		t.Fatalf("expected %d retained probe samples, got %d", len(wantRTTs), st.Measured.ProbeSampleCount)
	}
	for i, w := range wantRTTs {
		if st.Measured.ProbeSamples[i].RttMs != w {
			t.Fatalf("sample[%d]=%v want %v", i, st.Measured.ProbeSamples[i].RttMs, w)
		}
	}

	// Three consecutive missed probes close the PC with reason "probe_timeout" and clear samples.
	var closedReason string
	cur := curSample + 50
	for step := 0; step < 6 && closedReason == ""; step++ {
		cur += int64(linkcore.ProbeIntervalMs)
		for _, e := range coord.OnSessionTimer(cur) {
			if cl, ok := e.(CoordPcClose); ok && cl.PCID == pcID {
				closedReason = cl.Reason
			}
		}
	}
	if closedReason != "probe_timeout" {
		t.Fatalf("expected probe_timeout close after 3 misses, got %q", closedReason)
	}
	if got := coord.Status(cur, r.machine).Measured.ProbeSampleCount; got != 0 {
		t.Fatalf("expected probe samples cleared after close, got %d", got)
	}
}

func TestCoordinatorMarkPCClosedDetachesDirectTerminals(t *testing.T) {
	// Audit core B1: probe_timeout and SetNetworkChanged (closeRoundNow) must
	// detach direct terminals and emit TermStateReconnecting when relay is closed.
	runCase := func(t *testing.T, trigger func(coord *Coordinator, now int64, pcID uint64) []CoordEffect) {
		t.Helper()
		coord, _, _, now, _, rid, _ := driveEstablishedCoordRound(t, "127.0.0.1", "")
		r := coord.rounds[rid]
		readPC := r.globalPC[UseRead]
		// Mark terminal direct ready via Capabilities.
		coord.handleEngine(now, Capabilities{Round: rid, Seq: 1, Terminal: true})
		streamID := "stream-b1-0000000000000000000001"
		_ = coord.TerminalDirectOpen(now+1, streamID, r.machine, "sess-1", []byte("relayleg00000001"))
		_ = coord.TerminalDirectLeg(now+2, streamID, []byte("relayleg00000001"), false, true, 0)
		ct := coord.termStreams[streamID]
		if ct == nil || !ct.directAttached || ct.directPCID != readPC {
			t.Fatalf("expected terminal attached to direct PC %d, got %+v", readPC, ct)
		}
		// Close relay leg so only direct remains.
		ct.relayOpen = false
		ct.stream.CloseLeg("relay", ct.relayGen)

		effs := trigger(coord, now+10, readPC)
		sawReconnecting := false
		for _, e := range effs {
			if st, ok := e.(CoordTerminalState); ok && st.StreamID == streamID && st.State == TermStateReconnecting {
				sawReconnecting = true
			}
		}
		if !sawReconnecting || ct.directAttached || !ct.disconnectedAll {
			t.Fatalf("B1: direct terminal was not detached on PC close (reconnecting=%v directAttached=%v disconnectedAll=%v)",
				sawReconnecting, ct.directAttached, ct.disconnectedAll)
		}
	}

	t.Run("closeRoundNow_NetworkChanged", func(t *testing.T) {
		runCase(t, func(coord *Coordinator, now int64, _ uint64) []CoordEffect {
			return coord.SetNetworkChanged(now)
		})
	})
	t.Run("probe_timeout", func(t *testing.T) {
		runCase(t, func(coord *Coordinator, now int64, pcID uint64) []CoordEffect {
			var all []CoordEffect
			cur := now
			for step := 0; step < 6; step++ {
				cur += int64(linkcore.ProbeIntervalMs)
				all = append(all, coord.OnSessionTimer(cur)...)
			}
			return all
		})
	})
}

func TestCoordinatorLastPCCloseRetainsSessionWindow(t *testing.T) {
	// Audit core B4: closing the sole ready PC via OnPcClosed must retire the
	// session for RetainMs instead of immediately destroying it with not_allowed.
	coord, _, _, now, _, rid, _ := driveEstablishedCoordRound(t, "127.0.0.1", "")
	r := coord.rounds[rid]
	// Close bulk first if present, then close read.
	if bulkPC := r.globalPC[UseBulk]; bulkPC != 0 {
		coord.OnPcClosed(now+1, bulkPC)
	}
	readPC := r.globalPC[UseRead]
	coord.OnPcClosed(now+2, readPC)
	if r.ended {
		t.Fatal("B4: round was immediately ended instead of retained after last PC closed")
	}
	_, _, sessAt, sessOK, _, _ := coord.NextTimers(now + 2)
	if !sessOK || sessAt < int64(r.session.RetainUntil()) {
		t.Fatalf("B4: expected retained replay window timer >= %d, got ok=%v at=%d", r.session.RetainUntil(), sessOK, sessAt)
	}
}

func TestCoordinatorSentFramesRingTracksPast256(t *testing.T) {
	// Audit core B10a: after sending >256 frames on a long-lived round,
	// OnSignalDelivery for the newest frame must still route to the round.
	coord, _, _, now, _, rid, _ := driveEstablishedCoordRound(t, "127.0.0.1", "")
	r := coord.rounds[rid]
	var lastID []byte
	for i := 0; i < 270; i++ {
		id := make([]byte, 16)
		id[0] = byte(i >> 8)
		id[1] = byte(i)
		raw := peerwire.PB(
			peerwire.B(1, coord.selfPub.ID),
			peerwire.B(2, r.peer.ID),
			peerwire.U(3, 2),
			peerwire.B(4, id),
			peerwire.B(5, []byte("x")),
		)
		coord.applySessionEffects(now+int64(i), r, []linkcore.Effect{
			linkcore.TransmitFrame{Raw: raw, Deadline: uint64(now + 5000), Class: 2, Type: 4},
		})
		coord.take()
		lastID = id
	}
	coord.OnSignalDelivery(now+300, lastID, 3, 2000)
	rounds, _, _ := coord.Diagnostics(r.machine)
	if len(rounds) == 0 || rounds[0].Limited == 0 {
		t.Fatalf("B10a: delivery result for frame #270 was dropped (Limited=0)")
	}
}

func TestCoordinatorEarlyReturnEmitsRoundEnded(t *testing.T) {
	// Audit core B11: early return in onStartRound (codec == nil) must emit
	// RoundEnded so Engine does not stay stuck in StateConnecting forever.
	coord := NewCoordinator(CoordConfig{Supported: true, Account: "acct-1", ProtocolVersion: 17})
	now := int64(1_700_000_000_000)
	coord.ProvideRandomBytes(now, make([]byte, 256))
	coord.SetMainLeg(now, true)
	// Supply a security snapshot with Epoch=0 so rebuildCodec leaves codec == nil.
	snap := CoordSecuritySnapshot{
		Member:        true,
		Account:       "acct-1",
		Head:          "head-1",
		Epoch:         0, // codec == nil!
		StartedAt:     now,
		LeaseNotAfter: now + 60_000,
		SelfID:        "client-0000000000000000000000001",
		Members: []CoordMember{
			{ID: "mach-000000000000000000000000001", Kind: MemberKindMachine},
		},
	}
	coord.OnSecuritySnapshot(now, snap)
	st := coord.Status(now, "mach-000000000000000000000000001")
	if st.State == StateConnecting {
		t.Fatalf("B11: Engine stuck in StateConnecting after onStartRound saw codec==nil")
	}
}

func TestCoordinatorInitialEntropyDoesNotWipeFreshness(t *testing.T) {
	// Audit core B12: ProvideRandomBytes receiving initial 48B entropy must
	// trigger discovery without calling NetworkChanged{} (which wipes freshUntil).
	coord := NewCoordinator(CoordConfig{Supported: true, Account: "acct-1", ProtocolVersion: 17})
	now := int64(1_700_000_000_000)
	coord.SetMainLeg(now, true)
	snap := CoordSecuritySnapshot{
		Member:        true,
		Account:       "acct-1",
		Head:          "head-1",
		Epoch:         1,
		StartedAt:     now,
		LeaseNotAfter: now + 60_000,
		SelfID:        "client-0000000000000000000000001",
		Members:       nil, // no machines yet so len(rounds)==0 && len(pendingStarts)==0
	}
	coord.OnSecuritySnapshot(now, snap)
	if coord.engine.freshUntil <= now {
		t.Fatalf("expected valid freshUntil after snapshot, got %d", coord.engine.freshUntil)
	}
	coord.ProvideRandomBytes(now+10, make([]byte, 256))
	if coord.engine.freshUntil <= now+10 {
		t.Fatalf("B12: initial ProvideRandomBytes wiped engine.freshUntil to %d", coord.engine.freshUntil)
	}
}

func TestCoordinatorVerdictCreateFailedReachable(t *testing.T) {
	// Audit core B14: OnPcCreated(false) must leave PC Attempts empty so
	// VerdictOf reports VerdictCreateFailed instead of VerdictNoSDP, and clean localToGlobal/globalPC.
	coord, _, _, now, _, rid, _ := driveEstablishedCoordRound(t, "127.0.0.1", "")
	// Create a fresh round and fail CreatePC on both uses.
	r := coord.rounds[rid]
	machine := r.machine
	coord.SetNetworkChanged(now + 100)
	// Start a new round via OnSecuritySnapshot + CreatePC failure.
	now2 := now + 200
	coord.OnSecuritySnapshot(now2, coord.snap)
	// Find newly created round.
	var newR *coordRound
	for _, id := range coord.sortedRoundIDs() {
		if id != rid {
			newR = coord.rounds[id]
		}
	}
	if newR == nil {
		t.Fatal("expected new round after snapshot")
	}
	coord.handleEngine(now2, DiagMark{Round: newR.id, Use: UseRead, Mark: MarkBoot})
	coord.applySessionEffects(now2, newR, []linkcore.Effect{
		linkcore.CreatePC{ID: 99, Use: uint64(UseRead), Generation: 1},
	})
	pcID := newR.localToGlobal[99]
	if pcID == 0 {
		t.Fatal("expected global pcID allocated")
	}
	coord.OnPcCreated(now2+1, pcID, false)
	if newR.localToGlobal[99] != 0 || newR.globalPC[UseRead] != 0 {
		t.Fatalf("B14: OnPcCreated(false) leaked localToGlobal=%d globalPC=%d", newR.localToGlobal[99], newR.globalPC[UseRead])
	}
	coord.finishSession(now2+10_000, newR, "round_deadline")
	rounds, _, _ := coord.Diagnostics(machine)
	if len(rounds) == 0 {
		t.Fatal("expected diagnostics")
	}
	if v := VerdictOf(rounds[0], false); v != VerdictCreateFailed {
		t.Fatalf("B14: expected VerdictCreateFailed(%d), got %d", VerdictCreateFailed, v)
	}
}

func TestCoordinatorRecordsBatchCapCandidateDropsInDiagnostics(t *testing.T) {
	coord, _, _, now, _, rid, _ := driveEstablishedCoordRound(t, "127.0.0.1", "")
	r := coord.rounds[rid]
	for i := 0; i < 6; i++ {
		coord.applySessionEffects(now+int64(i), r, []linkcore.Effect{
			linkcore.SignalRejected{Use: uint64(UseRead), Type: 5, Reason: "batch cap"},
		})
	}
	rounds, _, _ := coord.Diagnostics(r.machine)
	if len(rounds) == 0 {
		t.Fatal("expected diagnostic round")
	}
	found := false
	for _, rej := range rounds[0].Pcs[0].Rejects {
		if rej.Key == "candidates:batch cap" && rej.N == 6 {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected candidates:batch cap with N=6 in diagnostics, got %+v", rounds[0].Pcs[0].Rejects)
	}
}

func TestCoordinatorSamplesUnconfirmedSiblingPC(t *testing.T) {
	coord, _, _, now, _, rid, _ := driveEstablishedCoordRound(t, "127.0.0.1", "")
	r := coord.rounds[rid]
	// Simulate half-open round: UseRead is confirmed/established while UseBulk is created and stuck in ICE checking.
	r.confirmed[UseBulk] = false
	r.sampledLate[UseBulk] = false
	bulkPC := r.globalPC[UseBulk]
	if bulkPC == 0 {
		t.Fatal("expected bulk PC")
	}
	if b := coord.pcs[bulkPC]; b != nil {
		b.confirmed = false
	}
	// Reset diagnostic sampling state on UseBulk attempt so we observe new ReadStats requests.
	rounds, _, _ := coord.Diagnostics(r.machine)
	bulkAttempt := rounds[0].Pc(UseBulk).current()
	if bulkAttempt == nil {
		t.Fatal("expected bulk attempt")
	}
	bulkAttempt.sampling = false
	bulkAttempt.Samples = 0
	bulkAttempt.Sampled = false

	// Advance to deadline - lateStatsSampleLeadMs: UseBulk must still be scheduled & sampled even though the round is Established.
	lateAt := int64(r.session.Deadline()) - lateStatsSampleLeadMs
	_, _, sessAt, sessOK, _, _ := coord.NextTimers(now)
	if !sessOK || sessAt > lateAt {
		t.Fatalf("expected late sample timer <= %d for unconfirmed UseBulk, got ok=%v at=%d", lateAt, sessOK, sessAt)
	}
	effs := coord.OnSessionTimer(lateAt)
	sawBulkReadStats := false
	for _, e := range effs {
		if rs, ok := e.(CoordDcReadStats); ok && rs.PCID == bulkPC && rs.Use == UseBulk {
			sawBulkReadStats = true
		}
	}
	if !sawBulkReadStats {
		t.Fatalf("expected CoordDcReadStats for unconfirmed UseBulk pc=%d at late sample time, got %+v", bulkPC, effs)
	}
}
