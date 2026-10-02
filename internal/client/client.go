// Package client connects to the local daemon's unix socket.
package client

import (
	"context"
	"net"
	"net/http"

	"connectrpc.com/connect"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/protocol"
)

// New returns a LocalService client for the daemon listening on socket.
func New(socket string) remoteagentv1connect.LocalServiceClient {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		},
	}
	// No client timeout: event streams and approval waits are long-lived;
	// callers bound them with contexts.
	return remoteagentv1connect.NewLocalServiceClient(&http.Client{Transport: tr}, "http://remote-agent", connect.WithInterceptors(protocol.Local()))
}
