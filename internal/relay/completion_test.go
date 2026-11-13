package relay

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/outcome"
)

func TestBackendEvidenceDoesNotFollowDiagnosticCode(t *testing.T) {
	for _, code := range []connect.Code{connect.CodeUnknown, connect.CodeCanceled, connect.CodeDeadlineExceeded, connect.CodeAborted, connect.CodeInternal, connect.CodeFailedPrecondition} {
		for _, final := range []bool{false, true} {
			err := error(connect.NewError(code, errors.New("test diagnostic")))
			if final {
				err = outcome.FinalError(err)
			}
			c := &conn{r: New(interruptedScheduleBackend{cause: err})}
			_, got := c.executeEvidence(context.Background(), "", &pb.Command{Kind: &pb.Command_Schedule{Schedule: &pb.ScheduleRequest{Operation: "pause"}}})
			if got != final {
				t.Errorf("code=%s final=%v got=%v", code, final, got)
			}
		}
	}
}
