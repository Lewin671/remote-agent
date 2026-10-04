package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	_ "modernc.org/sqlite"
)

// store keeps the server's state in SQLite. Content is only ever stored as
// sealed envelopes.
type store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS account (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	public_key BLOB NOT NULL,
	created_ms INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS devices (
	id TEXT PRIMARY KEY,
	public_key BLOB NOT NULL,
	kind INTEGER NOT NULL,
	registration BLOB NOT NULL,
	signature BLOB NOT NULL,
	created_ms INTEGER NOT NULL,
	meta BLOB,
	meta_revision INTEGER NOT NULL DEFAULT 0,
	last_seen_ms INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS registration_policy (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	freshness_floor_ms INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS revoked_devices (
	id TEXT PRIMARY KEY,
	revoked_ms INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS tokens (
	hash BLOB PRIMARY KEY,
	device_id TEXT NOT NULL,
	expires_ms INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS machines (
	id TEXT PRIMARY KEY,
	meta BLOB,
	meta_revision INTEGER NOT NULL DEFAULT 0,
	last_seen_ms INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS sessions (
	session_id TEXT PRIMARY KEY,
	machine_id TEXT NOT NULL,
	key BLOB NOT NULL,
	meta BLOB,
	meta_revision INTEGER NOT NULL DEFAULT 0,
	last_seq INTEGER NOT NULL DEFAULT 0,
	updated_ms INTEGER NOT NULL,
	-- When the session ended, 0 while it has not: the one thing about a
	-- session's state the server is told in the clear, to page the listing.
	ended_ms INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS sessions_ended ON sessions (ended_ms DESC, session_id);
CREATE TABLE IF NOT EXISTS events (
	session_id TEXT NOT NULL,
	seq INTEGER NOT NULL,
	envelope BLOB NOT NULL,
	PRIMARY KEY (session_id, seq)
) WITHOUT ROWID;
`

func openStore(path string) (*store, error) {
	// One connection: SQLite serializes writers anyway, and it keeps
	// transactions simple.
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}
	// Registrations signed before the floor are refused for unknown
	// devices; a new database has none.
	if _, err := db.Exec(`INSERT INTO registration_policy (id, freshness_floor_ms) VALUES (1, 0) ON CONFLICT (id) DO NOTHING`); err != nil {
		db.Close()
		return nil, err
	}

	return &store{db: db}, nil
}

func (s *store) Close() error { return s.db.Close() }

func nowMs() int64 { return time.Now().UnixMilli() }

var errNoRows = sql.ErrNoRows

func (s *store) accountKey(ctx context.Context) ([]byte, error) {
	var k []byte
	err := s.db.QueryRowContext(ctx, `SELECT public_key FROM account WHERE id = 1`).Scan(&k)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return k, err
}

var errDeviceRevoked = errors.New("device has been revoked; pair again with a new device key")
var errRegistrationStale = errors.New("registration is older than this server accepts; pair again and check the device clock")

// addDevice stores a registered device, creating the account first when
// account is not nil. Registering the same key again is a no-op.
func (s *store) addDevice(ctx context.Context, account []byte, id string, reg *pb.DeviceRegistration, raw, sig []byte) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var revoked int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM revoked_devices WHERE id = ?`, id).Scan(&revoked); err != nil {
		return err
	}
	if revoked != 0 {
		return errDeviceRevoked
	}
	var floor int64
	if err := tx.QueryRowContext(ctx, `SELECT freshness_floor_ms FROM registration_policy WHERE id = 1`).Scan(&floor); err != nil {
		return err
	}
	if reg.CreatedMs < floor {
		var existing int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM devices WHERE id = ?`, id).Scan(&existing); err != nil {
			return err
		}
		if existing == 0 {
			return errRegistrationStale
		}
	}
	if account != nil {
		if _, err := tx.ExecContext(ctx, `INSERT INTO account (id, public_key, created_ms) VALUES (1, ?, ?)`, account, nowMs()); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO devices (id, public_key, kind, registration, signature, created_ms)
		VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (id) DO NOTHING`,
		id, reg.DevicePublicKey, int32(reg.Kind), raw, sig, nowMs()); err != nil {
		return err
	}
	if reg.Kind == pb.DeviceKind_DEVICE_KIND_MACHINE {
		if _, err := tx.ExecContext(ctx, `INSERT INTO machines (id) VALUES (?) ON CONFLICT (id) DO NOTHING`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type device struct {
	id        string
	publicKey []byte
	kind      pb.DeviceKind
	expiresMs int64
}

func (s *store) device(ctx context.Context, id string) (*device, error) {
	d := &device{id: id}
	var kind int32
	err := s.db.QueryRowContext(ctx, `SELECT public_key, kind FROM devices WHERE id = ?`, id).Scan(&d.publicKey, &kind)
	if err != nil {
		return nil, err
	}
	d.kind = pb.DeviceKind(kind)
	return d, nil
}

type registration struct{ raw, sig []byte }

// registration returns a device's registration and the account's
// signature over it, as stored when it registered.
func (s *store) registration(ctx context.Context, id string) (registration, error) {
	var r registration
	err := s.db.QueryRowContext(ctx, `SELECT registration, signature FROM devices WHERE id = ?`, id).Scan(&r.raw, &r.sig)
	return r, err
}

func (s *store) addToken(ctx context.Context, hash []byte, deviceID string, expires int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO tokens (hash, device_id, expires_ms) VALUES (?, ?, ?)`, hash, deviceID, expires)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM tokens WHERE expires_ms < ?`, nowMs())
	return err
}

func (s *store) tokenDevice(ctx context.Context, hash []byte) (*device, error) {
	var id string
	var expires int64
	err := s.db.QueryRowContext(ctx, `SELECT device_id, expires_ms FROM tokens WHERE hash = ?`, hash).Scan(&id, &expires)
	if err != nil {
		return nil, err
	}
	if expires <= nowMs() {
		return nil, errNoRows
	}
	d, err := s.device(ctx, id)
	if err == nil {
		d.expiresMs = expires
	}
	return d, err
}

func marshalEnv(e *pb.Envelope) ([]byte, error) {
	if e == nil {
		return nil, nil
	}
	return proto.Marshal(e)
}

func unmarshalEnv(b []byte) *pb.Envelope {
	if b == nil {
		return nil
	}
	e := &pb.Envelope{}
	if proto.Unmarshal(b, e) != nil {
		return nil
	}
	return e
}

// putMachineMeta stores meta unless a newer revision is stored already.
func (s *store) putMachineMeta(ctx context.Context, id string, meta []byte, revision uint64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE machines SET meta = ?, meta_revision = ? WHERE id = ? AND meta_revision <= ?`,
		meta, revision, id, revision)
	return err
}

func (s *store) touchMachine(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE machines SET last_seen_ms = ? WHERE id = ?`, nowMs(), id)
	return err
}

func (s *store) machines(ctx context.Context) ([]*pb.Machine, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, meta, last_seen_ms FROM machines ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*pb.Machine
	for rows.Next() {
		m := &pb.Machine{}
		var meta []byte
		if err := rows.Scan(&m.Id, &meta, &m.LastSeenMs); err != nil {
			return nil, err
		}
		m.Meta = unmarshalEnv(meta)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *store) machine(ctx context.Context, id string) (*pb.Machine, error) {
	m := &pb.Machine{Id: id}
	var meta []byte
	err := s.db.QueryRowContext(ctx, `SELECT meta, last_seen_ms FROM machines WHERE id = ?`, id).Scan(&meta, &m.LastSeenMs)
	if err != nil {
		return nil, err
	}
	m.Meta = unmarshalEnv(meta)
	return m, nil
}

var errOtherMachine = errors.New("session belongs to another machine")

// putSession creates the session with key, or updates its metadata, and
// with it when the session ended, unless a newer revision is stored. It
// returns the stored session.
func (s *store) putSession(ctx context.Context, machineID, sessionID string, key, meta []byte, revision uint64, endedMs int64) (*pb.RemoteSession, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var owner string
	err = tx.QueryRowContext(ctx, `SELECT machine_id FROM sessions WHERE session_id = ?`, sessionID).Scan(&owner)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if key == nil {
			return nil, errNoKey
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO sessions (session_id, machine_id, key, meta, meta_revision, updated_ms, ended_ms)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, sessionID, machineID, key, meta, revision, nowMs(), endedMs)
	case err != nil:
		return nil, err
	case owner != machineID:
		return nil, errOtherMachine
	case meta != nil:
		_, err = tx.ExecContext(ctx, `UPDATE sessions SET meta = ?, meta_revision = ?, updated_ms = ?, ended_ms = ?
			WHERE session_id = ? AND meta_revision <= ?`, meta, revision, nowMs(), endedMs, sessionID, revision)
	}
	if err != nil {
		return nil, err
	}
	rs, err := sessionTx(ctx, tx, sessionID)
	if err != nil {
		return nil, err
	}
	return rs, tx.Commit()
}

var errNoKey = errors.New("a new session needs its key")

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func sessionTx(ctx context.Context, q querier, id string) (*pb.RemoteSession, error) {
	rs := &pb.RemoteSession{SessionId: id}
	var key, meta []byte
	err := q.QueryRowContext(ctx, `SELECT machine_id, key, meta, last_seq, updated_ms, ended_ms FROM sessions WHERE session_id = ?`, id).
		Scan(&rs.MachineId, &key, &meta, &rs.LastSeq, &rs.UpdatedMs, &rs.EndedMs)
	if err != nil {
		return nil, err
	}
	rs.Key, rs.Meta = unmarshalEnv(key), unmarshalEnv(meta)
	return rs, nil
}

const sessionColumns = `session_id, machine_id, key, meta, last_seq, updated_ms, ended_ms`

func scanSessions(rows *sql.Rows) ([]*pb.RemoteSession, error) {
	defer rows.Close()
	var out []*pb.RemoteSession
	for rows.Next() {
		rs := &pb.RemoteSession{}
		var key, meta []byte
		if err := rows.Scan(&rs.SessionId, &rs.MachineId, &key, &meta, &rs.LastSeq, &rs.UpdatedMs, &rs.EndedMs); err != nil {
			return nil, err
		}
		rs.Key, rs.Meta = unmarshalEnv(key), unmarshalEnv(meta)
		out = append(out, rs)
	}
	return out, rows.Err()
}

// liveSessions returns every session that has not ended.
func (s *store) liveSessions(ctx context.Context) ([]*pb.RemoteSession, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+sessionColumns+` FROM sessions WHERE ended_ms = 0 ORDER BY updated_ms DESC`)
	if err != nil {
		return nil, err
	}
	return scanSessions(rows)
}

// endedSessions returns up to limit ended sessions after the cursor, from
// the latest ended to the earliest, and the cursor for the rest if there
// are more.
func (s *store) endedSessions(ctx context.Context, after *pb.SessionCursor, limit int) ([]*pb.RemoteSession, *pb.SessionCursor, error) {
	query := `SELECT ` + sessionColumns + ` FROM sessions WHERE ended_ms > 0`
	var args []any
	if after != nil {
		// The bound on ended_ms alone lets SQLite seek into the index to
		// the cursor; without it every page scans the ones before it.
		query += ` AND ended_ms <= ? AND (ended_ms < ? OR session_id > ?)`
		args = append(args, after.EndedMs, after.EndedMs, after.SessionId)
	}
	rows, err := s.db.QueryContext(ctx, query+` ORDER BY ended_ms DESC, session_id LIMIT ?`, append(args, limit+1)...)
	if err != nil {
		return nil, nil, err
	}
	out, err := scanSessions(rows)
	if err != nil || len(out) <= limit {
		return out, nil, err
	}
	last := out[limit-1]
	return out[:limit], &pb.SessionCursor{EndedMs: last.EndedMs, SessionId: last.SessionId}, nil
}

// sessionsByID returns those of the sessions named that exist.
func (s *store) sessionsByID(ctx context.Context, ids []string) ([]*pb.RemoteSession, error) {
	var out []*pb.RemoteSession
	for _, id := range ids {
		rs, err := sessionTx(ctx, s.db, id)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, rs)
	}
	return out, nil
}

func (s *store) sessionHeads(ctx context.Context, machineID string) ([]*pb.SessionHead, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT session_id, last_seq, meta_revision, ended_ms FROM sessions WHERE machine_id = ?`, machineID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*pb.SessionHead
	for rows.Next() {
		h := &pb.SessionHead{}
		if err := rows.Scan(&h.SessionId, &h.LastSeq, &h.MetaRevision, &h.EndedMs); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// gapError reports that an upload does not continue the stored events.
type gapError struct{ last uint64 }

func (e *gapError) Error() string {
	return fmt.Sprintf("events must continue after seq %d", e.last)
}

// appendEvents stores the events that follow the stored ones and skips
// those already stored. It returns the newly stored events and the new
// last seq.
func (s *store) appendEvents(ctx context.Context, machineID, sessionID string, evs []*pb.StoredEvent) ([]*pb.StoredEvent, *pb.RemoteSession, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	var owner string
	var last uint64
	err = tx.QueryRowContext(ctx, `SELECT machine_id, last_seq FROM sessions WHERE session_id = ?`, sessionID).Scan(&owner, &last)
	if err != nil {
		return nil, nil, err
	}
	if owner != machineID {
		return nil, nil, errOtherMachine
	}
	var added []*pb.StoredEvent
	for _, e := range evs {
		if e.Seq <= last {
			continue
		}
		if e.Seq != last+1 {
			return nil, nil, &gapError{last: last}
		}
		b, err := proto.Marshal(e.Envelope)
		if err != nil {
			return nil, nil, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO events (session_id, seq, envelope) VALUES (?, ?, ?)`, sessionID, e.Seq, b); err != nil {
			return nil, nil, err
		}
		last = e.Seq
		added = append(added, e)
	}
	if len(added) > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET last_seq = ?, updated_ms = ? WHERE session_id = ?`, last, nowMs(), sessionID); err != nil {
			return nil, nil, err
		}
	}
	rs, err := sessionTx(ctx, tx, sessionID)
	if err != nil {
		return nil, nil, err
	}
	return added, rs, tx.Commit()
}

func (s *store) events(ctx context.Context, sessionID string, after uint64, limit int) ([]*pb.StoredEvent, bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT seq, envelope FROM events WHERE session_id = ? AND seq > ? ORDER BY seq LIMIT ?`,
		sessionID, after, limit+1)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	var out []*pb.StoredEvent
	// Reserve the two bytes used by the More flag. Stop while reading rows,
	// rather than accumulating up to 1000 potentially 1 MiB envelopes first.
	size, more := 2, false
	for rows.Next() {
		if len(out) >= limit {
			more = true
			break
		}
		e := &pb.StoredEvent{}
		var b []byte
		if err := rows.Scan(&e.Seq, &b); err != nil {
			return nil, false, err
		}
		e.Envelope = unmarshalEnv(b)
		n := protowire.SizeTag(1) + protowire.SizeBytes(proto.Size(e))
		if len(out) > 0 && size+n > maxEventPageBytes {
			more = true
			break
		}
		size += n
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	return out, more, nil
}

// removeDevice deletes a device and its logins. For a machine it also
// deletes its sessions and their events. It reports whether the device was
// a machine.
func (s *store) removeDevice(ctx context.Context, id string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var kind int32
	if err := tx.QueryRowContext(ctx, `SELECT kind FROM devices WHERE id = ?`, id).Scan(&kind); err != nil {
		return false, err
	}
	// Keep a tombstone: the old signed registration remains valid even after
	// the live device row is deleted, and must never resurrect this key.
	if _, err := tx.ExecContext(ctx, `INSERT INTO revoked_devices (id, revoked_ms) VALUES (?, ?) ON CONFLICT (id) DO NOTHING`, id, nowMs()); err != nil {
		return false, err
	}
	stmts := []string{
		`DELETE FROM devices WHERE id = ?`,
		`DELETE FROM tokens WHERE device_id = ?`,
		`DELETE FROM push_subs WHERE device_id = ?`,
		`DELETE FROM fcm_targets WHERE device_id = ?`,
		`DELETE FROM events WHERE session_id IN (SELECT session_id FROM sessions WHERE machine_id = ?)`,
		`DELETE FROM images WHERE machine_id = ?`,
		`DELETE FROM sessions WHERE machine_id = ?`,
		`DELETE FROM machines WHERE id = ?`,
	}
	for _, q := range stmts {
		if _, err := tx.ExecContext(ctx, q, id); err != nil {
			return false, err
		}
	}
	return pb.DeviceKind(kind) == pb.DeviceKind_DEVICE_KIND_MACHINE, tx.Commit()
}
