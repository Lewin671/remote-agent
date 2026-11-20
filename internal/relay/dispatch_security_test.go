package relay

import (
	"context"
	"errors"
	"fmt"
	"google.golang.org/protobuf/proto"
	"io"
	"sync/atomic"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/transport"
	"github.com/Lewin671/remote-agent/shared/devicesec"
)

func TestForwardFloodIsBoundedAndCancelable(t *testing.T) {
	e := testEndpoint(t, New(nil))
	client := endpointClient(t, e)
	env := commandEnvelope(t, client, e.acct.DeviceID, "reader-flood-request", &pb.Command{Kind: &pb.Command_GetSettings{GetSettings: &pb.GetSettingsRequest{}}})
	a := e.admissionOwner()
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var starts atomic.Int32
	a.authenticate = func(w devicesec.Wire) (*deviceauth.Authenticated, error) {
		if starts.Add(1) <= 2 {
			entered <- struct{}{}
		}
		<-release
		return nil, devicesec.ErrInvalid
	}
	defer func() { close(release); a.close() }()
	for range 2 {
		e.dispatch(context.Background(), &pb.ForwardCall{Command: env}, func(*pb.ForwardReply) {})
		<-entered
	}
	read := 0
	err := readForwardCalls(context.Background(), func() (*pb.ServerFrame, error) {
		if read == 1000 {
			return nil, io.EOF
		}
		read++
		return &pb.ServerFrame{Kind: &pb.ServerFrame_ForwardCall{ForwardCall: &pb.ForwardCall{Command: env}}}, nil
	}, func(f *pb.ForwardCall) { e.dispatch(context.Background(), f, func(*pb.ForwardReply) {}) })
	if !errors.Is(err, io.EOF) || read != 1000 {
		t.Fatal("reader blocked on full authentication queue")
	}
	a.mu.Lock()
	queued, retained := a.frames.Len(), a.frames.UsedBytes()
	a.mu.Unlock()
	if queued != 8 || retained > 1<<20 || starts.Load() != 2 {
		t.Fatalf("auth bounds: queued=%d bytes=%d workers=%d", queued, retained, starts.Load())
	}
}
func TestForwardReadFailureLeavesExecutionWithProcess(t *testing.T) {
	link := transport.NewLink(context.Background())
	defer link.Close()
	defer link.DrainCalls()
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	failure := errors.New("stream ended")
	reads := 0
	err := readForwardCalls(context.Background(), func() (*pb.ServerFrame, error) {
		reads++
		if reads == 1 {
			return &pb.ServerFrame{Kind: &pb.ServerFrame_ForwardCall{ForwardCall: &pb.ForwardCall{CallId: 1}}}, nil
		}
		<-entered
		return nil, failure
	}, func(*pb.ForwardCall) {
		link.Run(func(ctx context.Context) {
			close(entered)
			<-release
			if ctx.Err() != nil {
				t.Error("path failure canceled execution")
			}
		})
	})
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
}

func TestAuthenticationFrameAndByteBudgets(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		cipher int
		peers  bool
		want   int
	}{{"global-count", 1024, true, 64}, {"global-bytes", 128 << 10, true, 0}, {"peer-bytes", 128 << 10, false, 0}} {
		t.Run(scenario.name, func(t *testing.T) {
			e := testEndpoint(t, New(nil))
			client := endpointClient(t, e)
			env := commandEnvelope(t, client, e.acct.DeviceID, "authentication-budget-request", &pb.Command{Kind: &pb.Command_GetSettings{GetSettings: &pb.GetSettingsRequest{}}})
			wire, err := deviceauth.FromEnvelope(env)
			if err != nil {
				t.Fatal(err)
			}
			wire.Cipher = make([]byte, scenario.cipher)
			header, err := devicesec.ParseHeader(wire.Header)
			if err != nil {
				t.Fatal(err)
			}
			a := e.admissionOwner()
			entered := make(chan struct{}, 2)
			release := make(chan struct{})
			var calls atomic.Int32
			a.authenticate = func(devicesec.Wire) (*deviceauth.Authenticated, error) {
				if calls.Add(1) <= 2 {
					entered <- struct{}{}
				}
				<-release
				return nil, devicesec.ErrInvalid
			}
			defer func() { close(release); a.close() }()
			for range 2 {
				e.dispatch(context.Background(), &pb.ForwardCall{Command: env}, func(*pb.ForwardReply) {})
				<-entered
			}
			frameBytes := 0
			for i := range 100 {
				if scenario.peers {
					header.Sender = fmt.Sprintf("%032x", i+1)
				}
				wire.Header, err = header.MarshalBinary()
				if err != nil {
					t.Fatal(err)
				}
				forged, err := deviceauth.Envelope(wire)
				if err != nil {
					t.Fatal(err)
				}
				call := &pb.ForwardCall{Command: forged}
				frameBytes = proto.Size(call)
				e.dispatch(context.Background(), call, func(*pb.ForwardReply) {})
			}
			want := scenario.want
			if want == 0 {
				budget := 4 << 20
				if !scenario.peers {
					budget = 1 << 20
				}
				want = budget / frameBytes
			}
			a.mu.Lock()
			count, used := a.frames.Len(), a.frames.UsedBytes()
			a.mu.Unlock()
			if count != want || used != want*frameBytes {
				t.Fatalf("%s: retained %d/%d bytes, want %d/%d", scenario.name, count, used, want, want*frameBytes)
			}
		})
	}
}
