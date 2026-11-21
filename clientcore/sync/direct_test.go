package sync

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/Lewin671/remote-agent/clientcore/domain/outcome"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/transport"
	"github.com/Lewin671/remote-agent/shared/linkcore"
	"github.com/Lewin671/remote-agent/shared/peerwire"
)

func testSyncRandomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

type directTestActor struct {
	t        *testing.T
	id       string
	signPriv ed25519.PrivateKey
	signPub  [32]byte
	xPriv    *ecdh.PrivateKey
	xPub     [32]byte
}

func newDirectTestActor(t *testing.T, tag byte, role byte) *directTestActor {
	t.Helper()
	seed := sha256.Sum256([]byte{tag, role})
	signPriv := ed25519.NewKeyFromSeed(seed[:])
	signPub := signPriv.Public().(ed25519.PublicKey)
	xSeed := sha256.Sum256([]byte{tag, role, 0x55})
	xPriv, err := ecdh.X25519().NewPrivateKey(xSeed[:])
	if err != nil {
		t.Fatal(err)
	}
	idHash := sha256.Sum256(signPub)
	a := &directTestActor{
		t:        t,
		id:       hex.EncodeToString(idHash[:16]),
		signPriv: signPriv,
		xPriv:    xPriv,
	}
	copy(a.signPub[:], signPub)
	copy(a.xPub[:], xPriv.PublicKey().Bytes())
	return a
}

func (a *directTestActor) agree(peerX [32]byte) []byte {
	pk, err := ecdh.X25519().NewPublicKey(peerX[:])
	if err != nil {
		a.t.Fatal(err)
	}
	sec, err := a.xPriv.ECDH(pk)
	if err != nil {
		a.t.Fatal(err)
	}
	return sec
}

func testDirectSDP(seed int) []byte {
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
	u := []byte("uf0000")
	u[2] = '0' + byte((seed/1000)%10)
	u[3] = '0' + byte((seed/100)%10)
	u[4] = '0' + byte((seed/10)%10)
	u[5] = '0' + byte(seed%10)
	return []byte("v=0\r\no=- 1 2 IN IP4 127.0.0.1\r\ns=-\r\nt=0 0\r\na=group:BUNDLE 0\r\nm=application 9 UDP/DTLS/SCTP webrtc-datachannel\r\nc=IN IP4 0.0.0.0\r\na=ice-ufrag:" + string(u) + "\r\na=ice-pwd:pwdpwdpwdpwdpwdpwdpwdpwd\r\na=fingerprint:sha-256 " + string(fp[:]) + "\r\na=setup:actpass\r\na=mid:0\r\na=sctp-port:5000\r\n")
}

func directAckPayload(frame peerwire.Opened) []byte {
	sum := sha256.Sum256(frame.Payload)
	return peerwire.PB(
		peerwire.U(4, 2),
		peerwire.U(5, frame.Context.Seq),
		peerwire.B(6, sum[:]),
	)
}

type directSyncHarness struct {
	t         *testing.T
	eng       *Engine
	now       int64
	client    *directTestActor
	daemon    *directTestActor
	account   string
	domain    [32]byte
	headHash  [32]byte
	codec     *peerwire.Codec
	daemonSec *linkcore.Session

	wantSeed bool
	readPCID uint64
	bulkPCID uint64
	termOpen []string
	termWait bool
}

func newDirectSyncHarness(t *testing.T) *directSyncHarness {
	t.Helper()
	client := newDirectTestActor(t, 1, 1)
	daemon := newDirectTestActor(t, 2, 2)
	account := "00000000000000000000000000000001"
	domain := sha256.Sum256([]byte("direct-sync-domain"))
	headHash := sha256.Sum256([]byte("direct-sync-head"))
	codec, err := peerwire.NewCodec(account, domain, headHash, 1, 17)
	if err != nil {
		t.Fatal(err)
	}
	return &directSyncHarness{
		t:        t,
		eng:      NewEngine(),
		now:      1_791_078_000_000,
		client:   client,
		daemon:   daemon,
		account:  account,
		domain:   domain,
		headHash: headHash,
		codec:    codec,
	}
}

func (h *directSyncHarness) step(ev *corev1.Event) StepResult {
	h.t.Helper()
	ev.NowMs = h.now
	ev.MonoMs = h.now
	_, res := h.eng.HandleEvent(ev)
	return res
}

func (h *directSyncHarness) connectMainAndList(sessionID string) {
	h.t.Helper()
	// Preload entropy pool so sync never stalls on missing IDs/floats.
	h.step(&corev1.Event{
		RandomIds:  []string{"id1", "id2", "id3", "id4", "id5", "id6", "id7", "id8", "id9", "id10", "id11", "id12", "id13", "id14", "id15", "id16"},
		UnitFloats: []float64{0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5, 0.5},
	})
	res := h.step(&corev1.Event{
		Event: &corev1.Event_SyncLifecycle{SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START}},
	})
	var tokenOp, secOp uint64
	for _, eff := range res.Effects {
		if et := eff.GetEnsureToken(); et != nil {
			tokenOp = et.OpId
		}
		if ss := eff.GetSyncSecurity(); ss != nil {
			secOp = ss.OpId
		}
	}
	res = h.step(&corev1.Event{
		Event: &corev1.Event_TokenResult{TokenResult: &corev1.TokenResultEvent{OpId: tokenOp, Token: "tok-1", ExpiresMs: h.now + 3_600_000}},
	})
	var legID uint64
	for _, eff := range res.Effects {
		if os := eff.GetOpenStream(); os != nil {
			legID = os.LegId
		}
	}
	res = h.step(&corev1.Event{
		Event: &corev1.Event_StreamReady{StreamReady: &corev1.StreamReadyEvent{LegId: legID, ServerTimeMs: h.now}},
	})
	var listOp uint64
	for _, eff := range res.Effects {
		if r := eff.GetRpc(); r != nil && r.GetFetchLists() != nil {
			listOp = r.OpId
		}
	}
	h.step(&corev1.Event{
		Event: &corev1.Event_SecuritySnapshot{
			SecuritySnapshot: &corev1.SecuritySnapshotEvent{
				OpId:              secOp,
				IsMember:          true,
				SelfDeviceId:      h.client.id,
				AuthorityDeviceId: h.client.id,
				TrustedMachineIds: []string{h.daemon.id},
				HeadEpoch:         1,
				LeaseEndMs:        h.now + 3_600_000,
			},
		},
	})
	h.step(&corev1.Event{
		Event: &corev1.Event_ListsFetched{
			ListsFetched: &corev1.ListsFetchedEvent{
				OpId: listOp,
				Machines: []*corev1.PlainMachine{
					{
						MachineId: h.daemon.id,
						Online:    true,
						Meta: &v1.MachineMeta{
							Hostname:        "Workstation",
							ProtocolVersion: ProtocolVersion,
							Capabilities:    &v1.MachineCapabilities{ImageUpload: true, FileUpload: true},
						},
					},
				},
				Sessions: []*corev1.PlainSession{
					{
						SessionId: sessionID,
						MachineId: h.daemon.id,
						LastSeq:   1,
						Meta:      &v1.Session{Id: sessionID, Cwd: "/tmp/proj", Agent: v1.Agent_AGENT_CODEX, Status: v1.SessionStatus_SESSION_STATUS_RUNNING},
					},
				},
			},
		},
	})
	h.step(&corev1.Event{
		Event: &corev1.Event_SessionWatch{SessionWatch: &corev1.SessionWatchEvent{SessionId: sessionID, Watched: true}},
	})
}

func (h *directSyncHarness) driveDirectHandshake() {
	h.t.Helper()
	res := h.step(&corev1.Event{
		Event: &corev1.Event_TransportConfig{
			TransportConfig: &corev1.TransportConfigEvent{
				Supported:       true,
				Account:         h.account,
				ProtocolVersion: ProtocolVersion,
				StunServers:     []string{"stun:127.0.0.1:3478"},
				PlatformAgent:   "TestBrowser/1.0",
			},
		},
	})
	for _, eff := range res.Effects {
		if re := eff.GetRequestEntropy(); re != nil && re.MinRandomBytes > 0 {
			h.wantSeed = true
		}
	}
	if !h.wantSeed {
		h.t.Fatal("expected RequestEntropyEffect.MinRandomBytes after TransportConfigEvent")
	}

	h.step(&corev1.Event{
		Event: &corev1.Event_EntropyProvided{EntropyProvided: &corev1.EntropyProvided{RandomBytes: testSyncRandomBytes(transport.CoordEntropyRefillBytes)}},
	})

	var signalsToDaemon [][]byte
	var daemonToClient [][]byte
	var signQueue []*corev1.DirectKeySignEffect
	var agreeQueue []*corev1.DirectKeyAgreeEffect

	type pcRec struct {
		sdp       []byte
		remote    []byte
		connected bool
	}
	clientPCs := map[uint64]*pcRec{}
	daemonPCs := map[uint64]*pcRec{}
	daemonSDPs := map[uint64][]byte{}
	daemonInst := make([]byte, 16)
	daemonBoot := make([]byte, 32)
	for i := range daemonInst {
		daemonInst[i] = byte(10 + i)
		daemonBoot[i] = byte(40 + i)
	}
	daemonPub := peerwire.Public{ID: []byte(h.daemon.id), Role: 2, Instance: daemonInst, Boot: daemonBoot, Sign: h.daemon.signPub[:], Box: h.daemon.xPub[:]}
	clientMember := peerwire.Public{ID: []byte(h.client.id), Role: 1, Sign: h.client.signPub[:], Box: h.client.xPub[:]}
	var daemonCapSent bool

	var handleDaemonEffs func([]linkcore.Effect)
	handleDaemonEffs = func(effs []linkcore.Effect) {
		for _, e := range effs {
			switch e := e.(type) {
			case linkcore.SignFrame:
				sig := ed25519.Sign(h.daemon.signPriv, e.Message)
				handleDaemonEffs(h.daemonSec.Handle(linkcore.FrameSigned{Op: e.Op, Signature: sig}, uint64(h.now)))
			case linkcore.TransmitFrame:
				daemonToClient = append(daemonToClient, e.Raw)
			case linkcore.CreatePC:
				daemonPCs[e.ID] = &pcRec{}
				handleDaemonEffs(h.daemonSec.Handle(linkcore.PCCreated{ID: e.ID, OK: true}, uint64(h.now)))
			case linkcore.DescribePC:
				sdp := testDirectSDP(int(e.ID) + 200)
				daemonPCs[e.ID].sdp = sdp
				daemonSDPs[e.ID] = sdp
				handleDaemonEffs(h.daemonSec.Handle(linkcore.PCDescribed{ID: e.ID, SDP: sdp, OK: true}, uint64(h.now)))
				handleDaemonEffs(h.daemonSec.Handle(linkcore.PCLocalCandidate{ID: e.ID, Candidate: []byte("candidate:1 1 udp 2130706431 203.0.113.9 50001 typ srflx"), Valid: true}, uint64(h.now)))
				handleDaemonEffs(h.daemonSec.Handle(linkcore.PCLocalCandidate{ID: e.ID, End: true, Valid: true}, uint64(h.now)))
			case linkcore.SetRemotePC:
				daemonPCs[e.ID].remote = e.SDP
				handleDaemonEffs(h.daemonSec.Handle(linkcore.PCRemoteSet{ID: e.ID, OK: true}, uint64(h.now)))
			case linkcore.AddCandidatesPC:
				handleDaemonEffs(h.daemonSec.Handle(linkcore.PCCandidatesAdded{ID: e.ID, OK: true}, uint64(h.now)))
			case linkcore.HandleControl:
				if e.Frame.Type == 15 {
					handleDaemonEffs(h.daemonSec.Handle(linkcore.ControlHandled{Op: e.Op, Type: 4, Body: directAckPayload(e.Frame)}, uint64(h.now)))
				} else {
					handleDaemonEffs(h.daemonSec.Handle(linkcore.ControlHandled{Op: e.Op, Type: 0}, uint64(h.now)))
				}
			case linkcore.ConfirmPC:
				if !daemonCapSent && e.Use == 1 {
					daemonCapSent = true
					mc := peerwire.PB(peerwire.U(2, 1), peerwire.U(5, 65536), peerwire.U(13, 1))
					cap14 := peerwire.PB(peerwire.B(1, make([]byte, 16)), peerwire.B(2, mc), peerwire.U(3, uint64(h.now+60_000)))
					handleDaemonEffs(h.daemonSec.Handle(linkcore.QueueControl{Type: 14, Body: cap14}, uint64(h.now)))
					cap1 := peerwire.PB(peerwire.B(1, make([]byte, 16)), peerwire.U(2, 15), peerwire.U(4, uint64(h.now+60_000)), peerwire.U(5, 1), peerwire.U(6, 1048576))
					handleDaemonEffs(h.daemonSec.Handle(linkcore.QueueControl{Type: 1, Body: cap1}, uint64(h.now)))
				}
			}
		}
	}

	var drainEffects func([]*corev1.Effect)
	drainEffects = func(effs []*corev1.Effect) {
		for _, eff := range effs {
			switch k := eff.Effect.(type) {
			case *corev1.Effect_DirectKeyAgree:
				agreeQueue = append(agreeQueue, k.DirectKeyAgree)
			case *corev1.Effect_DirectKeySign:
				signQueue = append(signQueue, k.DirectKeySign)
			case *corev1.Effect_DirectSignalSend:
				raw := peerwire.PB(
					peerwire.B(1, []byte(h.client.id)),
					peerwire.B(2, k.DirectSignalSend.ToDevice),
					peerwire.U(3, uint64(k.DirectSignalSend.FrameClass)),
					peerwire.B(4, k.DirectSignalSend.FrameId),
					peerwire.B(5, k.DirectSignalSend.Sealed),
				)
				signalsToDaemon = append(signalsToDaemon, raw)
			case *corev1.Effect_DirectPcCreate:
				c := k.DirectPcCreate
				clientPCs[c.PcId] = &pcRec{}
				if transport.Use(c.Use) == transport.UseRead {
					h.readPCID = c.PcId
				} else if transport.Use(c.Use) == transport.UseBulk {
					h.bulkPCID = c.PcId
				}
				drainEffects(h.step(&corev1.Event{
					Event: &corev1.Event_DirectPcCreated{DirectPcCreated: &corev1.DirectPcCreatedEvent{PcId: c.PcId, Ok: true}},
				}).Effects)
			case *corev1.Effect_DirectPcDescribe:
				d := k.DirectPcDescribe
				sdp := testDirectSDP(int(d.PcId))
				clientPCs[d.PcId].sdp = sdp
				drainEffects(h.step(&corev1.Event{
					Event: &corev1.Event_DirectPcDescription{DirectPcDescription: &corev1.DirectPcDescriptionEvent{PcId: d.PcId, Sdp: sdp, Ok: true}},
				}).Effects)
				drainEffects(h.step(&corev1.Event{
					Event: &corev1.Event_DirectPcLocalCandidate{DirectPcLocalCandidate: &corev1.DirectPcLocalCandidateEvent{PcId: d.PcId, Candidate: "candidate:1 1 udp 2130706431 203.0.113.5 50000 typ srflx"}},
				}).Effects)
				drainEffects(h.step(&corev1.Event{
					Event: &corev1.Event_DirectPcLocalCandidate{DirectPcLocalCandidate: &corev1.DirectPcLocalCandidateEvent{PcId: d.PcId, EndOfCandidates: true}},
				}).Effects)
			case *corev1.Effect_DirectPcSetRemote:
				sr := k.DirectPcSetRemote
				clientPCs[sr.PcId].remote = sr.Sdp
				drainEffects(h.step(&corev1.Event{
					Event: &corev1.Event_DirectPcRemoteSet{DirectPcRemoteSet: &corev1.DirectPcRemoteSetEvent{PcId: sr.PcId, Ok: true}},
				}).Effects)
			case *corev1.Effect_DirectPcAddCandidates:
				drainEffects(h.step(&corev1.Event{
					Event: &corev1.Event_DirectPcCandidatesAdded{DirectPcCandidatesAdded: &corev1.DirectPcCandidatesAddedEvent{PcId: k.DirectPcAddCandidates.PcId, Ok: true}},
				}).Effects)
			case *corev1.Effect_DirectDcReadStats:
				drainEffects(h.step(&corev1.Event{
					Event: &corev1.Event_DirectDcStats{DirectDcStats: &corev1.DirectDcStatsEvent{
						PcId: k.DirectDcReadStats.PcId,
						Report: &corev1.DirectStatsReport{
							DtlsState: "connected",
							DcState:   "open",
							Pairs: []*corev1.DirectStatsPair{
								{LocalType: corev1.DirectCandType_DIRECT_CAND_TYPE_SRFLX, LocalFamily: corev1.DirectCandFamily_DIRECT_CAND_FAMILY_V4, RemoteType: corev1.DirectCandType_DIRECT_CAND_TYPE_SRFLX, RemoteFamily: corev1.DirectCandFamily_DIRECT_CAND_FAMILY_V4, Nominated: true, State: "succeeded", RequestsSent: 3, ResponsesReceived: 3, CurrentRttSeconds: 0.012, HasCurrentRtt: true},
							},
						},
					}},
				}).Effects)
			}
		}
	}

	snapRes := h.step(&corev1.Event{
		Event: &corev1.Event_DirectSecuritySnapshot{
			DirectSecuritySnapshot: &corev1.DirectSecuritySnapshotEvent{
				Member:          true,
				Account:         h.account,
				Domain:          h.domain[:],
				Head:            "head-1",
				HeadHash:        h.headHash[:],
				Epoch:           1,
				StartedAtMs:     h.now,
				LeaseNotAfterMs: h.now + 3_600_000,
				SelfId:          h.client.id,
				SelfEdPublic:    h.client.signPub[:],
				SelfXPublic:     h.client.xPub[:],
				Members: []*corev1.DirectSecurityMember{
					{DeviceId: h.client.id, EdPublic: h.client.signPub[:], XPublic: h.client.xPub[:], IsSelf: true},
					{DeviceId: h.daemon.id, EdPublic: h.daemon.signPub[:], XPublic: h.daemon.xPub[:]},
				},
			},
		},
	})
	drainEffects(snapRes.Effects)

	for step := 0; step < 40 && !h.eng.directReady[h.daemon.id]; step++ {
		for len(agreeQueue) > 0 {
			a := agreeQueue[0]
			agreeQueue = agreeQueue[1:]
			drainEffects(h.step(&corev1.Event{
				Event: &corev1.Event_DirectKeyAgreed{DirectKeyAgreed: &corev1.DirectKeyAgreedEvent{OpId: a.OpId, SharedSecret: h.client.agree(h.daemon.xPub), Ok: true}},
			}).Effects)
		}
		for len(signQueue) > 0 {
			s := signQueue[0]
			signQueue = signQueue[1:]
			sig := ed25519.Sign(h.client.signPriv, s.Message)
			drainEffects(h.step(&corev1.Event{
				Event: &corev1.Event_DirectKeySigned{DirectKeySigned: &corev1.DirectKeySignedEvent{OpId: s.OpId, Signature: sig}},
			}).Effects)
		}
		for len(signalsToDaemon) > 0 {
			raw := signalsToDaemon[0]
			signalsToDaemon = signalsToDaemon[1:]
			if h.daemonSec == nil {
				opened, err := h.codec.Open(raw, clientMember, daemonPub, h.daemon.agree(h.client.xPub), uint64(h.now))
				if err != nil {
					h.t.Fatalf("daemon open initial: %v", err)
				}
				sess, initEffs, err := linkcore.NewSession(linkcore.SessionConfig{
					Self: daemonPub,
					Peer: peerwire.Public{
						ID:       []byte(h.client.id),
						Role:     1,
						Instance: opened.Context.SenderInstance,
						Boot:     opened.Context.SenderBoot,
						Sign:     h.client.signPub[:],
						Box:      h.client.xPub[:],
					},
					Shared:     h.daemon.agree(h.client.xPub),
					Codec:      h.codec,
					Connection: opened.Context.Connection,
					Deadline:   opened.Context.Deadline,
					Entropy:    rand.Reader,
					Allow:      func() bool { return true },
				}, false, uint64(h.now))
				if err != nil {
					h.t.Fatal(err)
				}
				h.daemonSec = sess
				handleDaemonEffs(initEffs)
			}
			handleDaemonEffs(h.daemonSec.Handle(linkcore.FrameReceived{Raw: raw}, uint64(h.now)))
		}
		for len(daemonToClient) > 0 {
			raw := daemonToClient[0]
			daemonToClient = daemonToClient[1:]
			drainEffects(h.step(&corev1.Event{
				Event: &corev1.Event_DirectSignalReceived{DirectSignalReceived: &corev1.DirectSignalReceivedEvent{Raw: raw}},
			}).Effects)
		}
		for pcID, cpc := range clientPCs {
			for dID, dpc := range daemonPCs {
				if !cpc.connected && !dpc.connected && cpc.sdp != nil && string(dpc.remote) == string(cpc.sdp) && dpc.sdp != nil && string(cpc.remote) == string(dpc.sdp) {
					cpc.connected, dpc.connected = true, true
					drainEffects(h.step(&corev1.Event{
						Event: &corev1.Event_DirectPcState{DirectPcState: &corev1.DirectPcStateEvent{PcId: pcID, Kind: corev1.DirectStateKind_DIRECT_STATE_KIND_CONN, Value: "connected"}},
					}).Effects)
					drainEffects(h.step(&corev1.Event{
						Event: &corev1.Event_DirectDcState{DirectDcState: &corev1.DirectDcStateEvent{PcId: pcID, Open: true}},
					}).Effects)
					handleDaemonEffs(h.daemonSec.Handle(linkcore.PCStateChanged{ID: dID, State: linkcore.PCConnected, Valid: true}, uint64(h.now)))
				}
			}
		}
		h.now += 25
		engAt, engOK, sessAt, sessOK, _, _ := h.eng.ensureTransportCoord().NextTimers(h.now)
		if engOK && h.now >= engAt {
			drainEffects(h.step(&corev1.Event{
				Event: &corev1.Event_TimerFired{TimerFired: &corev1.TimerFiredEvent{
					TimerId: h.eng.timers[timerKey{kind: corev1.TimerKind_TIMER_KIND_DIRECT_ENGINE}].id,
					Kind:    corev1.TimerKind_TIMER_KIND_DIRECT_ENGINE,
				}},
			}).Effects)
		}
		if sessOK && h.now >= sessAt {
			drainEffects(h.step(&corev1.Event{
				Event: &corev1.Event_TimerFired{TimerFired: &corev1.TimerFiredEvent{
					TimerId: h.eng.timers[timerKey{kind: corev1.TimerKind_TIMER_KIND_DIRECT_SESSION}].id,
					Kind:    corev1.TimerKind_TIMER_KIND_DIRECT_SESSION,
				}},
			}).Effects)
		}
		if h.daemonSec != nil {
			handleDaemonEffs(h.daemonSec.Handle(linkcore.Tick{}, uint64(h.now)))
		}
	}
	if !h.eng.directReady[h.daemon.id] {
		h.t.Fatalf("expected direct path ready after handshake, status=%+v", h.eng.ensureTransportCoord().Status(h.now, h.daemon.id))
	}
}

func TestSyncDirectLifecycleEventsDualSendAndFallback(t *testing.T) {
	h := newDirectSyncHarness(t)
	const sid = "sess-1"
	h.connectMainAndList(sid)
	h.driveDirectHandshake()

	// 1) Pushed DirectEventsReceivedEvent requires valid confirmed PcId and matching Sender.
	evPayload := &corev1.PlainEvent{
		Seq:      1,
		Verified: true,
		Event:    &v1.Event{Seq: 1},
	}
	h.step(&corev1.Event{
		Event: &corev1.Event_DirectEventsReceived{
			DirectEventsReceived: &corev1.DirectEventsReceivedEvent{
				MachineId: h.daemon.id,
				SessionId: sid,
				PcId:      h.readPCID,
				Sender:    "impostor-device",
				Events:    []*corev1.PlainEvent{evPayload},
			},
		},
	})
	if buf := h.eng.Buffer(sid); buf != nil && buf.PrefixLen() != 0 {
		t.Fatalf("expected unauthorized sender to be rejected, got PrefixLen=%d", buf.PrefixLen())
	}
	h.step(&corev1.Event{
		Event: &corev1.Event_DirectEventsReceived{
			DirectEventsReceived: &corev1.DirectEventsReceivedEvent{
				MachineId: h.daemon.id,
				SessionId: sid,
				PcId:      h.readPCID,
				Sender:    h.daemon.id,
				Events:    []*corev1.PlainEvent{evPayload},
			},
		},
	})
	if buf := h.eng.Buffer(sid); buf == nil || buf.PrefixLen() != 1 {
		t.Fatalf("expected authorized direct push to ingest seq 1, got %v", buf)
	}

	// 2) Interactive command triggers dual-send (DIRECT + RELAY); trusted direct outcome wins immediately.
	res := h.eng.Run(func(ctx *StepContext) {
		_, ok := h.eng.SubmitCommand(ctx, SubmitCommandParams{
			SlotKey:   "slot-send-1",
			MachineID: h.daemon.id,
			SessionID: sid,
			Command:   &v1.Command{Kind: &v1.Command_Send{Send: &v1.SendRequest{SessionId: sid, Text: "hi"}}},
		})
		if !ok {
			t.Fatal("expected SubmitCommand Send to dispatch")
		}
	})
	var directEff, relayEff *corev1.DispatchCommandEffect
	for _, eff := range res.Effects {
		if dc := eff.GetDispatchCommand(); dc != nil {
			if dc.RouteLeg == corev1.CommandRouteLeg_COMMAND_ROUTE_LEG_DIRECT {
				directEff = dc
			} else if dc.RouteLeg == corev1.CommandRouteLeg_COMMAND_ROUTE_LEG_RELAY {
				relayEff = dc
			}
		}
	}
	if directEff == nil || relayEff == nil || directEff.DirectPcId != h.readPCID || directEff.DirectTicketId == 0 {
		t.Fatalf("expected both DIRECT and RELAY legs, got direct=%v relay=%v", directEff, relayEff)
	}
	outRes := h.step(&corev1.Event{
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: &corev1.CommandOutcomeEvent{
				RequestId:         directEff.RequestId,
				AttemptId:         directEff.AttemptId,
				Source:            corev1.OutcomeSource_OUTCOME_SOURCE_DAEMON,
				AttemptBound:      true,
				Signed:            true,
				HashesMatch:       true,
				OuterMatchesInner: true,
				OuterOutcome:      "executed",
				OuterReason:       "executed",
				RouteWasDirect:    true,
				DirectTicketId:    directEff.DirectTicketId,
				ElapsedMs:         14,
				ObservedBytes:     256,
				Result:            &v1.CommandResult{Outcome: "executed", Reason: "executed", ResultAvailable: true},
			},
		},
	})
	if len(outRes.Settled) != 1 || outRes.Settled[0].Category != outcome.Executed {
		t.Fatalf("expected immediate settlement from winning direct leg, got %+v", outRes.Settled)
	}

	// 3) Read command initially routes over DIRECT and falls back immediately to RELAY on transport error.
	readRes := h.eng.Run(func(ctx *StepContext) {
		h.eng.SubmitCommand(ctx, SubmitCommandParams{
			MachineID: h.daemon.id,
			Command:   &v1.Command{Kind: &v1.Command_GetSettings{GetSettings: &v1.GetSettingsRequest{}}},
		})
	})
	var readDC *corev1.DispatchCommandEffect
	for _, eff := range readRes.Effects {
		if dc := eff.GetDispatchCommand(); dc != nil {
			readDC = dc
		}
	}
	if readDC == nil || readDC.RouteLeg != corev1.CommandRouteLeg_COMMAND_ROUTE_LEG_DIRECT {
		t.Fatalf("expected initial read attempt on DIRECT, got %+v", readDC)
	}
	fbRes := h.step(&corev1.Event{
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: &corev1.CommandOutcomeEvent{
				RequestId:      readDC.RequestId,
				AttemptId:      readDC.AttemptId,
				Source:         corev1.OutcomeSource_OUTCOME_SOURCE_TRANSPORT,
				NotForwarded:   true,
				RouteWasDirect: true,
				DirectTicketId: readDC.DirectTicketId,
				TransportCode:  "path_changed",
			},
		},
	})
	var fbDC *corev1.DispatchCommandEffect
	for _, eff := range fbRes.Effects {
		if dc := eff.GetDispatchCommand(); dc != nil {
			fbDC = dc
		}
	}
	if fbDC == nil || fbDC.RouteLeg != corev1.CommandRouteLeg_COMMAND_ROUTE_LEG_RELAY || !fbDC.ForceRelay {
		t.Fatalf("expected immediate RELAY fallback with ForceRelay=true, got %+v", fbDC)
	}

	// 4) Dual-send where direct fails: must wait for relay leg before settling.
	res2 := h.eng.Run(func(ctx *StepContext) {
		h.eng.SubmitCommand(ctx, SubmitCommandParams{
			SlotKey:   "slot-send-2",
			MachineID: h.daemon.id,
			SessionID: sid,
			Command:   &v1.Command{Kind: &v1.Command_Send{Send: &v1.SendRequest{SessionId: sid, Text: "hi2"}}},
		})
	})
	directEff, relayEff = nil, nil
	for _, eff := range res2.Effects {
		if dc := eff.GetDispatchCommand(); dc != nil {
			if dc.RouteLeg == corev1.CommandRouteLeg_COMMAND_ROUTE_LEG_DIRECT {
				directEff = dc
			} else if dc.RouteLeg == corev1.CommandRouteLeg_COMMAND_ROUTE_LEG_RELAY {
				relayEff = dc
			}
		}
	}
	waitRes := h.step(&corev1.Event{
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: &corev1.CommandOutcomeEvent{
				RequestId:       directEff.RequestId,
				AttemptId:       directEff.AttemptId,
				Source:          corev1.OutcomeSource_OUTCOME_SOURCE_TRANSPORT,
				NotForwarded:    true,
				RouteWasDirect:  true,
				DirectTicketId:  directEff.DirectTicketId,
				TransportCode:   "timeout",
				TransportDetail: "dc timeout",
			},
		},
	})
	if len(waitRes.Settled) != 0 {
		t.Fatalf("expected dual-send to wait for relay sibling, got %+v", waitRes.Settled)
	}
	winRes := h.step(&corev1.Event{
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: &corev1.CommandOutcomeEvent{
				RequestId:         relayEff.RequestId,
				AttemptId:         relayEff.AttemptId,
				Source:            corev1.OutcomeSource_OUTCOME_SOURCE_DAEMON,
				AttemptBound:      true,
				Signed:            true,
				HashesMatch:       true,
				OuterMatchesInner: true,
				OuterOutcome:      "executed",
				OuterReason:       "executed",
				Result:            &v1.CommandResult{Outcome: "executed", Reason: "executed", ResultAvailable: true},
			},
		},
	})
	if len(winRes.Settled) != 1 || winRes.Settled[0].Category != outcome.Executed {
		t.Fatalf("expected relay sibling to win after direct failure, got %+v", winRes.Settled)
	}

	// 5) Dual-send where both legs fail and direct handed off: must surface Unknown (prior).
	res3 := h.eng.Run(func(ctx *StepContext) {
		h.eng.SubmitCommand(ctx, SubmitCommandParams{
			SlotKey:   "slot-send-3",
			MachineID: h.daemon.id,
			SessionID: sid,
			Command:   &v1.Command{Kind: &v1.Command_Send{Send: &v1.SendRequest{SessionId: sid, Text: "hi3"}}},
		})
	})
	directEff, relayEff = nil, nil
	for _, eff := range res3.Effects {
		if dc := eff.GetDispatchCommand(); dc != nil {
			if dc.RouteLeg == corev1.CommandRouteLeg_COMMAND_ROUTE_LEG_DIRECT {
				directEff = dc
			} else if dc.RouteLeg == corev1.CommandRouteLeg_COMMAND_ROUTE_LEG_RELAY {
				relayEff = dc
			}
		}
	}
	h.step(&corev1.Event{
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: &corev1.CommandOutcomeEvent{
				RequestId:      directEff.RequestId,
				AttemptId:      directEff.AttemptId,
				Source:         corev1.OutcomeSource_OUTCOME_SOURCE_TRANSPORT,
				NotForwarded:   true,
				HandedOff:      true,
				RouteWasDirect: true,
				DirectTicketId: directEff.DirectTicketId,
				TransportCode:  "timeout",
			},
		},
	})
	bothFail := h.step(&corev1.Event{
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: &corev1.CommandOutcomeEvent{
				RequestId:     relayEff.RequestId,
				AttemptId:     relayEff.AttemptId,
				Source:        corev1.OutcomeSource_OUTCOME_SOURCE_LOCAL,
				NotForwarded:  true,
				TransportCode: "failed_precondition",
			},
		},
	})
	if len(bothFail.Settled) != 1 || bothFail.Settled[0].Category != outcome.Unknown {
		t.Fatalf("expected Unknown when direct handed off before both failed, got %+v", bothFail.Settled)
	}

	// 6) TerminalStreamEvent first close reopens immediately; second close backs off via TIMER_KIND_TERMINAL_REOPEN.
	termClosed1 := h.step(&corev1.Event{
		Event: &corev1.Event_TerminalStream{
			TerminalStream: &corev1.TerminalStreamEvent{
				TerminalId: "term-1",
				Kind:       corev1.TerminalStreamEventKind_TERMINAL_STREAM_EVENT_KIND_CLOSED,
				Opened:     true,
			},
		},
	})
	var immediateReopen bool
	for _, eff := range termClosed1.Effects {
		if tr := eff.GetTerminalReopen(); tr != nil && tr.TerminalId == "term-1" {
			immediateReopen = true
		}
	}
	if !immediateReopen {
		t.Fatal("expected immediate TerminalReopenEffect on first terminal close")
	}
	termClosed := h.step(&corev1.Event{
		Event: &corev1.Event_TerminalStream{
			TerminalStream: &corev1.TerminalStreamEvent{
				TerminalId: "term-1",
				Kind:       corev1.TerminalStreamEventKind_TERMINAL_STREAM_EVENT_KIND_CLOSED,
				Opened:     true,
			},
		},
	})
	var termTimerID uint64
	for _, eff := range termClosed.Effects {
		if st := eff.GetScheduleTimer(); st != nil && st.Kind == corev1.TimerKind_TIMER_KIND_TERMINAL_REOPEN {
			termTimerID = st.TimerId
			h.now += st.DelayMs
		}
	}
	if termTimerID == 0 {
		t.Fatal("expected TIMER_KIND_TERMINAL_REOPEN scheduled after repeated terminal close")
	}
	termFired := h.step(&corev1.Event{
		Event: &corev1.Event_TimerFired{
			TimerFired: &corev1.TimerFiredEvent{
				TimerId: termTimerID,
				Kind:    corev1.TimerKind_TIMER_KIND_TERMINAL_REOPEN,
			},
		},
	})
	var reopened bool
	for _, eff := range termFired.Effects {
		if tr := eff.GetTerminalReopen(); tr != nil && tr.TerminalId == "term-1" {
			reopened = true
		}
	}
	if !reopened {
		t.Fatal("expected TerminalReopenEffect after timer fired")
	}
}
