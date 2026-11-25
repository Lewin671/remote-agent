package server

import (
	"errors"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

func relayError(code connect.Code, err error, reason pb.RelayErrorReason) error {
	return relayErrorDetail(code, err, &pb.RelayErrorDetail{Reason: reason})
}

func relayErrorDetail(code connect.Code, err error, detail *pb.RelayErrorDetail) error {
	ce := connect.NewError(code, err)
	if detail != nil && detail.Reason != pb.RelayErrorReason_RELAY_ERROR_REASON_UNSPECIFIED {
		if d, e := connect.NewErrorDetail(detail); e == nil {
			ce.AddDetail(d)
		}
	}
	return ce
}

func errClockAhead(code connect.Code, msg string, serverTimeMs int64) error {
	return relayErrorDetail(code, errors.New(msg), &pb.RelayErrorDetail{
		Reason:       pb.RelayErrorReason_RELAY_ERROR_REASON_CLOCK_AHEAD,
		ServerTimeMs: serverTimeMs,
	})
}

func errFullReason(what string, reason pb.RelayErrorReason) error {
	return relayError(connect.CodeResourceExhausted, errors.New(what), reason)
}

// withRelayDetail returns a cloned *connect.Error carrying detail without
// mutating err (which may be a shared sentinel error).
func withRelayDetail(err error, detail *pb.RelayErrorDetail) error {
	var ce *connect.Error
	if !errors.As(err, &ce) || detail == nil || detail.Reason == pb.RelayErrorReason_RELAY_ERROR_REASON_UNSPECIFIED {
		return err
	}
	clone := connect.NewError(ce.Code(), errors.New(ce.Message()))
	for key, values := range ce.Meta() {
		clone.Meta()[key] = append([]string(nil), values...)
	}
	for _, existing := range ce.Details() {
		clone.AddDetail(existing)
	}
	if d, e := connect.NewErrorDetail(detail); e == nil {
		clone.AddDetail(d)
	}
	return clone
}
