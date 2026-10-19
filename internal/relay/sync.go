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
	"slices"
	"sync"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Upload batches stay well below the server's limits.
const (
	uploadEvents = 200
	uploadBytes  = 1 << 20
)

// syncLoop uploads sessions and reconciles completed sessions periodically.
// Only live sessions keep a worker; an exited session's worker stops once it
// catches up, while this single scheduler notices later resumes and metadata
// edits.
func (c *conn) syncLoop(ctx context.Context, sentMeta *pb.MachineMeta) error {
	res, err := c.machine.ListSessionHeads(ctx, connect.NewRequest(&pb.ListSessionHeadsRequest{}))
	if err != nil {
		return fmt.Errorf("list session heads: %w", err)
	}
	heads := map[string]*pb.SessionHead{}
	for _, h := range res.Msg.Sessions {
		heads[h.SessionId] = h
	}
	type slot struct {
		running   bool
		synced    bool
		completed bool
		onServer  bool
		ended     bool
		after     uint64
		meta      *pb.Session
		nextCheck time.Time
		failure   syncRetry
	}
	type finished struct {
		id      string
		after   uint64
		info    *pb.Session
		skipped bool
		healthy bool
		err     error
	}
	slots := map[string]*slot{}
	blocked := map[string]bool{}
	done := make(chan finished, 16)
	t := time.NewTicker(min(time.Second, c.r.syncInterval))
	defer t.Stop()
	endedRunning := 0
	for {
		if meta := c.r.backend.MachineMeta(); !proto.Equal(meta, sentMeta) {
			if err := c.putMachine(ctx, meta); err != nil {
				return fmt.Errorf("put machine: %w", err)
			}
			sentMeta = meta
		}
		for _, id := range c.r.backend.SessionIDs() {
			state := slots[id]
			if state != nil && state.running || blocked[id] {
				continue
			}
			if state == nil {
				state = &slot{}
				if head := heads[id]; head != nil {
					state.after, state.onServer = head.LastSeq, true
				}
				slots[id] = state
			}
			now := time.Now()
			if now.Before(state.failure.next) || state.synced && now.Before(state.nextCheck) {
				continue
			}
			info, err := c.r.backend.SessionInfo(id)
			if err != nil {
				state.failure.fail(id, "metadata", now)
				continue
			}
			changed := !state.synced || info.LastSeq > state.after || !proto.Equal(syncMeta(info), state.meta)
			if state.synced && !changed && !c.r.hasOwedNotify(id) {
				state.failure.recovered(id)
				state.nextCheck = now.Add(c.r.syncInterval)
				continue
			}
			isEnded := info.Status == pb.SessionStatus_SESSION_STATUS_EXITED
			if isEnded {
				if endedRunning > 0 {
					continue
				}
				endedRunning++
			}
			state.running = true
			state.ended = isEnded
			head := heads[id]
			if state.completed || head == nil && state.onServer || head != nil && state.after > head.LastSeq {
				head = &pb.SessionHead{LastSeq: state.after, EndedMs: EndedMs(state.meta), MetaRevision: head.GetMetaRevision()}
			}
			go func(id string, head *pb.SessionHead) {
				after, info, skipped, err := c.syncSession(ctx, id, head, func() {
					select {
					case done <- finished{id: id, healthy: true}:
					case <-ctx.Done():
					}
				})
				select {
				case done <- finished{id: id, after: after, info: info, skipped: skipped, err: err}:
				case <-ctx.Done():
				}
			}(id, head)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case result := <-done:
			if ctx.Err() != nil {
				return ctx.Err()
			}
			state := slots[result.id]
			if result.healthy {
				state.failure.recovered(result.id)
				continue
			}
			state.running = false
			if state.ended {
				endedRunning--
				state.ended = false
			}
			if result.err != nil {
				var failure *syncError
				if errors.As(result.err, &failure) && failure.connection {
					return fmt.Errorf("session %s: %w", result.id, result.err)
				}
				// A failed worker may already have committed a valid prefix.
				// Resume after it rather than replaying its notifications.
				if result.after > state.after {
					state.after, state.onServer = result.after, true
				}
				category := "events"
				if failure != nil {
					category = failure.category
				}
				state.failure.fail(result.id, category, time.Now())
				continue
			}
			if result.skipped {
				blocked[result.id] = true
				continue
			}
			state.synced, state.onServer, state.completed = true, true, true
			state.after, state.meta = result.after, syncMeta(result.info)
			state.nextCheck = time.Now().Add(c.r.syncInterval)
			// Re-read completed snapshots before sleeping: an event or
			// metadata edit which landed while the worker was in flight must
			// schedule an immediate follow-up.
			latest, err := c.r.backend.SessionInfo(result.id)
			if err != nil {
				state.failure.fail(result.id, "metadata", time.Now())
				continue
			}
			if latest.LastSeq > state.after || !proto.Equal(syncMeta(latest), state.meta) {
				state.synced = false
				state.nextCheck = time.Time{}
			}
			continue
		case <-t.C:
		}
	}
}

func syncMeta(info *pb.Session) *pb.Session {
	if info == nil {
		return nil
	}
	out := proto.Clone(info).(*pb.Session)
	out.LastSeq = 0
	return out
}

// syncSession keeps one session's metadata and events on the server.
func (c *conn) syncSession(ctx context.Context, id string, head *pb.SessionHead, healthy func()) (uint64, *pb.Session, bool, error) {
	key, err := c.sessionKey(id, head != nil)
	if errors.Is(err, errKeyLost) {
		slog.Error("relay: not syncing session", "session", id, "err", err)
		var after uint64
		if head != nil {
			after = head.LastSeq
		}
		return after, nil, true, nil
	}
	if err != nil {
		return 0, nil, false, localSyncError("key", err)
	}
	var after uint64
	var sent *pb.Session
	if head != nil {
		after = head.LastSeq
		sent = c.sentMeta(id, key, head.MetaRevision)
		if sent != nil && head.EndedMs != EndedMs(sent) {
			sent = nil
		}
	}
	release := func() {}
	if info, err := c.r.backend.SessionInfo(id); err == nil && info.Status == pb.SessionStatus_SESSION_STATUS_EXITED {
		select {
		case c.r.backlog <- struct{}{}:
			release = sync.OnceFunc(func() { <-c.r.backlog })
		case <-ctx.Done():
			return after, nil, false, nil
		}
	}
	defer release()
	floor := head.GetMetaRevision()
	put := func() (*pb.Session, error) {
		info, err := c.r.backend.SessionInfo(id)
		if err != nil {
			return nil, localSyncError("metadata", err)
		}
		meta := syncMeta(info)
		if sent == nil || !proto.Equal(meta, sent) {
			rev, err := c.putSession(ctx, id, key, info, floor)
			if err != nil {
				return nil, err
			}
			sent = meta
			floor = rev
			c.noteSentMeta(id, key, meta, rev)
		}
		return info, nil
	}
	for {
		if sent == nil {
			if _, err := put(); err != nil {
				return after, nil, false, err
			}
		}
		owed := c.r.takeOwedNotify(id)
		attemptedOwed := false
		var batch []*pb.StoredEvent
		var notifyBatch []*pb.Event
		batchBytes := 0
		flush := func() error {
			if len(batch) == 0 && (attemptedOwed || len(owed) == 0) {
				return nil
			}
			if len(batch) > 0 {
				res, err := c.machine.UploadEvents(ctx, connect.NewRequest(&pb.UploadEventsRequest{SessionId: id, Events: batch}))
				if err != nil {
					return rpcSyncError("upload_events", err)
				}
				after = res.Msg.LastSeq
				batch, batchBytes = nil, 0
			}
			info, err := put()
			if err != nil {
				owed = boundedOwed(append(owed, notifyBatch...))
				return err
			}
			pending := notifyBatch
			if !attemptedOwed {
				pending = append(owed, pending...)
				owed = nil
				attemptedOwed = true
			}
			newOwed, err := c.sendNotifications(ctx, id, info, pending)
			owed = boundedOwed(append(owed, newOwed...))
			notifyBatch = nil
			if err != nil {
				c.r.oweNotify(id, owed)
				return err
			}
			return nil
		}
		callbackFailed := false
		changed, err := c.r.backend.EachEvents(id, after, func(e *pb.Event) (err error) {
			defer func() { callbackFailed = err != nil }()
			// Images first: a client that sees the event can open them.
			if err := c.uploadImages(ctx, key, id, []*pb.Event{e}); err != nil {
				return err
			}
			env, err := c.sealEvent(ctx, key, id, c.withImageStates(id, e))
			if err != nil {
				return err
			}
			batch = append(batch, &pb.StoredEvent{Seq: e.Seq, Envelope: env})
			batchBytes += proto.Size(env)
			if notifyKind(e) != pb.NotifyKind_NOTIFY_KIND_UNSPECIFIED {
				notifyBatch = append(notifyBatch, fitEvent(slimEvent(e)))
			}
			if len(batch) >= uploadEvents || batchBytes >= uploadBytes {
				return flush()
			}
			return nil
		})
		if err != nil {
			// A damaged local record can also stop every scan at the same
			// point. Preserve the decoded prefix, but do not retry a failed
			// callback's upload / flush here.
			if !callbackFailed {
				if flushErr := flush(); flushErr != nil {
					err = flushErr
				}
			}
			c.r.oweNotify(id, owed)
			return after, nil, false, err
		}
		if err := flush(); err != nil {
			c.r.oweNotify(id, owed)
			return after, nil, false, err
		}
		info, err := put()
		if err != nil {
			c.r.oweNotify(id, owed)
			return after, nil, false, err
		}
		if !attemptedOwed {
			newOwed, err := c.sendNotifications(ctx, id, info, owed)
			owed = boundedOwed(newOwed)
			if err != nil {
				c.r.oweNotify(id, owed)
				return after, nil, false, err
			}
		}
		c.r.oweNotify(id, owed)
		latest, err := c.r.backend.SessionInfo(id)
		if err != nil {
			return after, nil, false, localSyncError("metadata", err)
		}
		if latest.LastSeq > after || !proto.Equal(syncMeta(latest), syncMeta(info)) {
			continue
		}
		if healthy != nil {
			healthy()
			healthy = nil // One successful pass is enough to reset this worker's backoff.
		}
		if latest.Status == pb.SessionStatus_SESSION_STATUS_EXITED {
			return after, latest, false, nil
		}
		select {
		case <-changed:
			continue
		default:
		}
		select {
		case <-ctx.Done():
			return after, nil, false, nil
		case <-changed:
		case <-time.After(c.r.syncInterval):
		}
	}
}

func boundedOwed(evs []*pb.Event) []*pb.Event {
	return slices.Clone(evs[max(0, len(evs)-maxOwedNotify):])
}

func (c *conn) sendNotifications(ctx context.Context, id string, info *pb.Session, pending []*pb.Event) ([]*pb.Event, error) {
	var owed []*pb.Event
	for i, e := range pending {
		if e.Seq <= info.ForkedSeq {
			continue
		}
		if kind := notifyKind(e); kind != pb.NotifyKind_NOTIFY_KIND_UNSPECIFIED {
			req, err := c.notifyRequest(ctx, id, info, kind, e)
			if err != nil {
				owed = append(owed, pending[i:]...)
				return boundedOwed(owed), localSyncError("encryption", err)
			}
			if _, err := c.machine.Notify(ctx, connect.NewRequest(req)); err != nil {
				failure := rpcSyncError("notify", err)
				var se *syncError
				if errors.As(failure, &se) && se.connection {
					owed = append(owed, pending[i:]...)
					return boundedOwed(owed), failure
				}
				slog.Warn("relay: notify", "session", id, "code", connect.CodeOf(err).String())
				if notifyOwed(err) {
					owed = append(owed, e)
				}
			}
		}
	}
	return boundedOwed(owed), nil
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
	c.r.mu.Lock()
	if cached := c.r.sentMeta[id]; cached != nil {
		info = cached
	} else {
		c.r.sentMeta[id] = info
	}
	c.r.mu.Unlock()
	return info
}

// noteSentMeta remembers metadata the server accepted. Only an ended
// session's is written down: it rarely changes again, and ended sessions
// are nearly all of what a restarted daemon would otherwise send anew.
func (c *conn) noteSentMeta(id string, key []byte, info *pb.Session, revision uint64) {
	path := c.sentMark(id)
	_ = c.writeDisk(func() error {
		c.r.mu.Lock()
		c.r.sentMeta[id] = info
		c.r.mu.Unlock()
		if info.Status == pb.SessionStatus_SESSION_STATUS_EXITED {
			return os.WriteFile(path, metaDigest(c.acct.Server, key, info, revision), 0o600)
		}
		return os.Remove(path)
	})
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

func (c *conn) sealEvent(ctx context.Context, key []byte, sessionID string, e *pb.Event) (*pb.Envelope, error) {
	fitted := fitEvent(slimEvent(e))
	env, err := c.security.sealMessage(&pb.Header{
		Kind: pb.PayloadKind_PAYLOAD_KIND_EVENT, MachineId: c.acct.DeviceID, SessionId: sessionID,
		Seq: e.Seq, Sender: c.acct.DeviceID,
	}, deviceauth.Event, key, fitted)
	if err != nil {
		return nil, localSyncError("encryption", err)
	}
	return env, nil
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
func (c *conn) putSession(ctx context.Context, id string, key []byte, info *pb.Session, floor uint64) (uint64, error) {
	base := proto.CloneOf(info)
	base.CurrentCheckpoint = nil
	base.MachineReplacements = c.acct.Replacements
	checkpoint, checkpointErr := c.currentCheckpoint(id, info)
	if checkpointErr == nil {
		var snapshot pb.LiveSnapshot
		if err := proto.Unmarshal(checkpoint, &snapshot); err != nil {
			checkpointErr = err
		} else {
			info = snapshot.Session
			info.CurrentCheckpoint = checkpoint
			info.MachineReplacements = c.acct.Replacements
		}
	}
	if checkpointErr == nil && proto.Size(info) > deviceauth.MaxMutableBytes {
		checkpointErr = errors.New("current checkpoint exceeds combined session metadata capacity")
	}
	if checkpointErr != nil {
		info = base
	}
	keyEnv, err := c.security.seal(&pb.Header{
		Kind: pb.PayloadKind_PAYLOAD_KIND_SESSION_KEY, Seq: 1, MachineId: c.acct.DeviceID, SessionId: id, Sender: c.acct.DeviceID,
	}, deviceauth.SessionKey, nil, key)
	if err != nil {
		return 0, localSyncError("encryption", err)
	}
	// The caller fetched this object's floor once for its sync batch.
	if floor == ^uint64(0) {
		return 0, localSyncError("revision", deviceauth.ErrCapacity)
	}
	revMu.Lock()
	lastRev = max(lastRev+1, floor+1, uint64(time.Now().UnixMicro()))
	rev := lastRev
	revMu.Unlock()
	meta, err := c.security.sealMessage(&pb.Header{
		Kind: pb.PayloadKind_PAYLOAD_KIND_SESSION_META, MachineId: c.acct.DeviceID, SessionId: id,
		Seq: rev, Sender: c.acct.DeviceID,
	}, deviceauth.SessionMeta, nil, info)
	if err != nil {
		return 0, localSyncError("encryption", err)
	}
	request := &pb.PutSessionRequest{SessionId: id, Key: keyEnv, Meta: meta, EndedMs: EndedMs(info)}
	if checkpointErr == nil {
		checkpointErr = c.persistCheckpoint(id, request)
		if checkpointErr != nil {
			// The checkpoint was not made durable. Publish ordinary metadata
			// without advertising a complete bootstrap, then retry separately.
			request.Meta, err = c.security.sealMessage(&pb.Header{
				Kind: pb.PayloadKind_PAYLOAD_KIND_SESSION_META, MachineId: c.acct.DeviceID,
				SessionId: id, Seq: rev, Sender: c.acct.DeviceID,
			}, deviceauth.SessionMeta, nil, base)
			if err != nil {
				return 0, localSyncError("encryption", err)
			}
		}
	}
	if checkpointErr != nil {
		c.checkpointPending.Store(true)
		slog.Warn("relay: current checkpoint pending; normal session sync continues", "session", id, "err", checkpointErr)
	}
	_, err = c.machine.PutSession(ctx, connect.NewRequest(request))
	if err != nil {
		return 0, rpcSyncError("put_session", err)
	}
	slog.Debug("relay: session metadata sent", "session", id)
	return headerOfSecurity(request.Meta).Seq, nil
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
func (c *conn) notifyRequest(ctx context.Context, sessionID string, info *pb.Session, kind pb.NotifyKind, e *pb.Event) (*pb.NotifyRequest, error) {
	nc := &pb.NotifyContent{SourceEventSeq: e.Seq, MachineId: c.acct.DeviceID, SessionId: sessionID, Kind: kind}
	nc.Title, _ = clip(info.GetTitle(), notifyTitle)
	var dedupe string
	switch k := e.Kind.(type) {
	case *pb.Event_ApprovalRequested:
		a := k.ApprovalRequested
		dedupe = a.ApprovalId
		na := &pb.NotifyApproval{ApprovalId: a.ApprovalId, ToolName: a.ToolName}
		na.Description, na.Truncated = clip(a.Description, notifyText)
		// A command is shown as one; any other tool by its name and input.
		var cut bool
		if a.Command != "" {
			na.Command, cut = clip(a.Command, notifyCommand)
		} else {
			na.ToolInput, cut = clip(a.ToolInputJson, notifyCommand)
		}
		na.Truncated = na.Truncated || cut || approvalTextTruncated(a)
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
	if eventSkeleton(e) {
		// A notification must not reintroduce a decision button for an
		// event whose identity or selectable content was reduced.
		nc.Text = "内容过大，请在终端查看/回答"
		nc.Approval = nil
		// The ID prefix is private event content. Only the already-public
		// sequence may appear in the clear dedupe key and envelope header.
		dedupe = fmt.Sprintf("skeleton:%d", e.Seq)
	}

	routingID := dedupe
	if routingID == "" {
		routingID = fmt.Sprintf("notify:%d:%d", kind, e.Seq)
	}
	env, err := c.security.sealMessage(&pb.Header{
		Kind: pb.PayloadKind_PAYLOAD_KIND_NOTIFY, MachineId: c.acct.DeviceID, SessionId: sessionID,
		RequestId: routingID, Sender: c.acct.DeviceID,
	}, deviceauth.Notify, nil, nc)
	if err != nil {
		return nil, err
	}
	return &pb.NotifyRequest{SessionId: sessionID, Kind: kind, Content: env, DedupeKey: dedupe}, nil
}

// notifyOwed reports whether a notification that failed with err is still
// owed: the server refused it for lack of capacity, which it promises not
// to count as sent, or gave no answer at all, as when the connection
// dropped or the pass was canceled for a reconnect. Any other answer is a
// refusal of the notification itself.
func notifyOwed(err error) bool {
	// A pass canceled while the call still waited for its turn never became
	// a Connect error.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	switch connect.CodeOf(err) {
	case connect.CodeResourceExhausted, connect.CodeCanceled, connect.CodeUnavailable, connect.CodeDeadlineExceeded:
		return true
	}
	return false
}

// maxOwedNotify bounds the owed notifications kept for one session; the
// latest are the ones worth sending.
const maxOwedNotify = 16

// takeOwedNotify returns the events of a session whose notification is
// still owed, and forgets them.
func (r *Relay) takeOwedNotify(id string) []*pb.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	evs := r.owedNotify[id]
	delete(r.owedNotify, id)
	return evs
}

func (r *Relay) hasOwedNotify(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.owedNotify[id]) > 0
}

// oweNotify remembers events whose notification is still owed.
func (r *Relay) oweNotify(id string, evs []*pb.Event) {
	if len(evs) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.owedNotify[id] = boundedOwed(evs)
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
