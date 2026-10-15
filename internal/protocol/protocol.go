// Package protocol holds the version of the protocol the daemon, its local
// commands and the clients speak to one another.
package protocol

import (
	"context"
	"fmt"
	"strconv"

	"connectrpc.com/connect"
)

// Version is the one protocol version. Peers of different versions do not
// work together: each refuses the other and says to update, instead of
// falling back feature by feature. Raise it with any change an older peer
// would misread, in step with PROTOCOL_VERSION in web/src and android/core.
// Data already stored keeps its envelope version (e2e.Version).
const Version = 5

// Header carries Version on the daemon's local socket, in both directions.
const Header = "Remote-Agent-Protocol"

// Mismatch is the error for a peer that speaks version got.
func Mismatch(peer string, got uint32) error {
	return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
		"%s speaks protocol version %d and this remote-agent version %d; update both to the same release", peer, got, Version))
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
			if got := parse(req.Header().Get(Header)); got != Version {
				return nil, Mismatch("the command", got)
			}
			res, err := next(ctx, req)
			if err == nil {
				res.Header().Set(Header, strconv.Itoa(Version))
			}
			return res, err
		}
		req.Header().Set(Header, strconv.Itoa(Version))
		res, err := next(ctx, req)
		if err != nil {
			return nil, err
		}
		if got := parse(res.Header().Get(Header)); got != Version {
			return nil, Mismatch("the running daemon", got)
		}
		return res, nil
	}
}

func (local) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		conn.RequestHeader().Set(Header, strconv.Itoa(Version))
		return conn
	}
}

func (local) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		if got := parse(conn.RequestHeader().Get(Header)); got != Version {
			return Mismatch("the command", got)
		}
		return next(ctx, conn)
	}
}
