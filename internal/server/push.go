package server

import (
	"context"
	"database/sql"
	"encoding/base64"
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
	subscription TEXT NOT NULL,
	kinds INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS fcm_targets (
	device_id TEXT PRIMARY KEY,
	token TEXT NOT NULL,
	kinds INTEGER NOT NULL
);
`

// pushCoalesce drops repeated notifications of one kind for a session.
const pushCoalesce = 15 * time.Second

// pushDedupe is how long a dedupe key, such as an approval ID, is
// remembered: longer than any approval waits.
const pushDedupe = time.Hour

type pusher struct {
	s          *Server
	mu         sync.Mutex
	pub, priv  string
	recent     map[string]time.Time
	subscriber string
	client     *http.Client
	fcm        *fcm // nil when not configured
}

func newPusher(s *Server) (*pusher, error) {
	if _, err := s.store.db.Exec(pushSchema); err != nil {
		return nil, err
	}
	p := &pusher{s: s, recent: map[string]time.Time{}, subscriber: "https://github.com/Lewin671/remote-agent", client: &http.Client{Timeout: 20 * time.Second}}
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

func kindBit(k pb.NotifyKind) int { return 1 << int(k) }

func (p *pusher) subscribe(ctx context.Context, deviceID, sub string, kinds []pb.NotifyKind) error {
	db := p.s.store.db
	if sub == "" {
		_, err := db.ExecContext(ctx, `DELETE FROM push_subs WHERE device_id = ?`, deviceID)
		return err
	}
	var s webpush.Subscription
	if err := json.Unmarshal([]byte(sub), &s); err != nil || s.Endpoint == "" || s.Keys.P256dh == "" || s.Keys.Auth == "" {
		return errors.New("malformed push subscription")
	}
	mask := 0
	for _, k := range kinds {
		mask |= kindBit(k)
	}
	_, err := db.ExecContext(ctx, `INSERT INTO push_subs (device_id, subscription, kinds) VALUES (?, ?, ?)
		ON CONFLICT (device_id) DO UPDATE SET subscription = excluded.subscription, kinds = excluded.kinds`, deviceID, sub, mask)
	return err
}

func kindMask(kinds []pb.NotifyKind) int {
	mask := 0
	for _, k := range kinds {
		mask |= kindBit(k)
	}
	return mask
}

func (p *pusher) setFCM(ctx context.Context, deviceID, token string, kinds []pb.NotifyKind) error {
	db := p.s.store.db
	if token == "" {
		_, err := db.ExecContext(ctx, `DELETE FROM fcm_targets WHERE device_id = ?`, deviceID)
		return err
	}
	if len(token) > 4096 || strings.ContainsAny(token, " \n\t") {
		return errors.New("malformed FCM token")
	}
	_, err := db.ExecContext(ctx, `INSERT INTO fcm_targets (device_id, token, kinds) VALUES (?, ?, ?)
		ON CONFLICT (device_id) DO UPDATE SET token = excluded.token, kinds = excluded.kinds`, deviceID, token, kindMask(kinds))
	return err
}

var pushText = map[pb.NotifyKind]string{
	pb.NotifyKind_NOTIFY_KIND_ATTENTION:  "有会话在等你处理",
	pb.NotifyKind_NOTIFY_KIND_TURN_ENDED: "一轮对话结束了",
}

// notify pushes a notification to subscribed clients. content is a
// serialized NOTIFY envelope for FCM targets, or nil. With a dedupe key the
// notification is sent once per key; without one, once per session and
// kind within pushCoalesce.
func (p *pusher) notify(sessionID string, kind pb.NotifyKind, content []byte, dedupeKey string) {
	body, ok := pushText[kind]
	if !ok {
		return
	}
	key := sessionID + "/" + kind.String()
	window := pushCoalesce
	if dedupeKey != "" {
		key = "d/" + dedupeKey
		window = pushDedupe
	}
	p.mu.Lock()
	if t, ok := p.recent[key]; ok && time.Since(t) < window {
		p.mu.Unlock()
		return
	}
	p.recent[key] = time.Now()
	for k, t := range p.recent {
		if time.Since(t) > pushDedupe || (!strings.HasPrefix(k, "d/") && time.Since(t) > pushCoalesce) {
			delete(p.recent, k)
		}
	}
	p.mu.Unlock()
	if p.fcm != nil {
		p.notifyFCM(sessionID, kind, content)
	}

	rows, err := p.s.store.db.Query(`SELECT device_id, subscription FROM push_subs WHERE kinds & ? != 0`, kindBit(kind))
	if err != nil {
		slog.Error("load push subscriptions", "err", err)
		return
	}
	type target struct{ device, sub string }
	var targets []target
	for rows.Next() {
		var t target
		if rows.Scan(&t.device, &t.sub) == nil {
			targets = append(targets, t)
		}
	}
	rows.Close()
	payload, _ := json.Marshal(map[string]string{"title": "remote-agent", "body": body, "tag": sessionID + "/" + kind.String(), "session": sessionID})
	for _, t := range targets {
		go p.send(t.device, t.sub, payload)
	}
}

func (p *pusher) send(deviceID, sub string, payload []byte) {
	var s webpush.Subscription
	if json.Unmarshal([]byte(sub), &s) != nil {
		return
	}
	res, err := webpush.SendNotification(payload, &s, &webpush.Options{
		HTTPClient: p.client, Subscriber: p.subscriber, VAPIDPublicKey: p.pub, VAPIDPrivateKey: p.priv,
		TTL: 3600, Urgency: webpush.UrgencyHigh, Topic: "",
	})
	if err != nil {
		slog.Warn("web push", "device", deviceID, "err", err)
		return
	}
	res.Body.Close()
	switch {
	case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone:
		// The browser dropped the subscription.
		_, _ = p.s.store.db.Exec(`DELETE FROM push_subs WHERE device_id = ?`, deviceID)
	case res.StatusCode >= 300:
		slog.Warn("web push refused", "device", deviceID, "status", res.StatusCode)
	}
}

func (p *pusher) notifyFCM(sessionID string, kind pb.NotifyKind, content []byte) {
	rows, err := p.s.store.db.Query(`SELECT device_id, token FROM fcm_targets WHERE kinds & ? != 0`, kindBit(kind))
	if err != nil {
		slog.Error("load fcm targets", "err", err)
		return
	}
	type target struct{ device, token string }
	var targets []target
	for rows.Next() {
		var t target
		if rows.Scan(&t.device, &t.token) == nil {
			targets = append(targets, t)
		}
	}
	rows.Close()
	data := map[string]string{"session": sessionID, "kind": strconv.Itoa(int(kind))}
	// Too large for FCM: the app shows the generic text instead.
	if c := base64.StdEncoding.EncodeToString(content); len(content) > 0 && len(c) <= fcmMaxData {
		data["content"] = c
	}
	for _, t := range targets {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			err := p.fcm.sendRetrying(ctx, t.token, data, 10*time.Minute)
			switch {
			case errors.Is(err, errFCMToken):
				_, _ = p.s.store.db.Exec(`DELETE FROM fcm_targets WHERE device_id = ? AND token = ?`, t.device, t.token)
			case err != nil:
				slog.Warn("fcm push", "device", t.device, "err", err)
			}
		}()
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
	if err := c.s.push.subscribe(ctx, d.id, req.Msg.SubscriptionJson, req.Msg.Kinds); err != nil {
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
	switch req.Msg.Type {
	case pb.PushType_PUSH_TYPE_WEB_PUSH:
		err = c.s.push.subscribe(ctx, d.id, req.Msg.Target, req.Msg.Kinds)
	case pb.PushType_PUSH_TYPE_FCM:
		err = c.s.push.setFCM(ctx, d.id, req.Msg.Target, req.Msg.Kinds)
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
	var owner string
	err = m.s.store.db.QueryRowContext(ctx, `SELECT machine_id FROM sessions WHERE session_id = ?`, req.Msg.SessionId).Scan(&owner)
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
	m.s.push.notify(req.Msg.SessionId, req.Msg.Kind, content, req.Msg.DedupeKey)
	return connect.NewResponse(&pb.NotifyResponse{}), nil
}

func (c *clientService) RemoveDevice(ctx context.Context, req *connect.Request[pb.RemoveDeviceRequest]) (*connect.Response[pb.RemoveDeviceResponse], error) {
	if deviceFrom(ctx).kind != pb.DeviceKind_DEVICE_KIND_CLIENT {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("only clients remove devices"))
	}
	machine, err := c.s.store.removeDevice(ctx, req.Msg.DeviceId)
	if errors.Is(err, errNoRows) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such device"))
	}
	if err != nil {
		return nil, internal(err)
	}
	slog.Info("device removed", "device", req.Msg.DeviceId)
	if machine {
		c.s.hub.drop(req.Msg.DeviceId)
		c.s.hub.broadcast(&pb.ServerFrame{Kind: &pb.ServerFrame_MachineRemoved{MachineRemoved: &pb.MachineRemoved{MachineId: req.Msg.DeviceId}}})
	}
	return connect.NewResponse(&pb.RemoveDeviceResponse{}), nil
}
