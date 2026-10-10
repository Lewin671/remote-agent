package relay

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
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
