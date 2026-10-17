package server

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
)

// Images. Daemons store the display copy of each image in a session as a
// sealed envelope; clients fetch it when a picture is opened. The server
// sees the session, a random image ID, the size and the times, nothing
// else. Images are kept for a while (Config.ImageRetention) and each
// account has a quota of stored bytes (its plan's), checked in the same
// transaction as the insert. An expired image leaves a row without
// its envelope, so that clients can tell "expired" from "never stored".

const imageSchema = `
CREATE TABLE IF NOT EXISTS images (
	account_id TEXT NOT NULL,
	session_id TEXT NOT NULL,
	image_id TEXT NOT NULL,
	machine_id TEXT NOT NULL,
	envelope BLOB,
	bytes INTEGER NOT NULL,
	created_ms INTEGER NOT NULL,
	expires_ms INTEGER NOT NULL,
	PRIMARY KEY (account_id, session_id, image_id)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS images_expiry ON images (expires_ms) WHERE envelope IS NOT NULL;
`

// Default image storage limits (architecture §17).
const (
	defaultImageRetention = 30 * 24 * time.Hour
	defaultImageQuota     = 1 << 30
)

var errQuota = errors.New("the account's image storage is full")

// putImage stores a sealed image unless it is stored already, and returns
// when it expires.
func (t *tstore) putImage(ctx context.Context, machineID, sessionID, imageID string, env []byte, retention time.Duration, quota int64, maxRows int) (int64, error) {
	tx, err := t.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var owner string
	if err := tx.QueryRowContext(ctx, `SELECT machine_id FROM sessions WHERE account_id = ? AND session_id = ?`, t.acct, sessionID).Scan(&owner); err != nil {
		return 0, err
	}
	if owner != machineID {
		return 0, errOtherMachine
	}
	var expires int64
	err = tx.QueryRowContext(ctx, `SELECT expires_ms FROM images WHERE account_id = ? AND session_id = ? AND image_id = ?`, t.acct, sessionID, imageID).Scan(&expires)
	if err == nil {
		// Stored before; an expired one is not brought back under the
		// same ID.
		return expires, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	now := nowMs()
	if _, err := tx.ExecContext(ctx, `UPDATE images SET envelope = NULL WHERE account_id = ? AND envelope IS NOT NULL AND expires_ms <= ?`, t.acct, now); err != nil {
		return 0, err
	}
	var used int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(SUM(bytes), 0) FROM images WHERE account_id = ? AND envelope IS NOT NULL`, t.acct).Scan(&used); err != nil {
		return 0, err
	}
	if used+int64(len(env)) > quota {
		return 0, errQuota
	}
	// An expired image keeps its row, to tell it from one never stored;
	// those rows are bounded by count where the plan says so.
	if maxRows > 0 {
		var rows int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM images WHERE account_id = ?`, t.acct).Scan(&rows); err != nil {
			return 0, err
		}
		if rows >= maxRows {
			return 0, errQuota
		}
	}
	expires = now + retention.Milliseconds()
	if _, err := tx.ExecContext(ctx, `INSERT INTO images (account_id, session_id, image_id, machine_id, envelope, bytes, created_ms, expires_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, t.acct, sessionID, imageID, machineID, env, len(env), now, expires); err != nil {
		return 0, err
	}
	return expires, tx.Commit()
}

// image returns a stored image; a nil envelope means it expired.
func (t *tstore) image(ctx context.Context, sessionID, imageID string) ([]byte, int64, error) {
	var env []byte
	var expires int64
	err := t.db.QueryRowContext(ctx, `SELECT envelope, expires_ms FROM images WHERE account_id = ? AND session_id = ? AND image_id = ?`, t.acct, sessionID, imageID).Scan(&env, &expires)
	if err != nil {
		return nil, 0, err
	}
	if expires <= nowMs() {
		env = nil
	}
	return env, expires, nil
}

// expireImages drops the envelopes of images past their time, keeping
// the rows, in every account. It returns how many it dropped.
func (s *store) expireImages(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE images SET envelope = NULL WHERE envelope IS NOT NULL AND expires_ms <= ?`, nowMs())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// expireLoop drops expired images every hour until stop is closed.
func (s *Server) expireLoop(stop <-chan struct{}) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if n, err := s.store.expireImages(context.Background()); err != nil {
			slog.Error("expire images", "err", err)
		} else if n > 0 {
			slog.Info("expired images", "count", n)
		}
		select {
		case <-stop:
			return
		case <-t.C:
		}
	}
}

func (m *machineService) PutImage(ctx context.Context, req *connect.Request[pb.PutImageRequest]) (*connect.Response[pb.PutImageResponse], error) {
	id, err := machineFrom(ctx)
	if err != nil {
		return nil, err
	}
	if !validHexID(req.Msg.ImageId) || req.Msg.SessionId == "" {
		return nil, invalid("malformed image ID")
	}
	if _, err := checkEnvelope(req.Msg.Image, &pb.Header{
		Kind: pb.PayloadKind_PAYLOAD_KIND_IMAGE, MachineId: id, SessionId: req.Msg.SessionId, RequestId: req.Msg.ImageId,
	}); err != nil {
		return nil, err
	}
	if m.s.diskLow.Load() {
		return nil, errFull("this relay is out of storage; try again later")
	}
	b, _ := proto.Marshal(req.Msg.Image)
	t := m.s.tenantFrom(ctx)
	expires, err := t.store.putImage(ctx, id, req.Msg.SessionId, req.Msg.ImageId, b, m.s.cfg.ImageRetention, t.imageQuota(m.s), t.plan.imageRows)
	switch {
	case errors.Is(err, errNoRows):
		return nil, connect.NewError(connect.CodeNotFound, errors.New("unknown session; put it first"))
	case errors.Is(err, errOtherMachine):
		return nil, connect.NewError(connect.CodePermissionDenied, err)
	case errors.Is(err, errQuota):
		return nil, connect.NewError(connect.CodeResourceExhausted, err)
	case err != nil:
		return nil, internal(err)
	}
	return connect.NewResponse(&pb.PutImageResponse{ExpiresMs: expires}), nil
}

func (c *clientService) GetImage(ctx context.Context, req *connect.Request[pb.GetImageRequest]) (*connect.Response[pb.GetImageResponse], error) {
	env, expires, err := c.s.tenantFrom(ctx).store.image(ctx, req.Msg.SessionId, req.Msg.ImageId)
	if errors.Is(err, errNoRows) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such image"))
	}
	if err != nil {
		return nil, internal(err)
	}
	res := &pb.GetImageResponse{ExpiresMs: expires}
	if env == nil {
		res.Expired = true
	} else {
		res.Image = unmarshalEnv(env)
	}
	return connect.NewResponse(res), nil
}

// validHexID reports whether id is 128 random bits in lower-case hex.
func validHexID(id string) bool { return validDeviceID(id) }
