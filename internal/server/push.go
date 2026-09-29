package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	webpush "github.com/SherClockHolmes/webpush-go"
)

// Web Push. Daemons say that something happened in a session; the server
// sends every subscribed client a notification with generic text, never
// content (scope §4). The push service can read the payload's session ID,
// which the server sees anyway.

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
`

// pushCoalesce drops repeated notifications of one kind for a session.
const pushCoalesce = 15 * time.Second

type pusher struct {
	s          *Server
	mu         sync.Mutex
	pub, priv  string
	recent     map[string]time.Time
	subscriber string
	client     *http.Client
}

func newPusher(s *Server) (*pusher, error) {
	if _, err := s.store.db.Exec(pushSchema); err != nil {
		return nil, err
	}
	p := &pusher{s: s, recent: map[string]time.Time{}, subscriber: "https://github.com/Lewin671/remote-agent", client: &http.Client{Timeout: 20 * time.Second}}
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

var pushText = map[pb.NotifyKind]string{
	pb.NotifyKind_NOTIFY_KIND_ATTENTION:  "有会话在等你处理",
	pb.NotifyKind_NOTIFY_KIND_TURN_ENDED: "一轮对话结束了",
}

// notify pushes a generic notification to subscribed clients.
func (p *pusher) notify(sessionID string, kind pb.NotifyKind) {
	body, ok := pushText[kind]
	if !ok {
		return
	}
	key := sessionID + "/" + kind.String()
	p.mu.Lock()
	if t, ok := p.recent[key]; ok && time.Since(t) < pushCoalesce {
		p.mu.Unlock()
		return
	}
	p.recent[key] = time.Now()
	for k, t := range p.recent {
		if time.Since(t) > pushCoalesce {
			delete(p.recent, k)
		}
	}
	p.mu.Unlock()

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
	payload, _ := json.Marshal(map[string]string{"title": "remote-agent", "body": body, "tag": key, "session": sessionID})
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
	m.s.push.notify(req.Msg.SessionId, req.Msg.Kind)
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
