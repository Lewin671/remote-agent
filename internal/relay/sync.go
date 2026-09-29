package relay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"google.golang.org/protobuf/proto"
)

// Upload batches stay well below the server's limits.
const (
	uploadEvents = 200
	uploadBytes  = 1 << 20
)

// syncLoop uploads every session, starting sessions as they appear.
func (c *conn) syncLoop(ctx context.Context) error {
	res, err := c.machine.ListSessionHeads(ctx, connect.NewRequest(&pb.ListSessionHeadsRequest{}))
	if err != nil {
		return fmt.Errorf("list session heads: %w", err)
	}
	heads := map[string]*pb.SessionHead{}
	for _, h := range res.Msg.Sessions {
		heads[h.SessionId] = h
	}
	started := map[string]bool{}
	errc := make(chan error, 1)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		for _, id := range c.r.backend.SessionIDs() {
			if started[id] {
				continue
			}
			started[id] = true
			go func() {
				if err := c.syncSession(ctx, id, heads[id]); err != nil && ctx.Err() == nil {
					select {
					case errc <- fmt.Errorf("session %s: %w", id, err):
					default:
					}
				}
			}()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errc:
			return err
		case <-t.C:
		}
	}
}

// syncSession keeps one session's metadata and events on the server.
func (c *conn) syncSession(ctx context.Context, id string, head *pb.SessionHead) error {
	key, err := c.sessionKey(id, head != nil)
	if errors.Is(err, errKeyLost) {
		// Other sessions keep syncing; this one needs its key back.
		slog.Error("relay: not syncing session", "session", id, "err", err)
		return nil
	}
	if err != nil {
		return err
	}
	var after uint64
	if head != nil {
		after = head.LastSeq
	}
	var sent *pb.Session
	for {
		// Metadata first: the server needs the session, with its key,
		// before it takes events.
		info, err := c.r.backend.SessionInfo(id)
		if err != nil {
			return err
		}
		info.LastSeq = 0 // the server tracks it; leaving it out saves updates
		if sent == nil || !proto.Equal(info, sent) {
			if err := c.putSession(ctx, id, key, info); err != nil {
				return err
			}
			sent = info
		}

		evs, changed, err := c.r.backend.Events(id, after)
		if err != nil {
			return err
		}
		for len(evs) > 0 {
			n, size := 0, 0
			var batch []*pb.StoredEvent
			for n < len(evs) && n < uploadEvents && size < uploadBytes {
				env, err := c.sealEvent(key, id, evs[n])
				if err != nil {
					return err
				}
				batch = append(batch, &pb.StoredEvent{Seq: evs[n].Seq, Envelope: env})
				size += proto.Size(env)
				n++
			}
			res, err := c.machine.UploadEvents(ctx, connect.NewRequest(&pb.UploadEventsRequest{SessionId: id, Events: batch}))
			if err != nil {
				return fmt.Errorf("upload events: %w", err)
			}
			after = res.Msg.LastSeq
			evs = evs[n:]
		}
		select {
		case <-ctx.Done():
			return nil
		case <-changed:
		case <-time.After(5 * time.Second):
		}
	}
}

func (c *conn) sealEvent(key []byte, sessionID string, e *pb.Event) (*pb.Envelope, error) {
	return e2e.SealMessage(key, &pb.Header{
		Kind: pb.PayloadKind_PAYLOAD_KIND_EVENT, MachineId: c.acct.DeviceID, SessionId: sessionID,
		Seq: e.Seq, Sender: c.acct.DeviceID,
	}, e)
}

func (c *conn) putSession(ctx context.Context, id string, key []byte, info *pb.Session) error {
	keyEnv, err := e2e.Seal(c.keys.Wrap, &pb.Header{
		Kind: pb.PayloadKind_PAYLOAD_KIND_SESSION_KEY, MachineId: c.acct.DeviceID, SessionId: id, Sender: c.acct.DeviceID,
	}, key)
	if err != nil {
		return err
	}
	meta, err := e2e.SealMessage(key, &pb.Header{
		Kind: pb.PayloadKind_PAYLOAD_KIND_SESSION_META, MachineId: c.acct.DeviceID, SessionId: id,
		Seq: revision(), Sender: c.acct.DeviceID,
	}, info)
	if err != nil {
		return err
	}
	_, err = c.machine.PutSession(ctx, connect.NewRequest(&pb.PutSessionRequest{SessionId: id, Key: keyEnv, Meta: meta}))
	if err != nil {
		return fmt.Errorf("put session: %w", err)
	}
	slog.Debug("relay: session metadata sent", "session", id)
	return nil
}
