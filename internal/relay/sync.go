package relay

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Upload batches stay well below the server's limits.
const (
	uploadEvents = 200
	uploadBytes  = 1 << 20
)

// syncLoop uploads every session, starting sessions as they appear, and
// the machine's metadata when it differs from sentMeta, the one the server
// was given on connecting.
func (c *conn) syncLoop(ctx context.Context, sentMeta *pb.MachineMeta) error {
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
	var sent *pb.Session
	if head != nil {
		after = head.LastSeq
		sent = c.sentMeta(id, key, head.MetaRevision)
		// The server pages its listing by when a session ended; if it
		// holds another time than the metadata says, it gets it again.
		if sent != nil && head.EndedMs != EndedMs(sent) {
			sent = nil
		}
	}
	// An ended session has nothing urgent: its first pass, which after a
	// daemon restart is one request per session, takes turns with the
	// others' so that live sessions are not queued behind them.
	release := func() {}
	if info, err := c.r.backend.SessionInfo(id); err == nil && info.Status == pb.SessionStatus_SESSION_STATUS_EXITED {
		select {
		case c.r.backlog <- struct{}{}:
			release = sync.OnceFunc(func() { <-c.r.backlog })
		case <-ctx.Done():
			return nil
		}
	}
	defer release()
	put := func() (*pb.Session, error) {
		info, err := c.r.backend.SessionInfo(id)
		if err != nil {
			return nil, err
		}
		info.LastSeq = 0 // the server tracks it; leaving it out saves updates
		if sent == nil || !proto.Equal(info, sent) {
			rev, err := c.putSession(ctx, id, key, info)
			if err != nil {
				return nil, err
			}
			sent = info
			c.noteSentMeta(id, key, info, rev)
		}
		return info, nil
	}
	for {
		// The server needs the session, with its key, before it takes
		// events. Once it has it, events go first: they are what a client
		// is waiting for, and the metadata follows in the same pass.
		if sent == nil {
			if _, err := put(); err != nil {
				return err
			}
		}
		evs, changed, err := c.r.backend.Events(id, after)
		if err != nil {
			return err
		}
		var uploaded []*pb.Event
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
			uploaded = append(uploaded, evs[:n]...)
			evs = evs[n:]
		}
		// Read after the events, so it is at least as new as they are.
		info, err := put()
		if err != nil {
			return err
		}
		// Once stored, clients can load what the notification is about.
		for _, e := range uploaded {
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
		release()
		select {
		case <-ctx.Done():
			return nil
		case <-changed:
		case <-time.After(5 * time.Second):
		}
	}
}

// sentMeta returns the metadata the server is known to hold for a
// session: what this process last put, or after a restart what the
// session directory records, if the session still reads the same and the
// server still holds that revision.
func (c *conn) sentMeta(id string, key []byte, revision uint64) *pb.Session {
	c.r.mu.Lock()
	sent := c.r.sentMeta[id]
	c.r.mu.Unlock()
	if sent != nil {
		return sent
	}
	info, err := c.r.backend.SessionInfo(id)
	if err != nil {
		return nil
	}
	info.LastSeq = 0
	mark, err := os.ReadFile(c.sentMark(id))
	if err != nil || !bytes.Equal(mark, metaDigest(c.acct.Server, key, info, revision)) {
		return nil
	}
	return info
}

// noteSentMeta remembers metadata the server accepted. Only an ended
// session's is written down: it rarely changes again, and ended sessions
// are nearly all of what a restarted daemon would otherwise send anew.
func (c *conn) noteSentMeta(id string, key []byte, info *pb.Session, revision uint64) {
	c.r.mu.Lock()
	c.r.sentMeta[id] = info
	c.r.mu.Unlock()
	if info.Status == pb.SessionStatus_SESSION_STATUS_EXITED {
		_ = os.WriteFile(c.sentMark(id), metaDigest(c.acct.Server, key, info, revision), 0o600)
	} else {
		_ = os.Remove(c.sentMark(id))
	}
}

func (c *conn) sentMark(id string) string {
	return filepath.Join(c.r.backend.SessionDir(id), "relay.sent")
}

// metaDigest identifies metadata as sent to one server at one revision.
// It is keyed with the session key, so the file says nothing about the
// content.
func metaDigest(server string, key []byte, info *pb.Session, revision uint64) []byte {
	b, _ := proto.MarshalOptions{Deterministic: true}.Marshal(info)
	h := hmac.New(sha256.New, key)
	h.Write([]byte(server))
	h.Write([]byte{0})
	h.Write(binary.BigEndian.AppendUint64(nil, revision))
	h.Write(b)
	return h.Sum(nil)
}

func (c *conn) sealEvent(key []byte, sessionID string, e *pb.Event) (*pb.Envelope, error) {
	return e2e.SealMessage(key, &pb.Header{
		Kind: pb.PayloadKind_PAYLOAD_KIND_EVENT, MachineId: c.acct.DeviceID, SessionId: sessionID,
		Seq: e.Seq, Sender: c.acct.DeviceID,
	}, fitEvent(slimEvent(e)))
}

// EndedMs is when a session ended as the server is told in the clear, 0
// while it has not ended. It is the time clients order ended sessions by:
// the recorded end, or for a session without one its creation.
func EndedMs(info *pb.Session) int64 {
	if info.GetStatus() != pb.SessionStatus_SESSION_STATUS_EXITED {
		return 0
	}
	for _, t := range []*timestamppb.Timestamp{info.EndedAt, info.CreatedAt} {
		if ms := t.AsTime().UnixMilli(); t != nil && ms > 0 {
			return ms
		}
	}
	return 1
}

// putSession stores a session's key and metadata, and returns the
// metadata's revision.
func (c *conn) putSession(ctx context.Context, id string, key []byte, info *pb.Session) (uint64, error) {
	keyEnv, err := e2e.Seal(c.keys.Wrap, &pb.Header{
		Kind: pb.PayloadKind_PAYLOAD_KIND_SESSION_KEY, MachineId: c.acct.DeviceID, SessionId: id, Sender: c.acct.DeviceID,
	}, key)
	if err != nil {
		return 0, err
	}
	rev := revision()
	meta, err := e2e.SealMessage(key, &pb.Header{
		Kind: pb.PayloadKind_PAYLOAD_KIND_SESSION_META, MachineId: c.acct.DeviceID, SessionId: id,
		Seq: rev, Sender: c.acct.DeviceID,
	}, info)
	if err != nil {
		return 0, err
	}
	_, err = c.machine.PutSession(ctx, connect.NewRequest(&pb.PutSessionRequest{SessionId: id, Key: keyEnv, Meta: meta, EndedMs: EndedMs(info)}))
	if err != nil {
		return 0, fmt.Errorf("put session: %w", err)
	}
	slog.Debug("relay: session metadata sent", "session", id)
	return rev, nil
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
