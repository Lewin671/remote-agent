package relay

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"google.golang.org/protobuf/proto"
)

type admissionImageBackend struct {
	Backend
	state   ImageUpload
	results []bool
}

func (b *admissionImageBackend) Image(string, string) ImageUpload { return b.state }
func (b *admissionImageBackend) ImageUploaded(_ string, _ string, ok bool, _ int64) {
	b.results = append(b.results, ok)
	b.state.Done = ok
	b.state.Failed = !ok
}

type admissionImageMachine struct {
	remoteagentv1connect.MachineServiceClient
	code     connect.Code
	failures int
	attempts []time.Time
	cancel   context.CancelFunc
}

func (m *admissionImageMachine) PutImage(context.Context, *connect.Request[pb.PutImageRequest]) (*connect.Response[pb.PutImageResponse], error) {
	m.attempts = append(m.attempts, time.Now())
	if m.cancel != nil {
		m.cancel()
	}
	if len(m.attempts) <= m.failures {
		return nil, connect.NewError(m.code, errors.New("temporarily busy"))
	}
	return connect.NewResponse(&pb.PutImageResponse{ExpiresMs: 123}), nil
}

func imageAdmissionConn(t *testing.T, m *admissionImageMachine) (*conn, *admissionImageBackend) {
	t.Helper()
	payload, err := proto.Marshal(&pb.ImagePayload{Format: pb.ImageFormat_IMAGE_FORMAT_PNG, Data: []byte("fixture")})
	if err != nil {
		t.Fatal(err)
	}
	b := &admissionImageBackend{state: ImageUpload{Payload: payload}}
	c := &conn{r: New(b), machine: m}
	initializeTestSecurity(t, c)
	return c, b
}

func TestImageAdmissionRetriesKeepDisplayCopy(t *testing.T) {
	for _, code := range []connect.Code{connect.CodeResourceExhausted, connect.CodeUnavailable} {
		t.Run(code.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				m := &admissionImageMachine{code: code, failures: 2}
				c, b := imageAdmissionConn(t, m)
				flushed := 0
				if err := c.uploadImage(context.Background(), make([]byte, 32), "session", "image", func() error { flushed++; return nil }); err != nil {
					t.Fatal(err)
				}
				if len(m.attempts) != 3 || m.attempts[1].Sub(m.attempts[0]) != 2*time.Second || m.attempts[2].Sub(m.attempts[1]) != 4*time.Second {
					t.Fatalf("attempts did not back off: %v", m.attempts)
				}
				if !b.state.Done || b.state.Failed || len(b.results) != 1 || !b.results[0] || flushed != 3 {
					t.Fatalf("transient refusal downgraded image: state=%v results=%v flushes=%d", b.state, b.results, flushed)
				}
				event := &pb.Event{Kind: &pb.Event_UserMessage{UserMessage: &pb.UserMessage{Images: []*pb.ImageRef{{ImageId: "image", Sync: pb.ImageSync_IMAGE_SYNC_SYNCED, DisplayWidth: 1}}}}}
				if got := c.withImageStates("session", event); got.GetUserMessage().Images[0].Sync != pb.ImageSync_IMAGE_SYNC_SYNCED {
					t.Fatal("successful retry lost the display copy reference")
				}
			})
		})
	}
}

func TestImageAdmissionRetryBudgetSurvivesPasses(t *testing.T) {
	for _, code := range []connect.Code{connect.CodeResourceExhausted, connect.CodeUnavailable} {
		t.Run(code.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				m := &admissionImageMachine{code: code, failures: 100}
				c, b := imageAdmissionConn(t, m)
				if err := c.uploadImage(context.Background(), make([]byte, 32), "session", "image", func() error { return nil }); connect.CodeOf(err) != code {
					t.Fatalf("first pass lost its retryable error: %v", err)
				}
				if len(m.attempts) != 3 || b.state.Failed || len(b.results) != 0 {
					t.Fatal("first pass exceeded its budget or prematurely downgraded the image")
				}
				// A later sync pass or connection uses the same relay-wide image budget.
				next := &conn{r: c.r, acct: c.acct, security: c.security, machine: m}
				if err := next.uploadImage(context.Background(), make([]byte, 32), "session", "image", func() error { return nil }); err != nil {
					t.Fatal(err)
				}
				if len(m.attempts) != maxImageTries || !b.state.Failed || len(b.results) != 1 || b.results[0] {
					t.Fatalf("cross-pass retry budget: attempts=%d results=%v", len(m.attempts), b.results)
				}
				if err := next.uploadImage(context.Background(), make([]byte, 32), "session", "image", func() error { return nil }); err != nil || len(m.attempts) != maxImageTries {
					t.Fatal("exhausted upload retried again")
				}
			})
		})
	}
}

func TestImageAdmissionRetryCancellationDoesNotDowngrade(t *testing.T) {
	for _, duringRPC := range []bool{false, true} {
		name := "backoff"
		if duringRPC {
			name = "RPC"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				m := &admissionImageMachine{code: connect.CodeUnavailable, failures: 100}
				if duringRPC {
					m.cancel = cancel
				}
				c, b := imageAdmissionConn(t, m)
				done := make(chan error, 1)
				go func() { done <- c.uploadImage(ctx, make([]byte, 32), "session", "image", func() error { return nil }) }()
				synctest.Wait()
				cancel()
				synctest.Wait()
				if err := <-done; !errors.Is(err, context.Canceled) {
					t.Fatalf("canceled retry: %v", err)
				}
				if len(m.attempts) != 1 || len(b.results) != 0 || b.state.Failed {
					t.Fatal("cancellation retried or downgraded the image")
				}
			})
		})
	}
}

func TestImagePermanentRefusalDoesNotRetry(t *testing.T) {
	for _, code := range []connect.Code{connect.CodeInvalidArgument, connect.CodePermissionDenied, connect.CodeFailedPrecondition} {
		t.Run(code.String(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				m := &admissionImageMachine{code: code, failures: 100}
				c, b := imageAdmissionConn(t, m)
				if err := c.uploadImage(context.Background(), make([]byte, 32), "session", "image", func() error { return nil }); err != nil {
					t.Fatal(err)
				}
				if len(m.attempts) != 1 || !b.state.Failed {
					t.Fatal("permanent refusal was retried")
				}
			})
		})
	}
}
