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
	sentMeta := c.r.backend.MachineMeta()
	for {
		// Usage limits change as sessions report them.
		if meta := c.r.backend.MachineMeta(); !proto.Equal(meta, sentMeta) {
			if err := c.putMachine(ctx, meta); err != nil {
				return fmt.Errorf("put machine: %w", err)
			}
			sentMeta = meta
		}
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
				// Images first: a client that sees the event can open them.
				if err := c.uploadImages(ctx, key, id, evs[n:n+1]); err != nil {
					return err
				}
				env, err := c.sealEvent(key, id, c.withImageStates(id, evs[n]))
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
			// Once stored, clients can load what the notification is about.
			for _, e := range evs[:n] {
				if kind := notifyKind(e); kind != pb.NotifyKind_NOTIFY_KIND_UNSPECIFIED {
					req, err := c.notifyRequest(id, info, kind, e)
					if err != nil {
						return err
					}
					if _, err := c.machine.Notify(ctx, connect.NewRequest(req)); err != nil {
						slog.Warn("relay: notify", "session", id, "err", err)
					}
				}
			}
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
	}, fitEvent(e))
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

// Limits on what a notification carries, so that it fits FCM's 4 KB.
const (
	notifyTitle   = 120
	notifyText    = 300
	notifyCommand = 1200
)

func clip(s string, n int) (string, bool) {
	r := []rune(s)
	if len(r) <= n {
		return s, false
	}
	return string(r[:n]) + "…", true
}

// notifyRequest describes event e for a push. The content is sealed with
// the notify key for the Android app; the server only passes it on.
// Approvals and questions are pushed once each, by their ID.
func (c *conn) notifyRequest(sessionID string, info *pb.Session, kind pb.NotifyKind, e *pb.Event) (*pb.NotifyRequest, error) {
	nc := &pb.NotifyContent{MachineId: c.acct.DeviceID, SessionId: sessionID, Kind: kind}
	nc.Title, _ = clip(info.GetTitle(), notifyTitle)
	var dedupe string
	switch k := e.Kind.(type) {
	case *pb.Event_ApprovalRequested:
		a := k.ApprovalRequested
		dedupe = a.ApprovalId
		na := &pb.NotifyApproval{ApprovalId: a.ApprovalId, ToolName: a.ToolName}
		na.Description, _ = clip(a.Description, notifyText)
		// A command is shown as one; any other tool by its name and input.
		if a.Command != "" {
			na.Command, na.Truncated = clip(a.Command, notifyCommand)
		} else {
			na.ToolInput, na.Truncated = clip(a.ToolInputJson, notifyCommand)
		}
		if a.Deadline != nil {
			na.DeadlineMs = a.Deadline.AsTime().UnixMilli()
		}
		nc.Approval = na
	case *pb.Event_QuestionAsked:
		dedupe = k.QuestionAsked.QuestionId
		if qs := k.QuestionAsked.Questions; len(qs) > 0 {
			nc.Text, _ = clip(qs[0].Text, notifyText)
		}
	case *pb.Event_PlanProposed:
		nc.Text = "计划待批准"
	case *pb.Event_SessionState:
		nc.Text, _ = clip(k.SessionState.Detail, notifyText)
	case *pb.Event_TurnEnded:
		nc.Text, _ = clip(info.GetPreview(), notifyText)
	}
	env, err := e2e.SealMessage(c.keys.Notify, &pb.Header{
		Kind: pb.PayloadKind_PAYLOAD_KIND_NOTIFY, MachineId: c.acct.DeviceID, SessionId: sessionID,
		RequestId: dedupe, Sender: c.acct.DeviceID,
	}, nc)
	if err != nil {
		return nil, err
	}
	return &pb.NotifyRequest{SessionId: sessionID, Kind: kind, Content: env, DedupeKey: dedupe}, nil
}

// notifyAge keeps catching up after a reconnect from notifying about the
// past.
const notifyAge = 2 * time.Minute

// notifyKind says whether an event deserves a push notification.
func notifyKind(e *pb.Event) pb.NotifyKind {
	if e.Time == nil || time.Since(e.Time.AsTime()) > notifyAge {
		return pb.NotifyKind_NOTIFY_KIND_UNSPECIFIED
	}
	switch k := e.Kind.(type) {
	case *pb.Event_ApprovalRequested, *pb.Event_PlanProposed:
		return pb.NotifyKind_NOTIFY_KIND_ATTENTION
	case *pb.Event_QuestionAsked:
		if !k.QuestionAsked.Async {
			return pb.NotifyKind_NOTIFY_KIND_ATTENTION
		}
	case *pb.Event_SessionState:
		if k.SessionState.Status == pb.SessionStatus_SESSION_STATUS_BLOCKED {
			return pb.NotifyKind_NOTIFY_KIND_ATTENTION
		}
	case *pb.Event_TurnEnded:
		return pb.NotifyKind_NOTIFY_KIND_TURN_ENDED
	}
	return pb.NotifyKind_NOTIFY_KIND_UNSPECIFIED
}
