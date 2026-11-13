package relay

import (
	"connectrpc.com/connect"
	"context"
	"errors"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/protocol"
	"google.golang.org/protobuf/proto"
	"testing"
	"time"
)

type diskBackend struct {
	publishBackend
	local  *diskLocal
	alerts []*pb.DiskStatus
	acked  []int64
}

func (b *diskBackend) Local() remoteagentv1connect.LocalServiceHandler { return b.local }
func (b *diskBackend) MachineDiskAlerts() []*pb.DiskStatus {
	return append([]*pb.DiskStatus(nil), b.alerts...)
}
func (b *diskBackend) AckMachineDiskAlert(at int64, p pb.ResourcePressure) {
	b.acked = append(b.acked, at)
	b.alerts = b.alerts[1:]
}

type diskLocal struct {
	remoteagentv1connect.UnimplementedLocalServiceHandler
	calls   int
	failure error
}

func (h *diskLocal) GetMachineStatus(context.Context, *connect.Request[pb.GetMachineStatusRequest]) (*connect.Response[pb.MachineStatus], error) {
	h.calls++
	return connect.NewResponse(&pb.MachineStatus{Cores: 8}), nil
}
func (h *diskLocal) UpdateSettings(context.Context, *connect.Request[pb.UpdateSettingsRequest]) (*connect.Response[pb.UpdateSettingsResponse], error) {
	return nil, h.failure
}

func (h *diskLocal) Schedule(context.Context, *connect.Request[pb.ScheduleRequest]) (*connect.Response[pb.ScheduleResponse], error) {
	return nil, h.failure
}

type diskMachine struct {
	remoteagentv1connect.MachineServiceClient
	requests []*pb.NotifyRequest
	failOn   int
}

func (m *diskMachine) Notify(_ context.Context, r *connect.Request[pb.NotifyRequest]) (*connect.Response[pb.NotifyResponse], error) {
	m.requests = append(m.requests, proto.Clone(r.Msg).(*pb.NotifyRequest))
	if len(m.requests) == m.failOn {
		return nil, errors.New("fixture transport failure")
	}
	return connect.NewResponse(&pb.NotifyResponse{}), nil
}
func TestSyncDiskAlertsAcknowledgesOnlyDeliveredAndRetriesInOrder(t *testing.T) {
	for _, fail := range []int{1, 2} {
		t.Run(string(rune('0'+fail)), func(t *testing.T) {
			b := &diskBackend{alerts: []*pb.DiskStatus{{ObservedMs: 1700000000001, AvailableBytes: 9e9, Pressure: pb.ResourcePressure_RESOURCE_PRESSURE_WARNING}, {ObservedMs: 1700000000002, AvailableBytes: 1e9, Pressure: pb.ResourcePressure_RESOURCE_PRESSURE_CRITICAL}}}
			m := &diskMachine{failOn: fail}
			now := time.Unix(1000, 0)
			c := &conn{r: New(b), acct: &Account{}, machine: m, diskAlertNow: func() time.Time { return now }}
			initializeTestSecurityKeys(t, c)
			if e := c.syncDiskAlerts(context.Background()); e == nil {
				t.Fatal("expected failure")
			} else {
				t.Log("initial failure", e)
			}
			if len(b.acked) != fail-1 || len(b.alerts) != 3-fail {
				t.Fatal("unconfirmed notification removed", len(b.acked), len(b.alerts), len(m.requests))
			}
			_ = c.syncDiskAlerts(context.Background())
			now = now.Add(59 * time.Second)
			_ = c.syncDiskAlerts(context.Background())
			if len(m.requests) != fail {
				t.Fatal("retry backoff ignored", len(m.requests), fail)
			}
			now = now.Add(time.Second)
			if e := c.syncDiskAlerts(context.Background()); e != nil {
				t.Fatal(e)
			}
			if len(b.alerts) != 0 || len(b.acked) != 2 || b.acked[0] != 1700000000001 || b.acked[1] != 1700000000002 {
				t.Fatal(b.acked)
			}
			for _, r := range m.requests {
				if r.SessionId != "" || r.Kind != pb.NotifyKind_NOTIFY_KIND_MACHINE_DISK {
					t.Fatal("not machine scoped")
				}
				wire, e := deviceauth.FromEnvelope(r.Content)
				if e != nil {
					t.Fatal(e)
				}
				h, plain, e := c.security.endpoint.Open(wire, nil, false, uint64(time.Now().UnixMilli()))
				if e != nil {
					t.Fatal(e)
				}
				plain, e = deviceauth.Unpack(h, plain)
				if e != nil {
					t.Fatal(e)
				}
				var content pb.NotifyContent
				if e = proto.Unmarshal(plain, &content); e != nil {
					t.Fatal(e)
				}
				if content.Title != "" || content.Text != "" || content.MachineName != "publish-test" || content.DiskStatus == nil {
					t.Fatal("notification must carry data only")
				}
			}
		})
	}
}
func TestMachineStatusScopeAndMachineErrorMapping(t *testing.T) {
	h := &diskLocal{}
	c := &conn{r: New(&diskBackend{local: h})}
	cmd := &pb.Command{Kind: &pb.Command_GetMachineStatus{GetMachineStatus: &pb.GetMachineStatusRequest{}}}
	if r := c.execute(context.Background(), "wrong-session", cmd); r.ErrorCode != "invalid_argument" || h.calls != 0 {
		t.Fatal(r)
	}
	if r := c.execute(context.Background(), "", cmd); r.GetMachineStatus().GetCores() != 8 || h.calls != 1 {
		t.Fatal(r)
	}
	for _, aborted := range []bool{false, true} {
		h.failure = protocol.NewMachineError(protocol.DiskFull, "fixture")
		if aborted {
			e := connect.NewError(connect.CodeAborted, errors.New("fixture"))
			d, _ := connect.NewErrorDetail(&pb.MachineError{Code: protocol.DiskFull})
			e.AddDetail(d)
			h.failure = e
		}
		r := c.execute(context.Background(), "", &pb.Command{Kind: &pb.Command_UpdateSettings{UpdateSettings: &pb.UpdateSettingsRequest{}}})
		want := "disk_full"
		if aborted {
			want = "aborted"
		}
		if r.ErrorCode != want {
			t.Fatal(r)
		}
	}
}

func TestScheduleAcceptedStorageErrorIsNotARevisionConflict(t *testing.T) {
	h := &diskLocal{}
	e := connect.NewError(connect.CodeAborted, errors.New("fixture"))
	detail, _ := connect.NewErrorDetail(&pb.MachineError{Code: protocol.DiskFull})
	e.AddDetail(detail)
	h.failure = e
	c := &conn{r: New(&diskBackend{local: h})}
	r := c.execute(context.Background(), "", &pb.Command{Kind: &pb.Command_Schedule{Schedule: &pb.ScheduleRequest{Operation: "run"}}})
	if r.ErrorCode != "aborted" {
		t.Fatal(r)
	}
}
