// Package transport owns device links independently of individual connections.
// Paths carry opaque, already sealed frames. Authentication and business
// dispatch remain in the shared endpoint above the link.
package transport

import (
	"context"
	"errors"

	"github.com/coder/websocket"
)

// Path is one physical leg. Ending it only ends that leg's I/O.
type Path interface {
	Read(context.Context) ([]byte, error)
	Write(context.Context, []byte) error
	Close()
	Finish()
}

// RelayPath adapts both the main and the independent terminal WebSockets.
var ErrInvalidFrame = errors.New("malformed frame from server")

type RelayPath struct{ socket *websocket.Conn }

func NewRelayPath(socket *websocket.Conn) *RelayPath { return &RelayPath{socket: socket} }
func (p *RelayPath) Read(ctx context.Context) ([]byte, error) {
	kind, frame, err := p.socket.Read(ctx)
	if err == nil && kind != websocket.MessageBinary {
		err = ErrInvalidFrame
	}
	return frame, err
}
func (p *RelayPath) Write(ctx context.Context, frame []byte) error {
	return p.socket.Write(ctx, websocket.MessageBinary, frame)
}
func (p *RelayPath) Close() { p.socket.CloseNow() }

func (p *RelayPath) Finish() { _ = p.socket.Close(websocket.StatusNormalClosure, "") }
