package server

import (
	"connectrpc.com/connect"
	"errors"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
)

const attemptHeader = "Remote-Agent-Attempt"

func validAttempt(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// Only a sealed refusal or a body-deterministic format rejection may use this
// helper. Being before the enqueue boundary alone does not establish evidence.
func notForwarded(err error, attempt, epoch, reason string) error {
	if !validAttempt(attempt) {
		return err
	}
	var ce *connect.Error
	if !errors.As(err, &ce) {
		ce = connect.NewError(connect.CodeUnavailable, err)
	}
	// Error values can be shared (admission errors are). Never attach per-call
	// details to the original, or another response could inherit this proof.
	copy := connect.NewError(ce.Code(), errors.New(ce.Message()))
	for key, values := range ce.Meta() {
		copy.Meta()[key] = append([]string(nil), values...)
	}
	ce = copy
	detail, e := connect.NewErrorDetail(&pb.NotForwarded{AttemptId: attempt, RelayEpoch: epoch, Reason: reason})
	if e == nil {
		ce.AddDetail(detail)
	}
	return ce
}
func entryRefusal(req connect.AnyRequest, err error) error {
	if req.Spec().Procedure != remoteagentv1connect.ClientServiceForwardProcedure {
		return err
	}
	// No terminal decision exists here. Preserve authentication diagnostics
	// (notably 401 for explicit login refresh), without claiming NotForwarded.
	return err
}
