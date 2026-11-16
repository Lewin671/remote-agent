package relay_test

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/protocol"
	"github.com/Lewin671/remote-agent/internal/relay"
	"github.com/Lewin671/remote-agent/internal/transport/direct"
	"github.com/Lewin671/remote-agent/internal/transport/peerwire"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"
)

type directBackend struct {
	*fakeBackend
	file []byte
}
type directLocal struct {
	remoteagentv1connect.LocalServiceHandler
	b *directBackend
}

func (b *directBackend) Local() remoteagentv1connect.LocalServiceHandler {
	return directLocal{b.fakeBackend.Local(), b}
}
func (l directLocal) GetSettings(context.Context, *connect.Request[pb.GetSettingsRequest]) (*connect.Response[pb.GetSettingsResponse], error) {
	return connect.NewResponse(&pb.GetSettingsResponse{}), nil
}
func (l directLocal) GetSharedFileChunk(_ context.Context, request *connect.Request[pb.GetSharedFileChunkRequest]) (*connect.Response[pb.GetSharedFileChunkResponse], error) {
	offset := int(request.Msg.Offset)
	data := make([]byte, 768<<10)
	copy(data, l.b.file[offset:min(offset+len(data), len(l.b.file))])
	total := uint64(len(l.b.file))
	return connect.NewResponse(&pb.GetSharedFileChunkResponse{Data: data, TotalBytes: &total}), nil
}
func TestDirectCommandAndLargeReadThroughLocalServer(t *testing.T) {
	e := setup(t)
	const session = "direct-session-fixture"
	e.backend.add(t, session)
	file := make([]byte, 17<<20)
	for i := range file {
		file[i] = byte(i*31 + 17)
	}
	backend := &directBackend{e.backend, file}
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
	if _, raw, err := ws.Read(ctx); err != nil {
		t.Fatal(err)
	} else {
		f := &pb.ServerFrame{}
		if proto.Unmarshal(raw, f) != nil || f.GetReady() == nil {
			t.Fatal("expected Ready")
		}
	}
	authority := e.authority
	head := authority.States[len(authority.States)-1]
	hash, _ := head.Hash()
	codec, err := peerwire.NewCodec(authority.Pin.Account, authority.Pin.Domain, hash, head.Core.Epoch)
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
	self := peerwire.Identity{ID: []byte(authority.Member.ID), Role: 1, Sign: authority.Ed, Box: box, Instance: make([]byte, 16), Boot: make([]byte, 32)}
	_, _ = rand.Read(self.Instance)
	_, _ = rand.Read(self.Boot)
	peer := peerwire.Public{ID: []byte(member.ID), Role: 2, Sign: member.EdPublic[:], Box: peerBox}
	connection := make([]byte, 16)
	_, _ = rand.Read(connection)
	out := make(chan direct.Outbound, 128)
	var workers sync.WaitGroup
	ready := make(chan struct {
		use  uint64
		path *direct.Path
	}, 2)
	link, err := direct.NewSession(ctx, direct.SessionConfig{Self: self, Peer: peer, Codec: codec, Connection: connection, Deadline: uint64(time.Now().Add(10 * time.Second).UnixMilli()), Pool: direct.NewPool(4), Allow: func() bool { return true }, LoopbackOnly: true, Send: func(frame direct.Outbound) bool {
		select {
		case out <- frame:
			return true
		default:
			return false
		}
	}, Ready: func(use uint64, path *direct.Path) {
		ready <- struct {
			use  uint64
			path *direct.Path
		}{use, path}
	}}, true)
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
					link.Receive(fields[11].B)
				}
			}
			if r := f.GetPeerFrameResult(); r != nil {
				link.Delivery(r.FrameId, uint32(r.Status), uint64(r.RetryAfterMs), func() bool { return ctx.Err() == nil })
			}
		}
	})
	var paths [2]*direct.Path
	for range 2 {
		select {
		case p := <-ready:
			paths[p.use-1] = p.path
		case <-ctx.Done():
			t.Fatal("authenticated direct negotiation through server timed out")
		}
	}
	endpoint, err := deviceauth.InitEndpoint("", authority.Pin, authority.States, authority.Proofs, authority.Member.ID, authority.Ed.Seed(), authority.X, authority.Lease)
	if err != nil {
		t.Fatal(err)
	}
	defer endpoint.Close()
	call := func(use int, session string, command *pb.Command) *pb.CommandResult {
		t.Helper()
		command.ProtocolVersion = protocol.Version
		plain, _ := proto.Marshal(command)
		var nonce [16]byte
		_, _ = rand.Read(nonce[:])
		id := hex.EncodeToString(nonce[:])
		now := uint64(time.Now().UnixMilli())
		h := deviceauth.Header{Kind: 5, Purpose: deviceauth.Command, Machine: e.acct.DeviceID, Session: session, Request: id, Deadline: now + 15000}
		sealed, err := endpoint.Prepare(h, nil, plain)
		if err != nil {
			t.Fatal(err)
		}
		env, err := sealed.Envelope()
		if err != nil {
			t.Fatal(err)
		}
		packet, _ := proto.Marshal(env)
		if err = paths[use].Write(ctx, packet); err != nil {
			t.Fatal(err)
		}
		packet, err = paths[use].Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		response := &pb.Envelope{}
		if err = proto.Unmarshal(packet, response); err != nil {
			t.Fatal(err)
		}
		wire, err := deviceauth.FromEnvelope(response)
		if err != nil {
			t.Fatal(err)
		}
		got, body, err := endpoint.Open(wire, nil, true, uint64(time.Now().UnixMilli()))
		if err != nil {
			t.Fatal(err)
		}
		body, err = deviceauth.Unpack(got, body)
		if err != nil {
			t.Fatal(err)
		}
		result := &pb.CommandResult{}
		if err = proto.Unmarshal(body, result); err != nil {
			t.Fatal(err)
		}
		original, _ := deviceauth.ParseHeader(sealed.Header)
		key, payload, err := deviceauth.RequestIdentity(original, plain)
		if err != nil {
			t.Fatal(err)
		}
		if got.Request != id || got.Machine != e.acct.DeviceID || result.Outcome != "executed" || result.ErrorCode != "" || !bytes.Equal(result.RequestKeyHash, key[:]) || !bytes.Equal(result.PayloadHash, payload[:]) {
			t.Fatal("direct result did not preserve authenticated request identity", result.Outcome, result.ErrorCode)
		}
		return result
	}
	if call(0, "", &pb.Command{Kind: &pb.Command_GetSettings{GetSettings: &pb.GetSettingsRequest{}}}).GetGetSettings() == nil {
		t.Fatal("read-only response missing")
	}
	databaseURL := url.URL{Scheme: "file", Path: filepath.Join(os.Getenv("REMOTE_AGENT_HOME"), "security-v4.db"), RawQuery: "mode=ro"}
	db, err := sql.Open("sqlite", databaseURL.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	count := func() int {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM accepted").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count()
	received := sha256.New()
	start := time.Now()
	for offset := 0; offset < len(file); offset += 768 << 10 {
		chunk := call(1, session, &pb.Command{Kind: &pb.Command_GetSharedFileChunk{GetSharedFileChunk: &pb.GetSharedFileChunkRequest{SessionId: session, FileId: "direct-file-fixture", Offset: int64(offset)}}}).GetGetSharedFileChunk()
		if chunk == nil || chunk.GetTotalBytes() != uint64(len(file)) || len(chunk.Data) != 768<<10 {
			t.Fatal("chunk shape")
		}
		received.Write(chunk.Data[:min(len(chunk.Data), len(file)-offset)])
	}
	want := sha256.Sum256(file)
	if !bytes.Equal(received.Sum(nil), want[:]) || count() != before {
		t.Fatal("download changed file bytes or wrote acceptance records")
	}
	t.Logf("direct read-only bytes=%d duration=%s MiB/s=%.2f acceptance_delta=%d", len(file), time.Since(start), float64(len(file))/(1<<20)/time.Since(start).Seconds(), count()-before)
}
