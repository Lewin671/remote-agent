package relay

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

// A retry canceled while it still waited for its turn among the machine's
// calls fails with the context's own error, not a Connect one: the
// notification is still owed.
func TestNotificationCanceledBeforeItWasSentIsStillOwed(t *testing.T) {
	for _, err := range []error{
		context.Canceled, context.DeadlineExceeded,
		fmt.Errorf("waiting for a slot: %w", context.Canceled),
		connect.NewError(connect.CodeCanceled, context.Canceled),
		connect.NewError(connect.CodeResourceExhausted, errors.New("busy")),
	} {
		if !notifyOwed(err) {
			t.Errorf("%v: taken for a refusal", err)
		}
	}
	for _, err := range []error{
		connect.NewError(connect.CodeInvalidArgument, errors.New("no")),
		connect.NewError(connect.CodePermissionDenied, errors.New("no")),
	} {
		if notifyOwed(err) {
			t.Errorf("%v: still owed", err)
		}
	}
}

func TestOwedNotifyCopiesBoundedTail(t *testing.T) {
	r := &Relay{owedNotify: map[string][]*pb.Event{}}
	original := make([]*pb.Event, maxOwedNotify+3)
	for i := range original {
		original[i] = &pb.Event{Seq: uint64(i + 1)}
	}
	r.oweNotify("session", original)
	// Replacing entries in the caller's backing array must not alter the
	// retained tail, which is what keeps the 16-event memory cap real.
	original[3], original[len(original)-1] = nil, nil
	got := r.takeOwedNotify("session")
	if len(got) != maxOwedNotify || got[0].Seq != 4 || got[len(got)-1].Seq != uint64(maxOwedNotify+3) {
		t.Fatalf("owed notification tail = %v", got)
	}
}
