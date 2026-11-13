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

// Only callers before the enqueue boundary may use this helper.
func notForwarded(err error, attempt, reason string) error {
	if !validAttempt(attempt) {
		return err
	}
	var ce *connect.Error
	if !errors.As(err, &ce) {
		ce = connect.NewError(connect.CodeUnavailable, err)
	}
	detail, e := connect.NewErrorDetail(&pb.NotForwarded{AttemptId: attempt, Reason: reason})
	if e == nil {
		ce.AddDetail(detail)
	}
	return ce
}
func entryRefusal(req connect.AnyRequest, err error) error {
	if req.Spec().Procedure != remoteagentv1connect.ClientServiceForwardProcedure {
		return err
	}
	return notForwarded(err, req.Header().Get(attemptHeader), "entry_refused")
}
