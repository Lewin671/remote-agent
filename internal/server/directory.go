package server

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
)

// The device directory and account preference objects. Both are sealed by
// clients with keys the server does not have; the server only keeps each
// object's latest revision and replaces it when a write names the revision
// it was based on (compare and swap). The sealed header binds the new
// revision, so the server cannot pass one revision off as another; it can
// still hand a new device an old copy, which no revision check can catch
// (architecture §8).

const v2Schema = imageSchema + `
CREATE TABLE IF NOT EXISTS account_prefs (
	object_id TEXT PRIMARY KEY,
	revision INTEGER NOT NULL,
	envelope BLOB NOT NULL,
	updated_ms INTEGER NOT NULL
);
`

// Size limits of sealed objects: preferences are a list of paths, device
// metadata a name.
const (
	maxPrefsBytes      = 256 << 10
	maxDeviceMetaBytes = 16 << 10
)

// migrate adds the columns later versions need to an older database.
func (s *store) migrate() error {
	cols := map[string]bool{}
	rows, err := s.db.Query(`SELECT name FROM pragma_table_info('devices')`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		cols[name] = true
	}
	rows.Close()
	for _, c := range []struct{ name, def string }{{"meta", "BLOB"}, {"meta_revision", "INTEGER NOT NULL DEFAULT 0"}} {
		if !cols[c.name] {
			if _, err := s.db.Exec(fmt.Sprintf(`ALTER TABLE devices ADD COLUMN %s %s`, c.name, c.def)); err != nil {
				return fmt.Errorf("add devices.%s: %w", c.name, err)
			}
		}
	}
	return nil
}

func scanDevice(sc interface{ Scan(...any) error }) (*pb.Device, error) {
	d := &pb.Device{}
	var kind int32
	var meta []byte
	if err := sc.Scan(&d.Id, &kind, &d.CreatedMs, &meta, &d.MetaRevision); err != nil {
		return nil, err
	}
	d.Kind = pb.DeviceKind(kind)
	d.Meta = unmarshalEnv(meta)
	return d, nil
}

const deviceColumns = `id, kind, created_ms, meta, meta_revision`

func (s *store) devices(ctx context.Context) ([]*pb.Device, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+deviceColumns+` FROM devices ORDER BY created_ms, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*pb.Device
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *store) deviceEntry(ctx context.Context, id string) (*pb.Device, error) {
	return scanDevice(s.db.QueryRowContext(ctx, `SELECT `+deviceColumns+` FROM devices WHERE id = ?`, id))
}

// putDeviceMeta replaces a client device's metadata if its revision is
// expected. It returns the device as stored afterwards.
func (s *store) putDeviceMeta(ctx context.Context, id string, expected uint64, env []byte) (bool, *pb.Device, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, nil, err
	}
	defer tx.Rollback()
	d, err := scanDevice(tx.QueryRowContext(ctx, `SELECT `+deviceColumns+` FROM devices WHERE id = ?`, id))
	if err != nil {
		return false, nil, err
	}
	if d.Kind != pb.DeviceKind_DEVICE_KIND_CLIENT {
		return false, nil, errNotClient
	}
	cur, _ := marshalEnv(d.Meta)
	switch {
	case d.MetaRevision == expected:
		if _, err := tx.ExecContext(ctx, `UPDATE devices SET meta = ?, meta_revision = ? WHERE id = ?`, env, expected+1, id); err != nil {
			return false, nil, err
		}
		d.Meta, d.MetaRevision = unmarshalEnv(env), expected+1
		return true, d, tx.Commit()
	case d.MetaRevision == expected+1 && bytes.Equal(cur, env):
		return true, d, nil // a retry of the write that made it
	}
	return false, d, nil
}

var errNotClient = errors.New("only a client device has metadata of its own")

func (s *store) prefs(ctx context.Context, objectID string) (*pb.AccountPrefsObject, error) {
	return prefsTx(ctx, s.db, objectID)
}

func prefsTx(ctx context.Context, q querier, objectID string) (*pb.AccountPrefsObject, error) {
	o := &pb.AccountPrefsObject{ObjectId: objectID}
	var env []byte
	err := q.QueryRowContext(ctx, `SELECT revision, envelope, updated_ms FROM account_prefs WHERE object_id = ?`, objectID).
		Scan(&o.Revision, &env, &o.UpdatedMs)
	if errors.Is(err, sql.ErrNoRows) {
		return o, nil
	}
	if err != nil {
		return nil, err
	}
	o.Envelope = unmarshalEnv(env)
	return o, nil
}

// putPrefs replaces a preferences object if its revision is expected. It
// returns the object as stored afterwards.
func (s *store) putPrefs(ctx context.Context, objectID string, expected uint64, env []byte) (bool, *pb.AccountPrefsObject, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, nil, err
	}
	defer tx.Rollback()
	o, err := prefsTx(ctx, tx, objectID)
	if err != nil {
		return false, nil, err
	}
	cur, _ := marshalEnv(o.Envelope)
	switch {
	case o.Revision == expected:
		now := nowMs()
		if _, err := tx.ExecContext(ctx, `INSERT INTO account_prefs (object_id, revision, envelope, updated_ms) VALUES (?, ?, ?, ?)
			ON CONFLICT (object_id) DO UPDATE SET revision = excluded.revision, envelope = excluded.envelope, updated_ms = excluded.updated_ms`,
			objectID, expected+1, env, now); err != nil {
			return false, nil, err
		}
		o = &pb.AccountPrefsObject{ObjectId: objectID, Revision: expected + 1, Envelope: unmarshalEnv(env), UpdatedMs: now}
		return true, o, tx.Commit()
	case o.Revision == expected+1 && bytes.Equal(cur, env):
		return true, o, nil
	}
	return false, o, nil
}

// clientFrom returns the calling client device.
func clientFrom(ctx context.Context) (*device, error) {
	d := deviceFrom(ctx)
	if d == nil || d.kind != pb.DeviceKind_DEVICE_KIND_CLIENT {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("only clients may call this"))
	}
	return d, nil
}

// validObjectID accepts short lower-case names like "project-prefs".
func validObjectID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

func (c *clientService) ListDevices(ctx context.Context, req *connect.Request[pb.ListDevicesRequest]) (*connect.Response[pb.ListDevicesResponse], error) {
	ds, err := c.s.store.devices(ctx)
	if err != nil {
		return nil, internal(err)
	}
	return connect.NewResponse(&pb.ListDevicesResponse{Devices: ds, CurrentDeviceId: deviceFrom(ctx).id}), nil
}

func (c *clientService) PutDeviceMeta(ctx context.Context, req *connect.Request[pb.PutDeviceMetaRequest]) (*connect.Response[pb.PutDeviceMetaResponse], error) {
	me, err := clientFrom(ctx)
	if err != nil {
		return nil, err
	}
	m := req.Msg
	if !validDeviceID(m.DeviceId) {
		return nil, invalid("malformed device ID")
	}
	if _, err := checkEnvelope(m.Meta, &pb.Header{
		Kind: pb.PayloadKind_PAYLOAD_KIND_DEVICE_META, RequestId: m.DeviceId, Seq: m.ExpectedRevision + 1, Sender: me.id,
	}); err != nil {
		return nil, err
	}
	b, _ := proto.Marshal(m.Meta)
	if len(b) > maxDeviceMetaBytes {
		return nil, invalid("device metadata larger than %d bytes", maxDeviceMetaBytes)
	}
	applied, d, err := c.s.store.putDeviceMeta(ctx, m.DeviceId, m.ExpectedRevision, b)
	switch {
	case errors.Is(err, errNoRows):
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such device"))
	case errors.Is(err, errNotClient):
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	case err != nil:
		return nil, internal(err)
	}
	if applied {
		c.s.hub.broadcast(&pb.ServerFrame{Kind: &pb.ServerFrame_DeviceUpdated{DeviceUpdated: &pb.DeviceUpdated{Device: d}}})
	}
	return connect.NewResponse(&pb.PutDeviceMetaResponse{Applied: applied, Device: d}), nil
}

func (c *clientService) GetAccountPrefs(ctx context.Context, req *connect.Request[pb.GetAccountPrefsRequest]) (*connect.Response[pb.GetAccountPrefsResponse], error) {
	if _, err := clientFrom(ctx); err != nil {
		return nil, err
	}
	if !validObjectID(req.Msg.ObjectId) {
		return nil, invalid("malformed object ID")
	}
	o, err := c.s.store.prefs(ctx, req.Msg.ObjectId)
	if err != nil {
		return nil, internal(err)
	}
	return connect.NewResponse(&pb.GetAccountPrefsResponse{Object: o}), nil
}

func (c *clientService) PutAccountPrefs(ctx context.Context, req *connect.Request[pb.PutAccountPrefsRequest]) (*connect.Response[pb.PutAccountPrefsResponse], error) {
	me, err := clientFrom(ctx)
	if err != nil {
		return nil, err
	}
	m := req.Msg
	if !validObjectID(m.ObjectId) {
		return nil, invalid("malformed object ID")
	}
	if _, err := checkEnvelope(m.Envelope, &pb.Header{
		Kind: pb.PayloadKind_PAYLOAD_KIND_ACCOUNT_PREFS, RequestId: m.ObjectId, Seq: m.ExpectedRevision + 1, Sender: me.id,
	}); err != nil {
		return nil, err
	}
	b, _ := proto.Marshal(m.Envelope)
	if len(b) > maxPrefsBytes {
		return nil, invalid("preferences larger than %d bytes", maxPrefsBytes)
	}
	applied, o, err := c.s.store.putPrefs(ctx, m.ObjectId, m.ExpectedRevision, b)
	if err != nil {
		return nil, internal(err)
	}
	if applied {
		c.s.hub.broadcast(&pb.ServerFrame{Kind: &pb.ServerFrame_AccountPrefsUpdated{AccountPrefsUpdated: &pb.AccountPrefsUpdated{Object: o}}})
	}
	return connect.NewResponse(&pb.PutAccountPrefsResponse{Applied: applied, Object: o}), nil
}
