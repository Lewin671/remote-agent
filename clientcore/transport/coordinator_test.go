package transport

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/Lewin671/remote-agent/shared/linkcore"
	"github.com/Lewin671/remote-agent/shared/peerwire"
)

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
		StunServers:     []string{"stun:127.0.0.1:3478"},
	})
	coord.Seed(now, []byte("deterministic-seed-32-bytes-long!!"))
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
			{ID: client.id, EdPub: client.signPub, XPub: client.xPub, Self: true},
			{ID: daemon.id, EdPub: daemon.signPub, XPub: daemon.xPub},
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
	daemonDRBG := linkcore.NewDRBG([]byte("daemon-seed-32-bytes-long-abcdef"))

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
				if e.Frame.Type == 15 {
					daemonSawSub = true
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
					Entropy:    daemonDRBG,
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
	coord.Seed(now, []byte("seed-for-unsupported-and-reopen!"))
	coord.SetMainLeg(now, true)
	coord.OnSecuritySnapshot(now, CoordSecuritySnapshot{
		Member: true, Account: "00000000000000000000000000000001", Domain: domain,
		Head: "h2", HeadHash: headHash, Epoch: 1, StartedAt: now, LeaseNotAfter: now + 60_000,
		SelfID: client.id, SelfEdPub: client.signPub, SelfXPub: client.xPub,
		Members: []CoordMember{
			{ID: client.id, EdPub: client.signPub, XPub: client.xPub, Self: true},
			{ID: daemon.id, EdPub: daemon.signPub, XPub: daemon.xPub},
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
