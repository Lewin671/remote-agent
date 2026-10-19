package server

import (
	"bytes"
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
// sealed envelopes. One database holds every account; each row of an
// account's data carries its account ID. store itself only reaches what
// belongs to no account or is looked up before the account is known:
// accounts, devices by ID for logging in, tokens. Everything else goes
// through a tstore, which is bound to one account and has no way to name
// another.
type store struct {
	db *sql.DB
}

// tstore is the store of one account. Every statement it runs is limited
// to its account.
type tstore struct {
	db   *sql.DB
	acct string
}

// tenant returns the store of an account.
func (s *store) tenant(account string) *tstore {
	if account == "" {
		panic("tenant store without an account")
	}
	return &tstore{db: s.db, acct: account}
}

// schemaVersion is PRAGMA user_version of the current layout. The layout
// of one account per relay, before accounts had IDs, had none.
const schemaVersion = 6

const schema = `
CREATE TABLE IF NOT EXISTS accounts (
	id TEXT PRIMARY KEY,
	public_key BLOB NOT NULL UNIQUE,
	plan TEXT NOT NULL,
	status INTEGER NOT NULL DEFAULT 0,
	created_ms INTEGER NOT NULL,
	last_active_ms INTEGER NOT NULL DEFAULT 0,
	-- Bytes of stored event envelopes, kept with every insert for the quota.
	event_bytes INTEGER NOT NULL DEFAULT 0,
	-- Registrations signed before this are refused for unknown devices.
	registration_floor_ms INTEGER NOT NULL DEFAULT 0,
	security_genesis BLOB,
	-- Recovery public key of the current signed head, kept here so an
	-- unauthenticated recovery lookup can find the account.
	recovery_ed BLOB,
	-- Set by the protocol 10 database upgrade: the account predates the
	-- phone authority, so its genesis must be pinned offline instead of
	-- over the network.
	offline_pin INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS accounts_recovery ON accounts (recovery_ed);
-- Device IDs are derived from device keys and unique across accounts: a
-- device belongs to one account.
CREATE TABLE IF NOT EXISTS devices (
	id TEXT PRIMARY KEY,
	account_id TEXT NOT NULL REFERENCES accounts (id),
	public_key BLOB NOT NULL,
	kind INTEGER NOT NULL,
	registration BLOB NOT NULL,
	signature BLOB NOT NULL,
	created_ms INTEGER NOT NULL,
	meta BLOB,
	meta_revision INTEGER NOT NULL DEFAULT 0,
	last_seen_ms INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS devices_account ON devices (account_id, created_ms, id);
-- A removed device key must never register again, in any account.
CREATE TABLE IF NOT EXISTS revoked_devices (
	id TEXT PRIMARY KEY,
	account_id TEXT NOT NULL,
	revoked_ms INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS revoked_devices_account ON revoked_devices (account_id);
-- Public, device-signed, expiring authorizations. No account secret is stored.
CREATE TABLE IF NOT EXISTS pairing_admissions (
 pairing_id TEXT PRIMARY KEY,
 account_id TEXT NOT NULL REFERENCES accounts(id),
 approver_device_id TEXT NOT NULL,
 candidate_device_id TEXT NOT NULL,
 candidate_kind INTEGER NOT NULL,
 approval BLOB NOT NULL,
 signature BLOB NOT NULL,
 candidate_token_hash BLOB NOT NULL,
 expires_ms INTEGER NOT NULL,
 used_ms INTEGER NOT NULL DEFAULT 0,
 canceled_ms INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS pairing_admissions_account ON pairing_admissions(account_id, expires_ms);

CREATE TABLE IF NOT EXISTS tokens (
	hash BLOB PRIMARY KEY,
	account_id TEXT NOT NULL,
	device_id TEXT NOT NULL,
	expires_ms INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS tokens_device ON tokens (account_id, device_id);
CREATE TABLE IF NOT EXISTS machines (
	id TEXT PRIMARY KEY,
	account_id TEXT NOT NULL,
	meta BLOB,
	meta_revision INTEGER NOT NULL DEFAULT 0,
	last_seen_ms INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS machines_account ON machines (account_id, id);
CREATE TABLE IF NOT EXISTS sessions (
	account_id TEXT NOT NULL,
	session_id TEXT NOT NULL,
	machine_id TEXT NOT NULL,
	key BLOB NOT NULL,
	meta BLOB,
	meta_revision INTEGER NOT NULL DEFAULT 0,
	last_seq INTEGER NOT NULL DEFAULT 0,
	-- Content epoch of the stored SESSION_KEY envelope, so a recovery
	-- re-publish can replace it and an older epoch cannot.
	key_content_epoch INTEGER NOT NULL DEFAULT 0,
	updated_ms INTEGER NOT NULL,
	-- When the session ended, 0 while it has not: the one thing about a
	-- session's state the server is told in the clear, to page the listing.
	ended_ms INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (account_id, session_id)
);
CREATE INDEX IF NOT EXISTS sessions_ended ON sessions (account_id, ended_ms DESC, session_id);
CREATE INDEX IF NOT EXISTS sessions_machine ON sessions (account_id, machine_id);
CREATE TABLE IF NOT EXISTS events (
	account_id TEXT NOT NULL,
	session_id TEXT NOT NULL,
	seq INTEGER NOT NULL,
	envelope BLOB NOT NULL,
	PRIMARY KEY (account_id, session_id, seq)
) WITHOUT ROWID;
-- The phone-signed membership chain. The server stores public signed states
-- only; it validates signatures and membership, never content keys.
CREATE TABLE IF NOT EXISTS security_states (
	account_id TEXT NOT NULL,
	epoch INTEGER NOT NULL,
	state BLOB NOT NULL,
	hash BLOB NOT NULL,
	recovery BLOB,
	PRIMARY KEY (account_id, epoch)
);
-- One pending, rejected or completed authority recovery per account.
CREATE TABLE IF NOT EXISTS security_recovery (
	account_id TEXT PRIMARY KEY,
	recovery_id TEXT NOT NULL UNIQUE,
	statement BLOB NOT NULL,
	state BLOB NOT NULL,
	-- The recovery key the statement installs, indexed so a phone holding it
	-- can find its pending row without scanning every account.
	new_recovery_ed BLOB NOT NULL,
	submitted_ms INTEGER NOT NULL,
	ready_ms INTEGER NOT NULL,
	completed_ms INTEGER NOT NULL DEFAULT 0,
	rejected_ms INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS security_recovery_new_ed ON security_recovery (new_recovery_ed);
-- Completed operations remain addressable when a later recovery replaces the pending row.
CREATE TABLE IF NOT EXISTS security_recovery_completed (
 account_id TEXT NOT NULL,
 recovery_id TEXT PRIMARY KEY,
 statement BLOB NOT NULL,
 state BLOB NOT NULL,
 submitted_ms INTEGER NOT NULL,
 ready_ms INTEGER NOT NULL,
 completed_ms INTEGER NOT NULL,
 rejected_ms INTEGER NOT NULL
);
`

var errOldLayout = errors.New("this database has the layout of a relay with a single account; " +
	"move it to the current layout as the release notes describe (docs/self-hosting.md, \"Upgrading to v0.41.0\") before starting this version")

func openStore(path string) (*store, error) {
	// One connection: SQLite serializes writers anyway, and it keeps
	// transactions simple.
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	// The layout is checked before anything is created: a database of the
	// single-account layout must not get new tables next to its old ones.
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		db.Close()
		return nil, err
	}
	var tables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table'`).Scan(&tables); err != nil {
		db.Close()
		return nil, err
	}
	switch {
	case version == schemaVersion:
	case version == 0 && tables == 0:
	case version == 0:
		db.Close()
		return nil, errOldLayout
	case version == 3:
		db.Close()
		return nil, fmt.Errorf("the database has layout 3; this version requires layout %d; apply the manual upgrade in docs/self-hosting.md#upgrading-device-enrollment-protocol-8 before starting", schemaVersion)
	case version == 5:
		db.Close()
		return nil, fmt.Errorf("the database has layout 5; this version requires layout %d; apply the manual upgrade in docs/self-hosting.md#upgrading-to-the-phone-authority-protocol-10 before starting", schemaVersion)
	default:
		db.Close()
		return nil, fmt.Errorf("the database has layout %d; this version knows layout %d", version, schemaVersion)
	}
	// In one transaction with the version, so that a start cut short leaves
	// no tables without it, which would look like the old layout.
	if err := createSchema(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}
	return &store{db: db}, nil
}

func createSchema(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, q := range []string{schema, imageSchema, prefsSchema, pushSchema, fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion)} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *store) Close() error { return s.db.Close() }

func nowMs() int64 { return time.Now().UnixMilli() }

var errNoRows = sql.ErrNoRows

// Account states.
const (
	accountActive    = 0
	accountSuspended = 1
)

type accountRow struct {
	id         string
	publicKey  []byte
	plan       string
	status     int
	createdMs  int64
	lastActive int64
	eventBytes int64
}

const accountColumns = `id, public_key, plan, status, created_ms, last_active_ms, event_bytes`

func scanAccount(sc interface{ Scan(...any) error }) (*accountRow, error) {
	a := &accountRow{}
	err := sc.Scan(&a.id, &a.publicKey, &a.plan, &a.status, &a.createdMs, &a.lastActive, &a.eventBytes)
	return a, err
}

// account returns an account by ID, or errNoRows.
func (s *store) account(ctx context.Context, id string) (*accountRow, error) {
	return scanAccount(s.db.QueryRowContext(ctx, `SELECT `+accountColumns+` FROM accounts WHERE id = ?`, id))
}

func (s *store) accounts(ctx context.Context) ([]*accountRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+accountColumns+` FROM accounts ORDER BY created_ms, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*accountRow
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *store) accountCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounts`).Scan(&n)
	return n, err
}

func (s *store) setAccountStatus(ctx context.Context, id string, status int) error {
	res, err := s.db.ExecContext(ctx, `UPDATE accounts SET status = ? WHERE id = ?`, status, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNoRows
	}
	return nil
}

// accountTables are the tables holding an account's data, in the order
// its rows are deleted.
var accountTables = []string{"security_recovery_completed", "security_recovery", "security_states", "pairing_admissions", "events", "images", "sessions", "account_prefs", "push_subs", "fcm_targets", "tokens", "machines", "devices"}

// deleteAccount deletes an account and everything stored for it. Its
// device keys stay revoked, so that its registrations cannot come back.
func (s *store) deleteAccount(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `INSERT INTO revoked_devices (id, account_id, revoked_ms)
		SELECT id, account_id, ? FROM devices WHERE account_id = ? ON CONFLICT (id) DO NOTHING`, nowMs(), id); err != nil {
		return err
	}
	for _, t := range accountTables {
		if _, err := tx.ExecContext(ctx, `DELETE FROM `+t+` WHERE account_id = ?`, id); err != nil {
			return err
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM accounts WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errNoRows
	}
	return tx.Commit()
}

// accountUsage is what an account has stored, for its quota.
type accountUsage struct {
	devices, machines, sessions int
	eventBytes, imageBytes      int64
	revoked, prefs              int
}

func (t *tstore) usage(ctx context.Context) (accountUsage, error) {
	return usageTx(ctx, t.db, t.acct)
}

func usageTx(ctx context.Context, q querier, acct string) (accountUsage, error) {
	var u accountUsage
	err := q.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM devices WHERE account_id = ?1),
		(SELECT COUNT(*) FROM machines WHERE account_id = ?1),
		(SELECT COUNT(*) FROM sessions WHERE account_id = ?1),
		(SELECT event_bytes FROM accounts WHERE id = ?1),
		(SELECT COALESCE(SUM(bytes), 0) FROM images WHERE account_id = ?1 AND envelope IS NOT NULL),
		(SELECT COUNT(*) FROM revoked_devices WHERE account_id = ?1),
		(SELECT COUNT(*) FROM account_prefs WHERE account_id = ?1)`, acct).
		Scan(&u.devices, &u.machines, &u.sessions, &u.eventBytes, &u.imageBytes, &u.revoked, &u.prefs)
	return u, err
}

var errDeviceRevoked = errors.New("device has been revoked; pair again with a new device key")
var errRegistrationStale = errors.New("registration is older than this server accepts; pair again and check the device clock")
var errOtherAccount = errors.New("device belongs to another account")

// newAccount is an account to create along with its first device, if the
// relay holds fewer than max accounts.
type newAccount struct {
	publicKey []byte
	plan      string
	max       int
}

var errNoRoom = errors.New("this relay has no room for another account")

// addDevice stores a registered device of account, creating the account
// first when create is not nil. Registering the same key again is a no-op.
// limit, if not nil, is checked against the account's usage before a new
// device is stored.
func (s *store) addDevice(ctx context.Context, account string, create *newAccount, id string, reg *pb.DeviceRegistration, raw, sig []byte, limit func(accountUsage) error, approval *enrollmentApproval) error {
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
	created := false
	if create != nil {
		// Two first devices of one account may race: the one that comes
		// second finds the account made and must obtain a device approval.
		var exists, n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FILTER (WHERE id = ?), COUNT(*) FROM accounts`, account).Scan(&exists, &n); err != nil {
			return err
		}
		if exists == 0 {
			if n >= create.max {
				return errNoRoom
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO accounts (id, public_key, plan, created_ms, last_active_ms) VALUES (?, ?, ?, ?, ?)`,
				account, create.publicKey, create.plan, nowMs(), nowMs()); err != nil {
				return err
			}
			created = true
		}
	}
	var state int
	if err := tx.QueryRowContext(ctx, `SELECT status FROM accounts WHERE id = ?`, account).Scan(&state); err != nil {
		return err
	}
	if state != accountActive {
		return errSuspended
	}
	var owner string
	var priorRaw, priorSig, priorPub []byte
	var priorKind int32
	err = tx.QueryRowContext(ctx, `SELECT account_id, registration, signature, public_key, kind FROM devices WHERE id = ?`, id).Scan(&owner, &priorRaw, &priorSig, &priorPub, &priorKind)
	switch {
	case err == nil && owner != account:
		return errOtherAccount
	case err == nil:
		if bytes.Equal(priorRaw, raw) && bytes.Equal(priorSig, sig) {
			return tx.Commit() // Exact response-loss retry, before admission consumption.
		}
		// The phone may register the candidate while preparing the pairing
		// and the candidate then registers with the same approval. A
		// replacement for the same device key/kind is that same device, not
		// a new admission; otherwise require a fresh unused admission.
		if approval == nil || !bytes.Equal(priorPub, reg.DevicePublicKey) || priorKind != int32(reg.Kind) {
			if err := consumeAdmission(ctx, tx, account, id, reg, approval); err != nil {
				return err
			}
		} else {
			// Replacing an existing row still needs the current authority's
			// approval and the stored admission it names. Without this a
			// recovery-code holder could wipe another device's tokens and
			// push targets by re-registering its public key.
			if err := checkApprover(ctx, tx, approval.info); err != nil {
				return err
			}
			if err := requireAdmission(ctx, tx, approval, account, id, reg.Kind); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM tokens WHERE account_id = ? AND device_id = ?`, account, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM push_subs WHERE account_id = ? AND device_id = ?`, account, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM fcm_targets WHERE account_id = ? AND device_id = ?`, account, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE devices SET public_key = ?, kind = ?, registration = ?, signature = ? WHERE account_id = ? AND id = ?`,
			reg.DevicePublicKey, int32(reg.Kind), raw, sig, account, id); err != nil {
			return err
		}
		return tx.Commit()
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	if !created {
		if err := consumeAdmission(ctx, tx, account, id, reg, approval); err != nil {
			return err
		}
	}
	var floor int64
	if err := tx.QueryRowContext(ctx, `SELECT registration_floor_ms FROM accounts WHERE id = ?`, account).Scan(&floor); err != nil {
		return err
	}
	if reg.CreatedMs < floor {
		return errRegistrationStale
	}
	if limit != nil {
		u, err := usageTx(ctx, tx, account)
		if err != nil {
			return err
		}
		if reg.Kind == pb.DeviceKind_DEVICE_KIND_MACHINE {
			u.machines++
		}
		u.devices++
		if err := limit(u); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO devices (id, account_id, public_key, kind, registration, signature, created_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, account, reg.DevicePublicKey, int32(reg.Kind), raw, sig, nowMs()); err != nil {
		return err
	}
	if reg.Kind == pb.DeviceKind_DEVICE_KIND_MACHINE {
		if _, err := tx.ExecContext(ctx, `INSERT INTO machines (id, account_id) VALUES (?, ?)`, id, account); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type device struct {
	id        string
	account   string
	plan      string
	publicKey []byte
	kind      pb.DeviceKind
	expiresMs int64
}

// device looks a device up by ID, before its account is known: to log in.
func (s *store) device(ctx context.Context, id string) (*device, error) {
	d := &device{id: id}
	var kind int32
	err := s.db.QueryRowContext(ctx, `SELECT account_id, public_key, kind FROM devices WHERE id = ?`, id).Scan(&d.account, &d.publicKey, &kind)
	if err != nil {
		return nil, err
	}
	d.kind = pb.DeviceKind(kind)
	return d, nil
}

type registration struct{ raw, sig []byte }

// registration returns a device's registration and the account's
// signature over it, as stored when it registered.
func (t *tstore) registration(ctx context.Context, id string) (registration, error) {
	var r registration
	err := t.db.QueryRowContext(ctx, `SELECT registration, signature FROM devices WHERE account_id = ? AND id = ?`, t.acct, id).Scan(&r.raw, &r.sig)
	return r, err
}

// maxDeviceTokens is how many login tokens a device keeps: a login past
// that ends the device's oldest one, so logging in again and again takes
// no room.
const maxDeviceTokens = 16

func (t *tstore) addToken(ctx context.Context, hash []byte, deviceID string, expires int64) error {
	_, err := t.db.ExecContext(ctx, `INSERT INTO tokens (hash, account_id, device_id, expires_ms) VALUES (?, ?, ?, ?)`, hash, t.acct, deviceID, expires)
	if err != nil {
		return err
	}
	if _, err = t.db.ExecContext(ctx, `DELETE FROM tokens WHERE account_id = ?1 AND device_id = ?2 AND hash NOT IN
		(SELECT hash FROM tokens WHERE account_id = ?1 AND device_id = ?2 ORDER BY expires_ms DESC LIMIT ?3)`, t.acct, deviceID, maxDeviceTokens); err != nil {
		return err
	}
	_, err = t.db.ExecContext(ctx, `DELETE FROM tokens WHERE expires_ms < ?`, nowMs())
	return err
}

var errSuspended = errors.New("this account is suspended on this relay")

// tokenDevice resolves a login token to its device, which carries its
// account. A suspended account's tokens are refused.
func (s *store) tokenDevice(ctx context.Context, hash []byte) (*device, error) {
	d := &device{}
	var kind int32
	var status int
	err := s.db.QueryRowContext(ctx, `SELECT t.device_id, t.account_id, t.expires_ms, d.public_key, d.kind, a.plan, a.status
		FROM tokens t JOIN devices d ON d.id = t.device_id AND d.account_id = t.account_id JOIN accounts a ON a.id = t.account_id
		WHERE t.hash = ?`, hash).Scan(&d.id, &d.account, &d.expiresMs, &d.publicKey, &kind, &d.plan, &status)
	if err != nil {
		return nil, err
	}
	if d.expiresMs <= nowMs() {
		return nil, errNoRows
	}
	if status != accountActive {
		return nil, errSuspended
	}
	d.kind = pb.DeviceKind(kind)
	return d, nil
}

// touchAccount records that an account was in use just now.
func (t *tstore) touchAccount(ctx context.Context) error {
	_, err := t.db.ExecContext(ctx, `UPDATE accounts SET last_active_ms = ? WHERE id = ?`, nowMs(), t.acct)
	return err
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
func (t *tstore) putMachineMeta(ctx context.Context, id string, meta []byte, revision uint64) error {
	_, err := t.db.ExecContext(ctx, `UPDATE machines SET meta = ?, meta_revision = ? WHERE account_id = ? AND id = ? AND meta_revision <= ?`,
		meta, revision, t.acct, id, revision)
	return err
}

func (t *tstore) touchMachine(ctx context.Context, id string) error {
	now := nowMs()
	if _, err := t.db.ExecContext(ctx, `UPDATE machines SET last_seen_ms = ? WHERE account_id = ? AND id = ?`, now, t.acct, id); err != nil {
		return err
	}
	return t.touchAccount(ctx)
}

func (t *tstore) machines(ctx context.Context) ([]*pb.Machine, error) {
	rows, err := t.db.QueryContext(ctx, `SELECT id, meta, last_seen_ms FROM machines WHERE account_id = ? ORDER BY id`, t.acct)
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

func (t *tstore) machine(ctx context.Context, id string) (*pb.Machine, error) {
	m := &pb.Machine{Id: id}
	var meta []byte
	err := t.db.QueryRowContext(ctx, `SELECT meta, last_seen_ms FROM machines WHERE account_id = ? AND id = ?`, t.acct, id).Scan(&meta, &m.LastSeenMs)
	if err != nil {
		return nil, err
	}
	m.Meta = unmarshalEnv(meta)
	return m, nil
}

var errOtherMachine = errors.New("session belongs to another machine")
var errNoMachine = errors.New("the machine has been removed")

// putSession creates the session with key, or updates its metadata, and
// with it when the session ended, unless a newer revision is stored. It
// returns the stored session. limit, if not nil, is checked against the
// account's usage before a new session is created.
func (t *tstore) putSession(ctx context.Context, machineID, sessionID string, key []byte, keyEpoch uint64, meta []byte, revision uint64, endedMs int64, limit func(accountUsage) error) (*pb.RemoteSession, error) {
	tx, err := t.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// The caller was authenticated before this transaction began, and its
	// machine may have been removed since, sessions and all. They must
	// not come back.
	var registered int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM machines WHERE account_id = ? AND id = ?`, t.acct, machineID).Scan(&registered); err != nil {
		return nil, err
	}
	if registered == 0 {
		return nil, errNoMachine
	}
	var owner string
	err = tx.QueryRowContext(ctx, `SELECT machine_id FROM sessions WHERE account_id = ? AND session_id = ?`, t.acct, sessionID).Scan(&owner)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if key == nil {
			return nil, errNoKey
		}
		if limit != nil {
			u, err := usageTx(ctx, tx, t.acct)
			if err != nil {
				return nil, err
			}
			u.sessions++
			if err := limit(u); err != nil {
				return nil, err
			}
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO sessions (account_id, session_id, machine_id, key, key_content_epoch, meta, meta_revision, updated_ms, ended_ms)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, t.acct, sessionID, machineID, key, keyEpoch, meta, revision, nowMs(), endedMs)
	case err != nil:
		return nil, err
	case owner != machineID:
		return nil, errOtherMachine
	default:
		// A recovery re-publish replaces the session key envelope when the
		// machine sends a newer content epoch; an older epoch never replaces
		// a newer one.
		if key != nil {
			if _, err = tx.ExecContext(ctx, `UPDATE sessions SET key = ?, key_content_epoch = ? WHERE account_id = ? AND session_id = ? AND key_content_epoch < ?`,
				key, keyEpoch, t.acct, sessionID, keyEpoch); err != nil {
				return nil, err
			}
		}
		if meta != nil {
			_, err = tx.ExecContext(ctx, `UPDATE sessions SET meta = ?, meta_revision = ?, updated_ms = ?, ended_ms = ?
				WHERE account_id = ? AND session_id = ? AND meta_revision <= ?`, meta, revision, nowMs(), endedMs, t.acct, sessionID, revision)
		}
	}
	if err != nil {
		return nil, err
	}
	rs, err := sessionTx(ctx, tx, t.acct, sessionID)
	if err != nil {
		return nil, err
	}
	return rs, tx.Commit()
}

var errNoKey = errors.New("a new session needs its key")

type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func sessionTx(ctx context.Context, q querier, acct, id string) (*pb.RemoteSession, error) {
	rs := &pb.RemoteSession{SessionId: id}
	var key, meta []byte
	err := q.QueryRowContext(ctx, `SELECT machine_id, key, meta, last_seq, updated_ms, ended_ms FROM sessions WHERE account_id = ? AND session_id = ?`, acct, id).
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
func (t *tstore) liveSessions(ctx context.Context) ([]*pb.RemoteSession, error) {
	rows, err := t.db.QueryContext(ctx, `SELECT `+sessionColumns+` FROM sessions WHERE account_id = ? AND ended_ms = 0 ORDER BY updated_ms DESC`, t.acct)
	if err != nil {
		return nil, err
	}
	return scanSessions(rows)
}

// endedSessions returns up to limit ended sessions after the cursor, from
// the latest ended to the earliest, and the cursor for the rest if there
// are more.
func (t *tstore) endedSessions(ctx context.Context, after *pb.SessionCursor, limit int) ([]*pb.RemoteSession, *pb.SessionCursor, error) {
	query := `SELECT ` + sessionColumns + ` FROM sessions WHERE account_id = ? AND ended_ms > 0`
	args := []any{t.acct}
	if after != nil {
		// The bound on ended_ms alone lets SQLite seek into the index to
		// the cursor; without it every page scans the ones before it.
		query += ` AND ended_ms <= ? AND (ended_ms < ? OR session_id > ?)`
		args = append(args, after.EndedMs, after.EndedMs, after.SessionId)
	}
	rows, err := t.db.QueryContext(ctx, query+` ORDER BY ended_ms DESC, session_id LIMIT ?`, append(args, limit+1)...)
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
func (t *tstore) sessionsByID(ctx context.Context, ids []string) ([]*pb.RemoteSession, error) {
	var out []*pb.RemoteSession
	for _, id := range ids {
		rs, err := sessionTx(ctx, t.db, t.acct, id)
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

// sessionMachine returns the machine a session belongs to.
func (t *tstore) sessionMachine(ctx context.Context, sessionID string) (string, error) {
	var owner string
	err := t.db.QueryRowContext(ctx, `SELECT machine_id FROM sessions WHERE account_id = ? AND session_id = ?`, t.acct, sessionID).Scan(&owner)
	return owner, err
}

func (t *tstore) sessionHeads(ctx context.Context, machineID string) ([]*pb.SessionHead, error) {
	rows, err := t.db.QueryContext(ctx, `SELECT session_id, last_seq, meta_revision, ended_ms FROM sessions WHERE account_id = ? AND machine_id = ?`, t.acct, machineID)
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
// last seq. limit, if not nil, is checked against the account's usage
// with the new events counted.
func (t *tstore) appendEvents(ctx context.Context, machineID, sessionID string, evs []*pb.StoredEvent, limit func(accountUsage) error) ([]*pb.StoredEvent, *pb.RemoteSession, error) {
	tx, err := t.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback()
	var owner string
	var last uint64
	err = tx.QueryRowContext(ctx, `SELECT machine_id, last_seq FROM sessions WHERE account_id = ? AND session_id = ?`, t.acct, sessionID).Scan(&owner, &last)
	if err != nil {
		return nil, nil, err
	}
	if owner != machineID {
		return nil, nil, errOtherMachine
	}
	var added []*pb.StoredEvent
	var bytes int64
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
		if _, err := tx.ExecContext(ctx, `INSERT INTO events (account_id, session_id, seq, envelope) VALUES (?, ?, ?, ?)`, t.acct, sessionID, e.Seq, b); err != nil {
			return nil, nil, err
		}
		last = e.Seq
		bytes += int64(len(b))
		added = append(added, e)
	}
	if len(added) > 0 {
		if limit != nil {
			u, err := usageTx(ctx, tx, t.acct)
			if err != nil {
				return nil, nil, err
			}
			u.eventBytes += bytes
			if err := limit(u); err != nil {
				return nil, nil, err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE sessions SET last_seq = ?, updated_ms = ? WHERE account_id = ? AND session_id = ?`, last, nowMs(), t.acct, sessionID); err != nil {
			return nil, nil, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE accounts SET event_bytes = event_bytes + ? WHERE id = ?`, bytes, t.acct); err != nil {
			return nil, nil, err
		}
	}
	rs, err := sessionTx(ctx, tx, t.acct, sessionID)
	if err != nil {
		return nil, nil, err
	}
	return added, rs, tx.Commit()
}

func (t *tstore) events(ctx context.Context, sessionID string, after uint64, limit int) ([]*pb.StoredEvent, bool, error) {
	rows, err := t.db.QueryContext(ctx, `SELECT seq, envelope FROM events WHERE account_id = ? AND session_id = ? AND seq > ? ORDER BY seq LIMIT ?`,
		t.acct, sessionID, after, limit+1)
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

// removeDevice deletes a device of the account and its logins. For a
// machine it also deletes its sessions and their events. It reports
// whether the device was a machine.
func (t *tstore) removeDevice(ctx context.Context, id string) (bool, error) {
	tx, err := t.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	machine, err := removeDeviceTx(ctx, tx, t.acct, id)
	if err != nil {
		return false, err
	}
	return machine, tx.Commit()
}

// removeDeviceTx is the removal used both by the old explicit path and when
// an accepted state drops a member. It keeps a tombstone, ends the device's
// logins and push targets, and for a machine deletes its sessions, events and
// images and recomputes the account's event bytes.
func removeDeviceTx(ctx context.Context, tx *sql.Tx, acct, id string) (bool, error) {
	// Tombstone and cancel admissions even when there is no device row: a
	// half-added member must never register with an old approval.
	if _, err := tx.ExecContext(ctx, `INSERT INTO revoked_devices (id, account_id, revoked_ms) VALUES (?, ?, ?) ON CONFLICT (id) DO NOTHING`, id, acct, nowMs()); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE pairing_admissions SET canceled_ms = 1 WHERE account_id = ? AND used_ms = 0`, acct); err != nil {
		return false, err
	}
	var kind int32
	err := tx.QueryRowContext(ctx, `SELECT kind FROM devices WHERE account_id = ? AND id = ?`, acct, id).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	stmts := []string{
		`DELETE FROM devices WHERE account_id = ?1 AND id = ?2`,
		`DELETE FROM tokens WHERE account_id = ?1 AND device_id = ?2`,
		`DELETE FROM push_subs WHERE account_id = ?1 AND device_id = ?2`,
		`DELETE FROM fcm_targets WHERE account_id = ?1 AND device_id = ?2`,
		`DELETE FROM events WHERE account_id = ?1 AND session_id IN (SELECT session_id FROM sessions WHERE account_id = ?1 AND machine_id = ?2)`,
		`DELETE FROM images WHERE account_id = ?1 AND machine_id = ?2`,
		`DELETE FROM sessions WHERE account_id = ?1 AND machine_id = ?2`,
		`DELETE FROM machines WHERE account_id = ?1 AND id = ?2`,
	}
	for _, q := range stmts {
		if _, err := tx.ExecContext(ctx, q, acct, id); err != nil {
			return false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE accounts SET event_bytes = (SELECT COALESCE(SUM(LENGTH(envelope)), 0) FROM events WHERE account_id = ?1) WHERE id = ?1`, acct); err != nil {
		return false, err
	}
	return pb.DeviceKind(kind) == pb.DeviceKind_DEVICE_KIND_MACHINE, nil
}
