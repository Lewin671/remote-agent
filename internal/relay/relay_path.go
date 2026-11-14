package relay

import (
	"context"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/transport"
	"google.golang.org/protobuf/proto"
	"log/slog"
	"time"
)

// relaySession owns only this leg's reads and reply waiters. The endpoint and
// link belong to the process dispatcher, not the lifetime of this socket.
type relaySession struct {
	dispatcher   *dispatcher
	pathLifetime context.Context
	path         transport.Path
	activity     func()
}

func (c *relaySession) readLoop(ctx context.Context) error {
	return readForwardCalls(ctx, c.dispatcher.link, func() (*pb.ServerFrame, error) {
		b, err := c.path.Read(ctx)
		if err == nil && c.activity != nil {
			c.activity()
		}
		if err != nil {
			return nil, err
		}
		f := &pb.ServerFrame{}
		if err := proto.Unmarshal(b, f); err != nil {
			return nil, transport.ErrInvalidFrame
		}
		return f, nil
	}, func(execution context.Context, call *pb.ForwardCall) {
		c.dispatcher.endpoint.handleCall(execution, ctx, call, func(reply *pb.ForwardReply) { c.reply(ctx, reply) })
	})
}

func readForwardCalls(ctx context.Context, link *transport.Link, read func() (*pb.ServerFrame, error), handle func(context.Context, *pb.ForwardCall)) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		f, err := read()
		if err != nil {
			return err
		}
		if call := f.GetForwardCall(); call != nil {
			if err := link.Dispatch(ctx, func(execution context.Context) {
				handle(execution, call)
			}); err != nil {
				return err
			}
		}
	}
}

func (c *relaySession) reply(ctx context.Context, r *pb.ForwardReply) {
	if c.pathLifetime != nil {
		ctx = c.pathLifetime
	}
	b, err := proto.Marshal(&pb.PeerFrame{Kind: &pb.PeerFrame_ForwardReply{ForwardReply: r}})
	if err != nil {
		return
	}
	wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := c.path.Write(wctx, b); err != nil {
		slog.Warn("relay: send reply", "call", r.CallId, "err", err)
	}
}
