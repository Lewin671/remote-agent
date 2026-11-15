package relay

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/transport"
)

func TestForwardFloodIsBoundedAndCancelable(t *testing.T) {
	e := testEndpoint(t, New(nil))
	client := endpointClient(t, e)
	env := commandEnvelope(t, client, e.acct.DeviceID, "reader-flood-request", &pb.Command{Kind: &pb.Command_GetSettings{GetSettings: &pb.GetSettingsRequest{}}})
	a := e.admissionOwner()
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var starts atomic.Int32
	a.authenticate = func(w deviceauth.Wire) (*deviceauth.Authenticated, error) {
		if starts.Add(1) <= 2 {
			entered <- struct{}{}
		}
		<-release
		return nil, deviceauth.ErrInvalid
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
