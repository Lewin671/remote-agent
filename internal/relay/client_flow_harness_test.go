package relay_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/protocol"
	"github.com/Lewin671/remote-agent/internal/relay"
)

// The three production client engines connect to this opt-in, loopback-only
// fixture. The daemon's real security and scheduler run behind a bounded fake
// backend; model/tool execution is verified by the separate native CLI pass.
type flowBackend struct {
	*fakeBackend
	flowMu               sync.Mutex
	release              chan struct{}
	entered              chan struct{}
	closed               map[string]bool
	snapshots, approvals map[string]int
}
type flowLocal struct {
	remoteagentv1connect.LocalServiceHandler
	b *flowBackend
}

func (b *flowBackend) Local() remoteagentv1connect.LocalServiceHandler {
	return flowLocal{b.fakeBackend.Local(), b}
}
func (b *flowBackend) hold(ctx context.Context) error {
	b.flowMu.Lock()
	release, entered := b.release, b.entered
	b.flowMu.Unlock()
	if release == nil {
		return nil
	}
	entered <- struct{}{}
	select {
	case <-release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (l flowLocal) ListDirectories(ctx context.Context, _ *connect.Request[pb.ListDirectoriesRequest]) (*connect.Response[pb.ListDirectoriesResponse], error) {
	if err := l.b.hold(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.ListDirectoriesResponse{}), nil
}
func (l flowLocal) PutImageChunk(ctx context.Context, _ *connect.Request[pb.PutImageChunkRequest]) (*connect.Response[pb.PutImageChunkResponse], error) {
	if err := l.b.hold(ctx); err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.PutImageChunkResponse{}), nil
}
func (l flowLocal) ResolveApproval(ctx context.Context, r *connect.Request[pb.ResolveApprovalRequest]) (*connect.Response[pb.ResolveApprovalResponse], error) {
	b := l.b
	b.flowMu.Lock()
	defer b.flowMu.Unlock()
	if r.Msg.ObservedEventSeq == 0 || agent.OriginFrom(ctx).GetObservedEventSeq() != r.Msg.ObservedEventSeq {
		return nil, fmt.Errorf("fixture requires current observation")
	}
	if b.closed[r.Msg.ApprovalId] {
		return nil, fmt.Errorf("fixture approval already resolved")
	}
	b.closed[r.Msg.ApprovalId] = true
	b.approvals[r.Msg.SessionId]++
	return connect.NewResponse(&pb.ResolveApprovalResponse{}), nil
}
func (b *flowBackend) LiveSnapshot(ctx context.Context, sid string) (*pb.LiveSnapshot, error) {
	info, err := b.SessionInfo(sid)
	if err != nil {
		return nil, err
	}
	b.flowMu.Lock()
	defer b.flowMu.Unlock()
	if agent.OriginFrom(ctx).GetKind() == pb.OriginKind_ORIGIN_KIND_CLIENT {
		b.snapshots[sid]++
		info.Title = fmt.Sprintf("owner-read-%d", b.snapshots[sid])
	}
	out := &pb.LiveSnapshot{Session: info}
	for i := 0; i < 120; i++ {
		id := fmt.Sprintf("%s-approval-%03d", sid, i)
		if b.closed[id] {
			continue
		}
		out.Actions = append(out.Actions, &pb.Event{SessionId: sid, Seq: uint64(i + 1), Kind: &pb.Event_ApprovalRequested{ApprovalRequested: &pb.ApprovalRequested{ApprovalId: id, Command: "true", ToolName: "fixture"}}})
	}
	return out, nil
}

func TestApprovalClientHarness(t *testing.T) {
	root := os.Getenv("RA_1B_FLOW_ROOT")
	if root == "" {
		t.Skip("set RA_1B_FLOW_ROOT for three-client full-flow measurements")
	}
	abs, err := filepath.Abs(root)
	if err != nil || !strings.HasPrefix(abs, "/tmp/") {
		t.Fatal("isolated /tmp root required")
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	e := setup(t)
	e.backend.machineMeta = func() *pb.MachineMeta {
		return &pb.MachineMeta{Hostname: "fixture-machine", ProtocolVersion: protocol.Version}
	}
	backend := &flowBackend{fakeBackend: e.backend, closed: map[string]bool{}, snapshots: map[string]int{}, approvals: map[string]int{}}
	for _, name := range []string{"web", "android", "ios"} {
		sid := "flow-" + name
		log := e.backend.add(t, sid)
		events := make([]*pb.Event, 120)
		for i := range events {
			events[i] = &pb.Event{Kind: &pb.Event_ApprovalRequested{ApprovalRequested: &pb.ApprovalRequested{ApprovalId: fmt.Sprintf("%s-approval-%03d", sid, i), Command: "true", ToolName: "fixture"}}}
		}
		if err := log.Append("fixture", events, "", 0); err != nil {
			t.Fatal(err)
		}
	}
	r := e.startRelayBackend(backend)
	e.online(t, true)
	var loadMu sync.Mutex
	var load sync.WaitGroup
	stopLoad := func() {
		backend.flowMu.Lock()
		release := backend.release
		backend.release = nil
		backend.flowMu.Unlock()
		if release != nil {
			close(release)
		}
		load.Wait()
	}
	defer stopLoad()
	finished := make(chan struct{})
	var finishOnce sync.Once
	tokenBytes := make([]byte, 24)
	if _, err = rand.Read(tokenBytes); err != nil {
		t.Fatal(err)
	}
	token := hex.EncodeToString(tokenBytes)
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("X-1b-Measure") != token {
			w.WriteHeader(401)
			return
		}
		loadMu.Lock()
		defer loadMu.Unlock()
		var q struct{ Action, Client string }
		if json.NewDecoder(req.Body).Decode(&q) != nil {
			w.WriteHeader(400)
			return
		}
		sid := "flow-" + q.Client
		switch q.Action {
		case "reset":
			stopLoad()
			backend.flowMu.Lock()
			for id := range backend.closed {
				if strings.HasPrefix(id, sid+"-") {
					delete(backend.closed, id)
				}
			}
			backend.snapshots[sid] = 0
			backend.approvals[sid] = 0
			backend.flowMu.Unlock()
			json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		case "start":
			stopLoad()
			backend.flowMu.Lock()
			backend.release = make(chan struct{})
			backend.entered = make(chan struct{}, 6)
			entered := backend.entered
			backend.snapshots[sid] = 0
			backend.approvals[sid] = 0
			backend.flowMu.Unlock()
			for i := range 6 {
				load.Go(func() {
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					var cmd *pb.Command
					if i < 4 {
						cmd = &pb.Command{Kind: &pb.Command_ListDirectories{ListDirectories: &pb.ListDirectoriesRequest{Path: "/fixture"}}}
					} else {
						cmd = &pb.Command{Kind: &pb.Command_PutImageChunk{PutImageChunk: &pb.PutImageChunkRequest{TransferId: "fixture", Data: []byte("load")}}}
					}
					_, _ = e.client.DoMachine(ctx, e.acct.DeviceID, cmd)
				})
			}
			ctx, cancel := context.WithTimeout(req.Context(), 3*time.Second)
			defer cancel()
			for range 6 {
				select {
				case <-entered:
				case <-ctx.Done():
					http.Error(w, "fixture load did not start", 500)
					return
				}
			}
			if relay.ActiveCallsForTest(r) != 6 {
				http.Error(w, "fixture pools not full", 500)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"normal": 4, "bulk": 2})
		case "stop":
			backend.flowMu.Lock()
			snapshots, approvals := backend.snapshots[sid], backend.approvals[sid]
			backend.flowMu.Unlock()
			active := relay.ActivePoolsForTest(r)
			stopLoad()
			json.NewEncoder(w).Encode(map[string]any{"snapshot_calls": snapshots, "approval_calls": approvals, "active_before_release": active, "normal": active[1], "bulk": active[2]})
		case "finish":
			stopLoad()
			json.NewEncoder(w).Encode(map[string]bool{"ok": true})
			finishOnce.Do(func() { close(finished) })
		default:
			w.WriteHeader(400)
		}
	}))
	defer control.Close()
	a := e.authority
	states, proofs := [][]byte{}, []any{}
	for i, s := range a.States {
		b, err := s.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		states = append(states, b)
		var proof any
		if a.Proofs[i] != nil {
			proof, err = a.Proofs[i].MarshalBinary()
			if err != nil {
				t.Fatal(err)
			}
		}
		proofs = append(proofs, proof)
	}
	lease, err := a.Lease.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	record := map[string]any{"version": 3, "account": a.Pin.Account, "domain": a.Pin.Domain[:], "genesis": a.Pin.Genesis[:], "member": a.Member.ID, "x": a.X, "states": states, "proofs": proofs, "lease": lease}
	bootstrap := map[string]any{"record": record, "server": a.Server, "machine": e.acct.DeviceID, "seed": a.Ed.Seed(), "root": a.Root, "control": control.URL, "controlToken": token}
	data, err := json.Marshal(bootstrap)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "bootstrap.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	fmt.Println("FLOW_HARNESS_READY")
	select {
	case <-finished:
	case <-time.After(30 * time.Minute):
		t.Fatal("flow harness was not stopped")
	}
}
