package relay

import (
	"context"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/transport"
	"github.com/Lewin671/remote-agent/internal/transport/peerwire"
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
	activity     func(isHeartbeat bool)
}

func (c *relaySession) readLoop(ctx context.Context) error {
	var directedGeneration uint64
	if c.dispatcher.direct != nil {
		var detach func()
		directedGeneration, detach = c.dispatcher.direct.attach(c.path)
		defer detach()
	}
	return readForwardCalls(ctx, func() (*pb.ServerFrame, error) {
		b, err := c.path.Read(ctx)
		if err != nil {
			return nil, err
		}
		f := &pb.ServerFrame{}
		if err := proto.Unmarshal(b, f); err != nil {
			if c.activity != nil {
				c.activity(false)
			}
			return nil, transport.ErrInvalidFrame
		}
		if c.activity != nil {
			c.activity(f.GetHeartbeat() != nil)
		}
		if manager := c.dispatcher.direct; manager != nil {
			if f.GetPeerFrameDelivery() != nil {
				if body, err := peerwire.ServerFrameBody(b, 11); err == nil {
					manager.receive(body)
				}
			}
			if result := f.GetPeerFrameResult(); result != nil {
				manager.delivery(result, directedGeneration)
			}
		}
		return f, nil
	}, func(call *pb.ForwardCall) {
		c.dispatcher.endpoint.dispatch(ctx, call, func(reply *pb.ForwardReply) { c.reply(ctx, reply) })
	})
}

func readForwardCalls(ctx context.Context, read func() (*pb.ServerFrame, error), handle func(*pb.ForwardCall)) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		f, err := read()
		if err != nil {
			return err
		}
		if call := f.GetForwardCall(); call != nil {
			handle(call)
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
