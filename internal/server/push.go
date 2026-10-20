package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	webpush "github.com/SherClockHolmes/webpush-go"
	"google.golang.org/protobuf/proto"
)

// Push. Daemons say that something happened in a session; the server
// sends every subscribed client a notification. Web Push gets generic text,
// never content (scope §4). FCM targets (the Android app) also get the
// daemon's content sealed with the notify key, passed through as it is.
// Either push service can read the payload's session ID, which the server
// sees anyway.

const pushSchema = `
CREATE TABLE IF NOT EXISTS kv (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS push_subs (
	device_id TEXT PRIMARY KEY,
	account_id TEXT NOT NULL,
	subscription TEXT NOT NULL,
	kinds INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS push_subs_account ON push_subs (account_id);
CREATE TABLE IF NOT EXISTS fcm_targets (
	device_id TEXT PRIMARY KEY,
	account_id TEXT NOT NULL,
	token TEXT NOT NULL,
	kinds INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS fcm_targets_account ON fcm_targets (account_id);
`

// pushCoalesce drops repeated notifications of one kind for a session.
const pushCoalesce = 15 * time.Second

// pushDedupe is how long a dedupe key, such as an approval ID, is
// remembered: longer than any approval waits.
const pushDedupe = time.Hour

// Bound both retained dedupe state and asynchronous network deliveries,
// for the relay and for each account, so that one account cannot take
// the others' notifications.
const (
	maxPushRecent            = 4096
	maxAccountPushRecent     = 256
	maxPushDeliveries        = 128
	maxAccountPushDeliveries = 32
)

var errPushBusy = errors.New("push capacity exhausted; try again later")

// Retained keys have a fixed size even when a peer supplies a very long
// session identifier. JSON framing also keeps the fields unambiguous.
func pushRecentKey(acct, sessionID string, kind pb.NotifyKind, dedupeKey string) string {
	b, _ := json.Marshal([]string{acct, sessionID, kind.String(), dedupeKey})
	sum := sha256.Sum256(b)
	key := hex.EncodeToString(sum[:])
	if dedupeKey != "" {
		key = "d/" + key
	}
	return key
}

// pusher sends the notifications of every account. Its VAPID keys and
// clients identify the relay, not an account; what it reads and deletes
// is the targets of the account it notifies.
type pusher struct {
	s         *Server
	mu        sync.Mutex
	pub, priv string
	recent    map[string]pushRecent
	// busy counts the deliveries in flight by account.
	busy       map[string]int
	subscriber string
	client     *http.Client
	fcm        *fcm // nil when not configured
	deliveries chan struct{}
}

func newPusher(s *Server) (*pusher, error) {
	p := &pusher{s: s, recent: map[string]pushRecent{}, busy: map[string]int{}, subscriber: "https://github.com/Lewin671/remote-agent", client: newPushHTTPClient(), deliveries: make(chan struct{}, maxPushDeliveries)}
	if s.cfg.FCMServiceAccount != "" {
		f, err := loadFCM(s.cfg.FCMServiceAccount)
		if err != nil {
			return nil, err
		}
		p.fcm = f
		slog.Info("fcm enabled", "project", f.projectID)
	}
	db := s.store.db
	err := db.QueryRow(`SELECT value FROM kv WHERE key = 'vapid_public'`).Scan(&p.pub)
	if err == nil {
		err = db.QueryRow(`SELECT value FROM kv WHERE key = 'vapid_private'`).Scan(&p.priv)
	}
	if errors.Is(err, sql.ErrNoRows) {
		if p.priv, p.pub, err = webpush.GenerateVAPIDKeys(); err != nil {
			return nil, err
		}
		if _, err := db.Exec(`INSERT INTO kv (key, value) VALUES ('vapid_public', ?), ('vapid_private', ?)`, p.pub, p.priv); err != nil {
			return nil, err
		}
		return p, nil
	}
	return p, err
}

func kindBit(k pb.NotifyKind) int {
	switch k {
	case pb.NotifyKind_NOTIFY_KIND_ATTENTION, pb.NotifyKind_NOTIFY_KIND_TURN_ENDED, pb.NotifyKind_NOTIFY_KIND_SECURITY:
		return 1 << int(k)
	default:
		return 0
	}
}

func validNotifyKinds(kinds []pb.NotifyKind) bool {
	for _, k := range kinds {
		if kindBit(k) == 0 {
			return false
		}
	}
	return true
}

type pushRecent struct {
	acct string
	at   time.Time
}

func (p *pusher) subscribe(ctx context.Context, t *tstore, deviceID, sub string, kinds []pb.NotifyKind) error {
	if !validNotifyKinds(kinds) {
		return errors.New("unknown notification kind")
	}
	if sub == "" {
		_, err := t.db.ExecContext(ctx, `DELETE FROM push_subs WHERE account_id = ? AND device_id = ?`, t.acct, deviceID)
		return err
	}
	var s webpush.Subscription
	if err := json.Unmarshal([]byte(sub), &s); err != nil || s.Endpoint == "" || s.Keys.P256dh == "" || s.Keys.Auth == "" {
		return errors.New("malformed push subscription")
	}
	if err := validatePushEndpoint(s.Endpoint); err != nil {
		return err
	}
	mask := 0
	for _, k := range kinds {
		mask |= kindBit(k)
	}
	_, err := t.db.ExecContext(ctx, `INSERT INTO push_subs (device_id, account_id, subscription, kinds) VALUES (?, ?, ?, ?)
		ON CONFLICT (device_id) DO UPDATE SET subscription = excluded.subscription, kinds = excluded.kinds
		WHERE push_subs.account_id = excluded.account_id`, deviceID, t.acct, sub, mask)
	return err
}

func kindMask(kinds []pb.NotifyKind) int {
	mask := 0
	for _, k := range kinds {
		mask |= kindBit(k)
	}
	return mask
}

func (p *pusher) setFCM(ctx context.Context, t *tstore, deviceID, token string, kinds []pb.NotifyKind) error {
	if !validNotifyKinds(kinds) {
		return errors.New("unknown notification kind")
	}
	if token == "" {
		_, err := t.db.ExecContext(ctx, `DELETE FROM fcm_targets WHERE account_id = ? AND device_id = ?`, t.acct, deviceID)
		return err
	}
	if len(token) > 4096 || strings.ContainsAny(token, " \n\t") {
		return errors.New("malformed FCM token")
	}
	_, err := t.db.ExecContext(ctx, `INSERT INTO fcm_targets (device_id, account_id, token, kinds) VALUES (?, ?, ?, ?)
		ON CONFLICT (device_id) DO UPDATE SET token = excluded.token, kinds = excluded.kinds
		WHERE fcm_targets.account_id = excluded.account_id`, deviceID, t.acct, token, kindMask(kinds))
	return err
}

var pushText = map[pb.NotifyKind]string{
	pb.NotifyKind_NOTIFY_KIND_ATTENTION:  "有会话在等你处理",
	pb.NotifyKind_NOTIFY_KIND_TURN_ENDED: "一轮对话结束了",
	pb.NotifyKind_NOTIFY_KIND_SECURITY:   "有设备申请接管管理权",
}

// notify pushes a notification to subscribed clients. content is a
// serialized NOTIFY envelope for FCM targets, or nil. With a dedupe key the
// notification is sent once per key; without one, once per session and
// kind within pushCoalesce.
func (p *pusher) notify(st *tstore, sessionID string, kind pb.NotifyKind, content []byte, dedupeKey string) error {
	return p.notifyTo(st, "", sessionID, kind, content, dedupeKey)
}

// notifyDevice sends to one device only, for account-level notices that must
// reach the current authority client rather than every member.
func (p *pusher) notifyDevice(st *tstore, deviceID, sessionID string, kind pb.NotifyKind, content []byte, dedupeKey string) error {
	return p.notifyTo(st, deviceID, sessionID, kind, content, dedupeKey)
}

func (p *pusher) notifyTo(st *tstore, deviceID, sessionID string, kind pb.NotifyKind, content []byte, dedupeKey string) error {
	body, ok := pushText[kind]
	if !ok {
		return invalid("unknown notification kind")
	}
	key := pushRecentKey(st.acct, sessionID, kind, dedupeKey)
	window := pushCoalesce
	if dedupeKey != "" {
		window = pushDedupe
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	mine := 0
	for k, r := range p.recent {
		if now.Sub(r.at) >= pushDedupe || (!strings.HasPrefix(k, "d/") && now.Sub(r.at) >= pushCoalesce) {
			delete(p.recent, k)
		} else if r.acct == st.acct {
			mine++
		}
	}
	if r, ok := p.recent[key]; ok && now.Sub(r.at) < window {
		return nil
	}
	if len(p.recent) >= maxPushRecent || mine >= maxAccountPushRecent {
		return errPushBusy
	}

	type target struct{ device, sub, token string }
	var targets []target
	webQuery := `SELECT device_id, subscription FROM push_subs WHERE account_id = ? AND kinds & ? != 0`
	webArgs := []any{st.acct, kindBit(kind)}
	if deviceID != "" {
		webQuery += ` AND device_id = ?`
		webArgs = append(webArgs, deviceID)
	}
	webQuery += ` LIMIT ?`
	webArgs = append(webArgs, maxAccountPushDeliveries+1)
	rows, err := st.db.Query(webQuery, webArgs...)
	if err != nil {
		return err
	}
	for rows.Next() {
		var t target
		if err := rows.Scan(&t.device, &t.sub); err != nil {
			rows.Close()
			return err
		}
		targets = append(targets, t)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if p.fcm != nil {
		fcmQuery := `SELECT device_id, token FROM fcm_targets WHERE account_id = ? AND kinds & ? != 0`
		fcmArgs := []any{st.acct, kindBit(kind)}
		if deviceID != "" {
			fcmQuery += ` AND device_id = ?`
			fcmArgs = append(fcmArgs, deviceID)
		}
		fcmQuery += ` LIMIT ?`
		fcmArgs = append(fcmArgs, maxAccountPushDeliveries+1)
		rows, err := st.db.Query(fcmQuery, fcmArgs...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var t target
			if err := rows.Scan(&t.device, &t.token); err != nil {
				rows.Close()
				return err
			}
			targets = append(targets, t)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	if len(targets) > cap(p.deliveries)-len(p.deliveries) || p.busy[st.acct]+len(targets) > maxAccountPushDeliveries {
		return errPushBusy
	}
	// Reserve every delivery before remembering the key. Rejected work can
	// be retried with the same key, and no notification is partially queued.
	for range targets {
		p.deliveries <- struct{}{}
	}
	if len(targets) > 0 {
		p.busy[st.acct] += len(targets)
	}
	p.recent[key] = pushRecent{acct: st.acct, at: now}
	payload, _ := json.Marshal(map[string]string{"title": "remote-agent", "body": body, "tag": sessionID + "/" + kind.String(), "session": sessionID})
	data := map[string]string{"session": sessionID, "kind": strconv.Itoa(int(kind))}
	if c := base64.StdEncoding.EncodeToString(content); len(content) > 0 && len(c) <= fcmMaxData {
		data["content"] = c
	}
	for _, t := range targets {
		go func() {
			defer func() {
				p.mu.Lock()
				if p.busy[st.acct]--; p.busy[st.acct] <= 0 {
					delete(p.busy, st.acct)
				}
				p.mu.Unlock()
				<-p.deliveries
			}()
			if t.sub != "" {
				p.send(st, t.device, t.sub, payload)
			} else {
				p.sendFCM(st, t.device, t.token, data)
			}
		}()
	}
	return nil
}

func (p *pusher) send(st *tstore, deviceID, sub string, payload []byte) {
	var s webpush.Subscription
	if json.Unmarshal([]byte(sub), &s) != nil || validatePushEndpoint(s.Endpoint) != nil {
		return
	}
	// webpush-go appends padding to the payload backing array. Each concurrent
	// delivery must own its input buffer.
	res, err := webpush.SendNotification(bytes.Clone(payload), &s, &webpush.Options{
		HTTPClient: p.client, Subscriber: p.subscriber, VAPIDPublicKey: p.pub, VAPIDPrivateKey: p.priv,
		TTL: 3600, Urgency: webpush.UrgencyHigh, Topic: "",
	})
	if err != nil {
		slog.Warn("web push delivery failed", "device", deviceID)
		return
	}
	res.Body.Close()
	switch {
	case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone:
		// The browser dropped the subscription: this one, not one the
		// device has registered since the delivery began.
		_, _ = st.db.Exec(`DELETE FROM push_subs WHERE account_id = ? AND device_id = ? AND subscription = ?`, st.acct, deviceID, sub)
	case res.StatusCode >= 300:
		slog.Warn("web push refused", "device", deviceID, "status", res.StatusCode)
	}
}

func (p *pusher) sendFCM(st *tstore, deviceID, token string, data map[string]string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	err := p.fcm.sendRetrying(ctx, token, data, 10*time.Minute)
	switch {
	case errors.Is(err, errFCMToken):
		_, _ = st.db.Exec(`DELETE FROM fcm_targets WHERE account_id = ? AND device_id = ? AND token = ?`, st.acct, deviceID, token)
	case err != nil:
		slog.Warn("fcm push", "device", deviceID, "err", err)
	}
}

// --- API ---

func (c *clientService) GetPushKey(ctx context.Context, req *connect.Request[pb.GetPushKeyRequest]) (*connect.Response[pb.GetPushKeyResponse], error) {
	return connect.NewResponse(&pb.GetPushKeyResponse{PublicKey: c.s.push.pub}), nil
}

func (c *clientService) SetPushSubscription(ctx context.Context, req *connect.Request[pb.SetPushSubscriptionRequest]) (*connect.Response[pb.SetPushSubscriptionResponse], error) {
	d := deviceFrom(ctx)
	if d.kind != pb.DeviceKind_DEVICE_KIND_CLIENT {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("only clients subscribe"))
	}
	if len(req.Msg.SubscriptionJson) > 4096 {
		return nil, invalid("subscription too large")
	}
	if err := c.s.push.subscribe(ctx, c.s.tenantOf(d).store, d.id, req.Msg.SubscriptionJson, req.Msg.Kinds); err != nil {
		return nil, invalid("%v", err)
	}
	return connect.NewResponse(&pb.SetPushSubscriptionResponse{}), nil
}

func (c *clientService) SetPushTarget(ctx context.Context, req *connect.Request[pb.SetPushTargetRequest]) (*connect.Response[pb.SetPushTargetResponse], error) {
	d := deviceFrom(ctx)
	if d.kind != pb.DeviceKind_DEVICE_KIND_CLIENT {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("only clients subscribe"))
	}
	if len(req.Msg.Target) > 4096 {
		return nil, invalid("target too large")
	}
	var err error
	enabled := true
	st := c.s.tenantOf(d).store
	switch req.Msg.Type {
	case pb.PushType_PUSH_TYPE_WEB_PUSH:
		err = c.s.push.subscribe(ctx, st, d.id, req.Msg.Target, req.Msg.Kinds)
	case pb.PushType_PUSH_TYPE_FCM:
		err = c.s.push.setFCM(ctx, st, d.id, req.Msg.Target, req.Msg.Kinds)
		enabled = c.s.push.fcm != nil
	default:
		return nil, invalid("unknown push type")
	}
	if err != nil {
		return nil, invalid("%v", err)
	}
	return connect.NewResponse(&pb.SetPushTargetResponse{Enabled: enabled}), nil
}

func (m *machineService) Notify(ctx context.Context, req *connect.Request[pb.NotifyRequest]) (*connect.Response[pb.NotifyResponse], error) {
	id, err := machineFrom(ctx)
	if err != nil {
		return nil, err
	}
	t := m.s.tenantFrom(ctx)
	owner, err := t.store.sessionMachine(ctx, req.Msg.SessionId)
	if err != nil || owner != id {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("unknown session"))
	}
	var content []byte
	if req.Msg.Content != nil {
		if _, err := checkEnvelope(req.Msg.Content, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_NOTIFY, MachineId: id, SessionId: req.Msg.SessionId}); err != nil {
			return nil, err
		}
		content, _ = proto.Marshal(req.Msg.Content)
	}
	if len(req.Msg.DedupeKey) > 128 {
		return nil, invalid("dedupe key too long")
	}
	if err := m.s.push.notify(t.store, req.Msg.SessionId, req.Msg.Kind, content, req.Msg.DedupeKey); err != nil {
		if errors.Is(err, errPushBusy) {
			return nil, connect.NewError(connect.CodeResourceExhausted, err)
		}
		var apiErr *connect.Error
		if errors.As(err, &apiErr) {
			return nil, err
		}
		return nil, internal(err)
	}
	return connect.NewResponse(&pb.NotifyResponse{}), nil
}
