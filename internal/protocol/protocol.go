// Package protocol holds the peer protocol version spoken between the daemon,
// its local commands and the clients. The relay protocol version spoken with
// remote-agent-server lives in internal/relayproto.
package protocol

import (
	"context"
	"fmt"
	"strconv"

	"connectrpc.com/connect"
)

// PeerVersion is the end-to-end peer protocol version (MachineMeta, Command,
// peerwire Context, and the daemon's local Unix socket). Peers of different
// versions do not work together: each refuses the other and says to update,
// instead of falling back feature by feature. Raise it with any change an older
// peer would misread, in step with clientcore, web/src, android/core and
// ios/RemoteAgentCore. Stored content uses devicesec.Version; pairing uses
// e2e.Version; relay entry points use internal/relayproto.Version.
const PeerVersion = 18

// GenerationConflictError rejects completion of a replaced upload before commit.
// Its diagnostic remains owned by the upload handler.
type GenerationConflictError struct{ Message string }

func (e *GenerationConflictError) Error() string { return e.Message }

// RevisionConflictError is a settled schedule compare-and-swap failure.
// It is local backend evidence, not a new wire type.
type RevisionConflictError struct{ Message string }

func (e *RevisionConflictError) Error() string { return e.Message }

// Header carries PeerVersion on the daemon's local socket, in both directions.
const Header = "Remote-Agent-Protocol"

// Mismatch is the error for a peer that speaks version got.
func Mismatch(peer string, got uint32) error {
	return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
		"%s speaks protocol version %d and this remote-agent version %d; update both to the same release", peer, got, PeerVersion))
}

func parse(v string) uint32 {
	n, _ := strconv.ParseUint(v, 10, 32)
	return uint32(n)
}

// Local is the interceptor for both ends of the daemon's local socket: a
// command and the daemon it reaches must be of the same version, as when
// the daemon was not restarted after an update.
func Local() connect.Interceptor { return local{} }

type local struct{}

func (local) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if !req.Spec().IsClient {
			if got := parse(req.Header().Get(Header)); got != PeerVersion {
				return nil, Mismatch("the command", got)
			}
			res, err := next(ctx, req)
			if err == nil {
				res.Header().Set(Header, strconv.Itoa(PeerVersion))
			}
			return res, err
		}
		req.Header().Set(Header, strconv.Itoa(PeerVersion))
		res, err := next(ctx, req)
		if err != nil {
			return nil, err
		}
		if got := parse(res.Header().Get(Header)); got != PeerVersion {
			return nil, Mismatch("the running daemon", got)
		}
		return res, nil
	}
}

func (local) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		conn.RequestHeader().Set(Header, strconv.Itoa(PeerVersion))
		return conn
	}
}

func (local) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if got := parse(conn.RequestHeader().Get(Header)); got != PeerVersion {
			return Mismatch("the command", got)
		}
		return next(ctx, conn)
	}
}
