package relay_test

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"github.com/Lewin671/remote-agent/internal/protocol"
	"github.com/Lewin671/remote-agent/internal/relay"
	"github.com/Lewin671/remote-agent/internal/transport/direct"
	"github.com/Lewin671/remote-agent/shared/peerwire"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"
)

type pushTestBackend struct {
	*directBackend
	holdImage string
}

func (b *pushTestBackend) Image(sessionID, imageID string) relay.ImageUpload {
	if imageID == b.holdImage {
		return relay.ImageUpload{Done: false, Failed: false}
	}
	return b.directBackend.Image(sessionID, imageID)
}

func (b *pushTestBackend) ImageUploaded(sessionID, imageID string, ok bool, expiresMs int64) {
	if imageID == b.holdImage {
		return
	}
	b.directBackend.ImageUploaded(sessionID, imageID, ok, expiresMs)
}

func TestDirectEventPushSubscriptionPrioritySizeImageAndRevocation(t *testing.T) {
	var blockRelay atomic.Bool
	e := setupWithHandler(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if blockRelay.Load() && (strings.HasSuffix(r.URL.Path, "/UploadEvents") || strings.Contains(r.URL.Path, "/machine/stream")) {
				http.Error(w, "relay offline for test", http.StatusServiceUnavailable)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	const session = "direct-push-session"
	const unsubscribed = "other-session"
	log1 := e.backend.add(t, session)
	log2 := e.backend.add(t, unsubscribed)

	// Pre-populate historical event before subscription; direct tailer must skip old history.
	if err := log1.Append(session, []*pb.Event{{Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Text: "history"}}}}, "", 0); err != nil {
		t.Fatal(err)
	}

	// Ensure relay.key exists on disk (as syncSession would create/load) for both sessions.
	key1 := e2e.NewKey()
	if err := os.WriteFile(filepath.Join(e.backend.SessionDir(session), "relay.key"), key1, 0o600); err != nil {
		t.Fatal(err)
	}
	key2 := e2e.NewKey()
	if err := os.WriteFile(filepath.Join(e.backend.SessionDir(unsubscribed), "relay.key"), key2, 0o600); err != nil {
		t.Fatal(err)
	}

	nowFn := time.Now

	backend := &pushTestBackend{
		directBackend: &directBackend{fakeBackend: e.backend},
		holdImage:     "img-pending",
	}
	e.startRelayBackend(backend, relay.SetDirectLoopbackForTest)
	e.online(t, true)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(e.http.URL, "http")+"/v1/stream", &websocket.DialOptions{HTTPClient: http.DefaultClient})
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	ws.SetReadLimit(1 << 20)
	hello, _ := proto.Marshal(&pb.PeerFrame{Kind: &pb.PeerFrame_Hello{Hello: &pb.StreamHello{Token: e.authority.Token}}})
	if err = ws.Write(ctx, websocket.MessageBinary, hello); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ws.Read(ctx); err != nil {
		t.Fatal(err)
	}

	authority := e.authority
	head := authority.States[len(authority.States)-1]
	hash, _ := head.Hash()
	codec, err := peerwire.NewCodec(authority.Pin.Account, authority.Pin.Domain, hash, head.Core.Epoch, protocol.Version)
	if err != nil {
		t.Fatal(err)
	}
	box, err := ecdh.X25519().NewPrivateKey(authority.X)
	if err != nil {
		t.Fatal(err)
	}
	member, _ := head.Core.Member(e.acct.DeviceID)
	peerBox, err := ecdh.X25519().NewPublicKey(member.XPublic[:])
	if err != nil {
		t.Fatal(err)
	}
	self := &direct.Identity{ID: []byte(authority.Member.ID), Role: 1, Sign: authority.Ed, Box: box, Instance: make([]byte, 16), Boot: make([]byte, 32)}
	_, _ = rand.Read(self.Instance)
	_, _ = rand.Read(self.Boot)
	peer := peerwire.Public{ID: []byte(member.ID), Role: 2, Sign: member.EdPublic[:], Box: peerBox.Bytes()}
	connection := make([]byte, 16)
	_, _ = rand.Read(connection)

	out := make(chan direct.Outbound, 128)
	var workers sync.WaitGroup
	ready := make(chan struct {
		use  uint64
		path *direct.Path
	}, 2)
	sawCap15 := make(chan struct{}, 1)
	link, err := direct.NewSession(ctx, direct.SessionConfig{
		Self:         self,
		Peer:         peer,
		Codec:        codec,
		Connection:   connection,
		Deadline:     uint64(nowFn().Add(10 * time.Second).UnixMilli()),
		Pool:         direct.NewPool(4),
		Allow:        func() bool { return true },
		LoopbackOnly: true,
		Now:          nowFn,
		Control: func(o peerwire.Opened, _ uint64) (uint64, []byte) {
			if o.Type == 1 {
				var caps pb.Capabilities
				if proto.Unmarshal(o.Payload, &caps) == nil {
					for _, ct := range caps.ControlTypes {
						if ct == 15 {
							select {
							case sawCap15 <- struct{}{}:
							default:
							}
						}
					}
				}
				return 4, peerwire.PB(peerwire.U(4, o.Context.Class), peerwire.U(5, o.Context.Seq), peerwire.B(6, o.Hash), peerwire.U(7, o.Context.Generation), peerwire.U(8, o.Context.Use))
			}
			return 0, nil
		},
		Send: func(frame direct.Outbound) bool {
			select {
			case out <- frame:
				return true
			default:
				return false
			}
		},
		Ready: func(use uint64, path *direct.Path) {
			ready <- struct {
				use  uint64
				path *direct.Path
			}{use, path}
		},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { cancel(); ws.CloseNow(); link.Close(); link.Wait(); workers.Wait() }()

	workers.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			case frame := <-out:
				if !frame.Valid() {
					continue
				}
				fields, err := peerwire.Parse(frame.Raw, 5)
				if err != nil {
					return
				}
				packet, _ := proto.Marshal(&pb.PeerFrame{Kind: &pb.PeerFrame_DirectedPeerFrame{DirectedPeerFrame: &pb.DirectedPeerFrame{ToDevice: fields[2].B, FrameClass: pb.FrameClass(fields[3].U), FrameId: fields[4].B, Sealed: fields[5].B}}})
				if ws.Write(ctx, websocket.MessageBinary, packet) != nil {
					return
				}
			}
		}
	})
	subAcked := make(chan struct{}, 8)
	var learnedMu sync.Mutex
	learnedPeer := peer
	workers.Go(func() {
		for {
			_, raw, err := ws.Read(ctx)
			if err != nil {
				return
			}
			f := &pb.ServerFrame{}
			if proto.Unmarshal(raw, f) != nil {
				return
			}
			if f.GetPeerFrameDelivery() != nil {
				fields, err := peerwire.Parse(raw, 1)
				if err == nil {
					packet := fields[11].B
					learnedMu.Lock()
					p := learnedPeer
					learnedMu.Unlock()
					if o, err := self.Open(codec, packet, p, uint64(nowFn().UnixMilli())); err == nil {
						if o.Context.Class == 1 && o.Type == 2 {
							learnedMu.Lock()
							learnedPeer.Instance = append([]byte(nil), o.Context.SenderInstance...)
							learnedPeer.Boot = append([]byte(nil), o.Context.SenderBoot...)
							learnedMu.Unlock()
						}
						if o.Context.Class == 2 && o.Type == 4 {
							if f, err := peerwire.ParseMessage(o.Payload, "Ack"); err == nil && f[4].U == 2 {
								select {
								case subAcked <- struct{}{}:
								default:
								}
							}
						}
					}
					link.Receive(packet)
				}
			}
			if r := f.GetPeerFrameResult(); r != nil {
				link.Delivery(r.FrameId, uint32(r.Status), uint64(r.RetryAfterMs), func() bool { return ctx.Err() == nil })
			}
		}
	})

	// Wait until use=1 path is ready (use=2 may finish concurrently).
	var use1 *direct.Path
	for use1 == nil {
		select {
		case p := <-ready:
			if p.use == 1 {
				use1 = p.path
			}
		case <-ctx.Done():
			t.Fatal("direct negotiation timed out")
		}
	}
	select {
	case <-sawCap15:
	case <-time.After(5 * time.Second):
		t.Fatal("expected daemon Capabilities Control 1 to advertise control type 15")
	}

	endpoint, err := deviceauth.InitEndpoint("", authority.Pin, authority.States, authority.Proofs, authority.Member.ID, authority.Ed.Seed(), authority.X, authority.Lease)
	if err != nil {
		t.Fatal(err)
	}
	defer endpoint.Close()

	// Drain any earlier Control 4 Acks (e.g. from handshake), then send Control 15 and wait for its Ack.
	for len(subAcked) > 0 {
		<-subAcked
	}
	subPayload, err := proto.Marshal(&pb.DirectEventSubscription{SessionIds: []string{session}})
	if err != nil {
		t.Fatal(err)
	}
	if !link.SendControl(15, subPayload) {
		t.Fatal("SendControl(15) failed")
	}
	select {
	case <-subAcked:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Control 15 Ack")
	}

	// Verify use=1 is open and Ready(1) has completed on the daemon before appending live events.
	warmCmd := &pb.Command{ProtocolVersion: protocol.Version, Kind: &pb.Command_GetSettings{GetSettings: &pb.GetSettingsRequest{}}}
	warmPlain, _ := proto.Marshal(warmCmd)
	warmHeader := deviceauth.Header{Kind: 5, Purpose: deviceauth.Command, Machine: e.acct.DeviceID, Request: "req-direct-push-warmup-check", Deadline: uint64(nowFn().UnixMilli()) + 15000}
	warmSealed, err := endpoint.Prepare(warmHeader, nil, warmPlain)
	if err != nil {
		t.Fatal(err)
	}
	warmEnv, _ := warmSealed.Envelope()
	warmBytes, _ := proto.Marshal(warmEnv)
	if err := use1.Write(ctx, warmBytes); err != nil {
		t.Fatal(err)
	}

	// Read packets arriving on use=1 into channels for events and results.
	directEvents := make(chan *pb.Event, 32)
	directResults := make(chan *pb.CommandResult, 16)
	workers.Go(func() {
		for {
			raw, err := use1.Read(ctx)
			if err != nil {
				return
			}
			env := &pb.Envelope{}
			if proto.Unmarshal(raw, env) != nil {
				continue
			}
			wire, err := deviceauth.FromEnvelope(env)
			if err != nil {
				continue
			}
			h, err := deviceauth.ParseHeader(wire.Header)
			if err != nil {
				continue
			}
			if h.Purpose == deviceauth.Event {
				got, body, err := endpoint.Open(wire, key1, true, uint64(nowFn().UnixMilli()))
				if err != nil {
					t.Errorf("open direct event: %v", err)
					continue
				}
				body, err = deviceauth.Unpack(got, body)
				if err != nil {
					t.Errorf("unpack direct event: %v", err)
					continue
				}
				var ev pb.Event
				if proto.Unmarshal(body, &ev) != nil {
					continue
				}
				directEvents <- &ev
			} else if h.Purpose == deviceauth.Result {
				got, body, err := endpoint.Open(wire, nil, true, uint64(nowFn().UnixMilli()))
				if err != nil {
					continue
				}
				body, err = deviceauth.Unpack(got, body)
				if err != nil {
					continue
				}
				var res pb.CommandResult
				if proto.Unmarshal(body, &res) == nil {
					directResults <- &res
				}
			}
		}
	})

	select {
	case res := <-directResults:
		if res.GetGetSettings() == nil {
			t.Fatalf("expected warmup GetSettings result, got %+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for warmup GetSettings result")
	}

	// Append an unsubscribed session event first: must NOT be delivered.
	if err := log2.Append(unsubscribed, []*pb.Event{{Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Text: "unsub"}}}}, "", 0); err != nil {
		t.Fatal(err)
	}

	// 1. Live event on subscribed session arrives via direct (even with no UploadEvents!).
	if err := log1.Append(session, []*pb.Event{{Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Text: "hello direct"}}}}, "", 0); err != nil {
		t.Fatal(err)
	}
	var gotEv *pb.Event
	select {
	case gotEv = <-directEvents:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for direct event push")
	}
	if gotEv.SessionId != session || gotEv.GetAgentMessage().GetText() != "hello direct" {
		t.Fatalf("unexpected direct event: %+v", gotEv)
	}

	// 2. Oversize event (> MaxSingleFragmentBytes even after DEFLATE) must skip direct.
	randomText := make([]byte, 24<<10)
	_, _ = rand.Read(randomText)
	const hexChars = "0123456789abcdef"
	var sb strings.Builder
	sb.Grow(len(randomText) * 2)
	for _, b := range randomText {
		sb.WriteByte(hexChars[b>>4])
		sb.WriteByte(hexChars[b&0xf])
	}
	if err := log1.Append(session, []*pb.Event{{Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Text: sb.String()}}}}, "", 0); err != nil {
		t.Fatal(err)
	}
	oversizeSeq := log1.LastSeq()

	// 3. Pending image event (IMAGE_SYNC_SYNCED with !Done && !Failed) must skip direct.
	if err := log1.Append(session, []*pb.Event{{
		Kind: &pb.Event_UserMessage{
			UserMessage: &pb.UserMessage{
				Text:   "see image",
				Images: []*pb.ImageRef{{ImageId: "img-pending", Sync: pb.ImageSync_IMAGE_SYNC_SYNCED}},
			},
		},
	}}, "", 0); err != nil {
		t.Fatal(err)
	}
	pendingImgSeq := log1.LastSeq()

	// 4. Subsequent normal event arrives and proves oversize & pending-image were skipped.
	if err := log1.Append(session, []*pb.Event{{Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Text: "after skipped"}}}}, "", 0); err != nil {
		t.Fatal(err)
	}
	smallSeq := log1.LastSeq()
	for {
		select {
		case ev := <-directEvents:
			if ev.Seq == oversizeSeq || ev.Seq == pendingImgSeq {
				t.Fatalf("oversize (%d) or pending-image (%d) event must not go direct, got seq=%d", oversizeSeq, pendingImgSeq, ev.Seq)
			}
			if ev.SessionId == unsubscribed {
				t.Fatalf("unsubscribed session event must not be pushed")
			}
			if ev.Seq == smallSeq {
				goto gotSmall
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for small direct event after skipped events")
		}
	}
gotSmall:

	// 5. Saturate low-priority direct events (> 32 queue capacity) concurrently with a high-priority CommandResult on use=1.
	burst := make([]*pb.Event, 48)
	for i := range burst {
		burst[i] = &pb.Event{Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Text: "burst"}}}
	}
	if err := log1.Append(session, burst, "", 0); err != nil {
		t.Fatal(err)
	}
	cmd := &pb.Command{ProtocolVersion: protocol.Version, Kind: &pb.Command_GetSettings{GetSettings: &pb.GetSettingsRequest{}}}
	plain, _ := proto.Marshal(cmd)
	h := deviceauth.Header{Kind: 5, Purpose: deviceauth.Command, Machine: e.acct.DeviceID, Request: "req-direct-push-priority-check", Deadline: uint64(nowFn().UnixMilli()) + 15000}
	sealed, err := endpoint.Prepare(h, nil, plain)
	if err != nil {
		t.Fatal(err)
	}
	cmdEnv, _ := sealed.Envelope()
	cmdBytes, _ := proto.Marshal(cmdEnv)
	if err := use1.Write(ctx, cmdBytes); err != nil {
		t.Fatal(err)
	}
	select {
	case res := <-directResults:
		if res.GetGetSettings() == nil {
			t.Fatalf("expected GetSettings result, got %+v", res)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for high-priority CommandResult")
	}

	// 6. Direct push continues working while relay machine stream and UploadEvents are offline.
	blockRelay.Store(true)
	e.dropStreams()
	if err := log1.Append(session, []*pb.Event{{Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Text: "while relay offline"}}}}, "", 0); err != nil {
		t.Fatal(err)
	}
	offlineSeq := log1.LastSeq()
	for {
		select {
		case ev := <-directEvents:
			if ev.Seq == offlineSeq {
				goto done
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for direct push while relay offline")
		}
	}
done:
}
