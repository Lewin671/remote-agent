package relay

import (
	"context"
	"fmt"
	"strings"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/transport"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"
)

// endpoint is the shared authenticated command/terminal entry point. It has
// no socket, login token, or path cancellation. Publication owns session keys;
// the link owns admitted executions and streams across relay generations.
type endpoint struct {
	r          *Relay
	acct       *Account
	security   *securitySession
	lifetime   context.Context
	link       *transport.Link
	sessionKey func(string, bool) ([]byte, error)
}

// openRelayStream is path setup only. The resulting path carries sealed
// terminal envelopes; it neither opens a native terminal nor owns its life.
func openRelayStream(ctx context.Context, server, token string, route transport.StreamRoute) (transport.Path, error) {
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server, "http")+"/v1/term", &websocket.DialOptions{HTTPClient: httpClient()})
	if err != nil {
		return nil, fmt.Errorf("join the terminal stream: %w", err)
	}
	ws.SetReadLimit(1 << 20)
	path := transport.NewRelayPath(ws)
	hello, _ := proto.Marshal(&pb.TermHello{Token: token, StreamId: route.ID})
	if err := path.Write(ctx, hello); err != nil {
		path.Close()
		return nil, err
	}
	return path, nil
}
