package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"google.golang.org/protobuf/proto"
)

// recoveryDelay is how long a submitted recovery must wait before it may
// replace the authority. The old phone can veto during that window by
// signing any newer state.
const recoveryDelay = 48 * time.Hour

// recoveryChallengeTTL bounds how long a GetRecoveryContext signature stays
// valid.
const recoveryChallengeTTL = 5 * time.Minute

// recoveryRate bounds unauthenticated recovery lookups per address.
const (
	recoveryPerMinute = 120
	recoveryGlobal    = 600
)

type recoveryService struct {
	s *Server

	mu          sync.Mutex
	byAddr      map[string][]time.Time
	global      []time.Time
	challengeHi []byte
}

func newRecoveryService(s *Server) *recoveryService {
	hi := make([]byte, 32)
	if _, err := rand.Read(hi); err != nil {
		panic("security: no recovery challenge key: " + err.Error())
	}
	return &recoveryService{s: s, byAddr: map[string][]time.Time{}, challengeHi: hi}
}

func (rs *recoveryService) allow(addr string) bool {
	now := time.Now()
	rs.mu.Lock()
	defer rs.mu.Unlock()
	rs.global, _ = recent(rs.global, now, time.Minute, recoveryGlobal)
	rs.byAddr[addr], _ = recent(rs.byAddr[addr], now, time.Minute, recoveryPerMinute)
	for a, ts := range rs.byAddr {
		if len(ts) == 0 {
			delete(rs.byAddr, a)
		}
	}
	if len(rs.global) >= recoveryGlobal || len(rs.byAddr[addr]) >= recoveryPerMinute {
		return false
	}
	rs.global = append(rs.global, now)
	rs.byAddr[addr] = append(rs.byAddr[addr], now)
	return true
}

func (rs *recoveryService) challenge(now int64) []byte {
	b := make([]byte, 32)
	binary.BigEndian.PutUint64(b[:8], uint64(now+recoveryChallengeTTL.Milliseconds()))
	m := hmac.New(sha256.New, rs.challengeHi)
	m.Write([]byte("ra3/recovery-challenge\x00"))
	m.Write(b[:8])
	copy(b[8:], m.Sum(nil)[:24])
	return b
}

func (rs *recoveryService) checkChallenge(c []byte, now int64) bool {
	if len(c) != 32 {
		return false
	}
	exp := int64(binary.BigEndian.Uint64(c[:8]))
	if exp < now || exp > now+recoveryChallengeTTL.Milliseconds() {
		return false
	}
	m := hmac.New(sha256.New, rs.challengeHi)
	m.Write([]byte("ra3/recovery-challenge\x00"))
	m.Write(c[:8])
	return hmac.Equal(c[8:], m.Sum(nil)[:24])
}

// recoveryContextBytes is what GetRecoveryContext's caller signs.
func recoveryContextBytes(challenge, recoveryEd []byte, recoveryID string) []byte {
	b := append([]byte("ra3/recovery-context\x00"), challenge...)
	var ed [32]byte
	copy(ed[:], recoveryEd)
	b = append(b, ed[:]...)
	return append(b, recoveryID...)
}

type recoveryRow struct {
	account    string
	recoveryID string
	statement  deviceauth.RecoveryStatement
	state      deviceauth.State
	submitted  int64
	ready      int64
	completed  int64
	rejected   int64
}

const recoveryColumns = `account_id, recovery_id, statement, state, submitted_ms, ready_ms, completed_ms, rejected_ms`

func scanRecovery(sc interface{ Scan(...any) error }) (*recoveryRow, error) {
	var stmtRaw, stateRaw []byte
	r := &recoveryRow{}
	err := sc.Scan(&r.account, &r.recoveryID, &stmtRaw, &stateRaw, &r.submitted, &r.ready, &r.completed, &r.rejected)
	if err != nil {
		return nil, err
	}
	st, err := deviceauth.ParseRecoveryStatement(stmtRaw)
	if err != nil {
		return nil, err
	}
	s, err := deviceauth.ParseState(stateRaw)
	if err != nil {
		return nil, err
	}
	r.statement, r.state = st, s
	return r, nil
}

func (rs *recoveryService) rowByID(ctx context.Context, id string) (*recoveryRow, error) {
	return scanRecovery(rs.s.store.db.QueryRowContext(ctx, `SELECT `+recoveryColumns+` FROM security_recovery WHERE recovery_id = ? UNION ALL SELECT `+recoveryColumns+` FROM security_recovery_completed WHERE recovery_id = ? LIMIT 1`, id, id))
}

func (rs *recoveryService) headHash(ctx context.Context, account string) (deviceauth.Hash, bool) {
	var raw []byte
	if err := rs.s.store.db.QueryRowContext(ctx, `SELECT state FROM security_states WHERE account_id = ? ORDER BY epoch DESC LIMIT 1`, account).Scan(&raw); err != nil {
		return deviceauth.Hash{}, false
	}
	head, err := deviceauth.ParseState(raw)
	if err != nil {
		return deviceauth.Hash{}, false
	}
	h, err := head.Hash()
	if err != nil {
		return deviceauth.Hash{}, false
	}
	return h, true
}

// pendingByAccount returns the row a recovery-key holder may act on: a row
// that is neither completed nor rejected and whose statement still names the
// current head as its parent. A veto or a completed recovery therefore leaves
// no pending row, and a new code may submit again.
func (rs *recoveryService) pendingByAccount(ctx context.Context, account string) (*recoveryRow, error) {
	head, ok := rs.headHash(ctx, account)
	if !ok {
		return nil, errNoRows
	}
	r, err := scanRecovery(rs.s.store.db.QueryRowContext(ctx, `SELECT `+recoveryColumns+` FROM security_recovery WHERE account_id = ? AND completed_ms = 0 AND rejected_ms = 0`, account))
	if err != nil {
		return nil, err
	}
	if r.statement.Parent != head {
		return nil, errNoRows
	}
	return r, nil
}

func (rs *recoveryService) accountByRecoveryKey(ctx context.Context, ed []byte) (string, []byte, error) {
	var account string
	var publicKey []byte
	err := rs.s.store.db.QueryRowContext(ctx, `SELECT id, public_key FROM accounts WHERE recovery_ed = ? LIMIT 1`, ed).Scan(&account, &publicKey)
	return account, publicKey, err
}

func (rs *recoveryService) chain(ctx context.Context, account string) ([]*pb.SecurityState, error) {
	rows, err := rs.s.store.db.QueryContext(ctx, `SELECT state, recovery FROM security_states WHERE account_id = ? ORDER BY epoch`, account)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*pb.SecurityState
	for rows.Next() {
		var state, rec []byte
		if err := rows.Scan(&state, &rec); err != nil {
			return nil, err
		}
		out = append(out, &pb.SecurityState{State: state, Recovery: rec})
	}
	return out, rows.Err()
}

func (rs *recoveryService) GetRecoveryChallenge(ctx context.Context, req *connect.Request[pb.GetRecoveryChallengeRequest]) (*connect.Response[pb.GetRecoveryChallengeResponse], error) {
	if !rs.allow(rs.s.clientAddr(req.Peer().Addr, req.Header())) {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("too many recovery lookups; try again in a minute"))
	}
	return connect.NewResponse(&pb.GetRecoveryChallengeResponse{Challenge: rs.challenge(nowMs())}), nil
}

func (rs *recoveryService) GetRecoveryContext(ctx context.Context, req *connect.Request[pb.GetRecoveryContextRequest]) (*connect.Response[pb.GetRecoveryContextResponse], error) {
	if !rs.allow(rs.s.clientAddr(req.Peer().Addr, req.Header())) {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("too many recovery lookups; try again in a minute"))
	}
	if !rs.checkChallenge(req.Msg.Challenge, nowMs()) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("fetch a fresh recovery challenge and sign it"))
	}
	var r *recoveryRow
	var account string
	var accountKey []byte
	if id := req.Msg.GetRecoveryId(); id != "" {
		row, err := rs.rowByID(ctx, id)
		if errors.Is(err, errNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("no recovery is pending for that key"))
		}
		if err != nil {
			return nil, internal(err)
		}
		key := row.statement.NewMember.EdPublic[:]
		if len(req.Msg.Signature) != ed25519.SignatureSize || !ed25519.Verify(key, recoveryContextBytes(req.Msg.Challenge, nil, id), req.Msg.Signature) {
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("bad recovery context signature"))
		}
		acct, err := rs.s.store.account(ctx, row.account)
		if err != nil {
			return nil, internal(err)
		}
		if row.completed != 0 {
			head, _, err := rs.s.store.tenant(row.account).securityHead(ctx)
			if err != nil {
				return nil, internal(err)
			}
			if _, ok := head.Core.Member(row.statement.NewMember.ID); !ok {
				return nil, connect.NewError(connect.CodePermissionDenied, errors.New("the recovered device is no longer a member"))
			}
		}
		r, account, accountKey = row, row.account, acct.publicKey
	} else {
		ed := req.Msg.GetRecoveryEd()
		if len(ed) != ed25519.PublicKeySize {
			return nil, connect.NewError(connect.CodeNotFound, errors.New("no recovery is pending for that key"))
		}
		if len(req.Msg.Signature) != ed25519.SignatureSize || !ed25519.Verify(ed, recoveryContextBytes(req.Msg.Challenge, ed, ""), req.Msg.Signature) {
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("bad recovery context signature"))
		}
		acctID, publicKey, err := rs.accountByRecoveryKey(ctx, ed)
		switch {
		case err == nil:
			account, accountKey = acctID, publicKey
			pending, perr := rs.pendingByAccount(ctx, acctID)
			if perr == nil {
				r = pending
			} else if !errors.Is(perr, errNoRows) {
				return nil, internal(perr)
			}
		case errors.Is(err, errNoRows):
			// The new recovery key only exists in a pending statement until
			// the head changes; a retry after losing the response finds it
			// by that key, still only when its parent is the current head.
			pending, perr := rs.pendingByNewKey(ctx, ed)
			if perr == nil {
				acct, aerr := rs.s.store.account(ctx, pending.account)
				if aerr != nil {
					return nil, internal(aerr)
				}
				r, account, accountKey = pending, pending.account, acct.publicKey
			} else if !errors.Is(perr, errNoRows) {
				return nil, internal(perr)
			} else {
				return nil, connect.NewError(connect.CodeNotFound, errors.New("no recovery is pending for that key"))
			}
		default:
			return nil, internal(err)
		}
	}
	chain, err := rs.chain(ctx, account)
	if err != nil {
		return nil, internal(err)
	}
	res := &pb.GetRecoveryContextResponse{AccountId: account, States: chain, AccountPublicKey: accountKey}
	if r != nil {
		res.ReadyMs = uint64(max(r.ready, 0))
		res.RecoveryId = r.recoveryID
		switch {
		case r.rejected != 0:
			res.Phase = pb.GetRecoveryContextResponse_PHASE_REJECTED
		case r.completed != 0:
			res.Phase = pb.GetRecoveryContextResponse_PHASE_PUBLISHED
		case nowMs() < r.ready:
			res.Phase = pb.GetRecoveryContextResponse_PHASE_PENDING
		default:
			res.Phase = pb.GetRecoveryContextResponse_PHASE_READY
		}
	}
	return connect.NewResponse(res), nil
}

// pendingByNewKey finds a row by the recovery key it would install, without
// scanning pending rows: the key is indexed.
func (rs *recoveryService) pendingByNewKey(ctx context.Context, ed []byte) (*recoveryRow, error) {
	r, err := scanRecovery(rs.s.store.db.QueryRowContext(ctx, `SELECT `+recoveryColumns+` FROM security_recovery WHERE new_recovery_ed = ? AND completed_ms = 0 AND rejected_ms = 0`, ed))
	if err != nil {
		return nil, err
	}
	head, ok := rs.headHash(ctx, r.account)
	if !ok || r.statement.Parent != head {
		return nil, errNoRows
	}
	return r, nil
}

func (rs *recoveryService) SubmitRecovery(ctx context.Context, req *connect.Request[pb.SubmitRecoveryRequest]) (*connect.Response[pb.SubmitRecoveryResponse], error) {
	if !rs.allow(rs.s.clientAddr(req.Peer().Addr, req.Header())) {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("too many recovery submissions; try again in a minute"))
	}
	stmt, err := deviceauth.ParseRecoveryStatement(req.Msg.Statement)
	if err != nil {
		return nil, invalid("invalid recovery statement")
	}
	state, err := deviceauth.ParseState(req.Msg.State)
	if err != nil || state.Core.IssuedAt > uint64(nowMs())+deviceauth.MaxClockSkew {
		return nil, invalid("invalid recovery state")
	}
	acct, err := rs.s.store.account(ctx, stmt.Account)
	if errors.Is(err, errNoRows) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("unknown account"))
	}
	if err != nil {
		return nil, internal(err)
	}
	if acct.status != accountActive {
		return nil, connect.NewError(connect.CodePermissionDenied, errSuspended)
	}
	stmtRaw, _ := stmt.MarshalBinary()
	stateRaw, _ := state.MarshalBinary()
	t := rs.s.store.tenant(stmt.Account)
	// The head read, validation and pending-row decision are one transaction,
	// so a concurrent veto cannot be overwritten by a stale submission.
	tx, err := t.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, internal(err)
	}
	defer tx.Rollback()
	existing, err := scanRecovery(tx.QueryRowContext(ctx, `SELECT `+recoveryColumns+` FROM security_recovery WHERE account_id = ?`, stmt.Account))
	if err != nil && !errors.Is(err, errNoRows) {
		return nil, internal(err)
	}
	// An exact retry is answered before checking the head: after completion
	// its parent is the previous head, but the caller must still learn the
	// same recovery ID. A vetoed row is not revived this way.
	if err == nil && existing.rejected == 0 && bytes.Equal(reqStatementRaw(existing.statement), stmtRaw) && bytes.Equal(reqStateRaw(existing.state), stateRaw) {
		return connect.NewResponse(&pb.SubmitRecoveryResponse{RecoveryId: existing.recoveryID, ReadyMs: uint64(max(existing.ready, 0))}), nil
	}
	var headRaw []byte
	if err := tx.QueryRowContext(ctx, `SELECT state FROM security_states WHERE account_id = ? ORDER BY epoch DESC LIMIT 1`, stmt.Account).Scan(&headRaw); errors.Is(err, errNoRows) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the account has no security domain"))
	} else if err != nil {
		return nil, internal(err)
	}
	head, err := deviceauth.ParseState(headRaw)
	if err != nil {
		return nil, internal(err)
	}
	if err := deviceauth.VerifyTransition(head, state, &stmt); err != nil {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("the recovery statement does not match the current head"))
	}
	if err := checkRecoveryEpoch(state.Core.Epoch); err != nil {
		return nil, err
	}
	// A completed or rejected row may be overwritten: a second recovery with
	// the phone's current code is a new request. A rejected statement whose
	// parent is no longer the head fails VerifyTransition above and cannot be
	// revived.
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return nil, internal(err)
	}
	recoveryID := hex.EncodeToString(id)
	ready := nowMs() + recoveryDelay.Milliseconds()
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO security_recovery_completed (`+recoveryColumns+`) SELECT `+recoveryColumns+` FROM security_recovery WHERE account_id = ? AND completed_ms != 0`, stmt.Account); err != nil {
		return nil, internal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO security_recovery (account_id, recovery_id, statement, state, new_recovery_ed, submitted_ms, ready_ms) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (account_id) DO UPDATE SET recovery_id = excluded.recovery_id, statement = excluded.statement, state = excluded.state, new_recovery_ed = excluded.new_recovery_ed, submitted_ms = excluded.submitted_ms, ready_ms = excluded.ready_ms, completed_ms = 0, rejected_ms = 0`,
		stmt.Account, recoveryID, stmtRaw, stateRaw, stmt.NewRecoveryEd[:], nowMs(), ready); err != nil {
		return nil, internal(err)
	}
	authority := head.Core.AuthorityID
	if err := tx.Commit(); err != nil {
		return nil, internal(err)
	}
	// Generic text only; the push never signs anything. The current
	// authority phone is the only recipient.
	_ = rs.s.push.notifyDevice(t, authority, "", pb.NotifyKind_NOTIFY_KIND_SECURITY, nil, "security/"+recoveryID)
	return connect.NewResponse(&pb.SubmitRecoveryResponse{RecoveryId: recoveryID, ReadyMs: uint64(ready)}), nil
}

func (rs *recoveryService) CompleteRecovery(ctx context.Context, req *connect.Request[pb.CompleteRecoveryRequest]) (*connect.Response[pb.CompleteRecoveryResponse], error) {
	if !rs.allow(rs.s.clientAddr(req.Peer().Addr, req.Header())) {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("too many recovery requests; try again in a minute"))
	}
	// The pending row and the head are read inside one transaction, and every
	// side effect happens only after the recovery state insert succeeded.
	return rs.completeRecovery(ctx, req)
}

func (rs *recoveryService) completeRecovery(ctx context.Context, req *connect.Request[pb.CompleteRecoveryRequest]) (*connect.Response[pb.CompleteRecoveryResponse], error) {
	row, err := rs.rowByID(ctx, req.Msg.RecoveryId)
	if errors.Is(err, errNoRows) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such recovery"))
	}
	if err != nil {
		return nil, internal(err)
	}
	label := append([]byte("ra3/recovery-complete\x00"), []byte(req.Msg.RecoveryId)...)
	if len(req.Msg.Signature) != ed25519.SignatureSize || !ed25519.Verify(row.statement.NewMember.EdPublic[:], label, req.Msg.Signature) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("bad recovery signature"))
	}
	acct, err := rs.s.store.account(ctx, row.account)
	if err != nil {
		return nil, internal(err)
	}
	if row.rejected != 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the recovery was vetoed"))
	}
	if row.completed != 0 {
		// The device is already registered. Never mint a token from this
		// replayable signature; the phone logs in with a fresh challenge.
		return connect.NewResponse(&pb.CompleteRecoveryResponse{DeviceId: row.statement.NewAuthorityID, AccountPublicKey: acct.publicKey}), nil
	}
	if nowMs() < row.ready {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the recovery is not ready yet"))
	}
	if row.state.Core.IssuedAt > uint64(nowMs())+deviceauth.MaxClockSkew {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the recovery state's clock is ahead"))
	}
	t := rs.s.store.tenant(row.account)
	tx, err := t.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, internal(err)
	}
	defer tx.Rollback()
	// The row may have changed since the check above; re-read it in the
	// transaction and refuse if the veto won.
	var completed, rejected int64
	if err := tx.QueryRowContext(ctx, `SELECT completed_ms, rejected_ms FROM security_recovery WHERE recovery_id = ?`, req.Msg.RecoveryId).Scan(&completed, &rejected); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the recovery is stale"))
	}
	if rejected != 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the recovery was vetoed"))
	}
	if completed != 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the recovery is stale"))
	}
	var headRaw []byte
	if err := tx.QueryRowContext(ctx, `SELECT state FROM security_states WHERE account_id = ? ORDER BY epoch DESC LIMIT 1`, row.account).Scan(&headRaw); err != nil {
		return nil, internal(err)
	}
	head, err := deviceauth.ParseState(headRaw)
	if err != nil {
		return nil, internal(err)
	}
	headHash, _ := head.Hash()
	verifyErr := deviceauth.VerifyTransition(head, row.state, &row.statement)
	if row.statement.Parent != headHash || verifyErr != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE security_recovery SET rejected_ms = ? WHERE recovery_id = ? AND completed_ms = 0`, nowMs(), req.Msg.RecoveryId); err != nil {
			return nil, internal(err)
		}
		if err := tx.Commit(); err != nil {
			return nil, internal(err)
		}
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the recovery is stale"))
	}
	if err := checkRecoveryEpoch(row.state.Core.Epoch); err != nil {
		return nil, err
	}
	h, _ := row.state.Hash()
	res, err := tx.ExecContext(ctx, `INSERT INTO security_states (account_id, epoch, state, hash, recovery) VALUES (?, ?, ?, ?, ?) ON CONFLICT (account_id, epoch) DO NOTHING`,
		row.account, row.state.Core.Epoch, reqStateRaw(row.state), h[:], reqStatementRaw(row.statement))
	if err != nil {
		return nil, internal(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		var stored []byte
		if err := tx.QueryRowContext(ctx, `SELECT state FROM security_states WHERE account_id = ? AND epoch = ?`, row.account, row.state.Core.Epoch).Scan(&stored); err != nil || !bytes.Equal(stored, reqStateRaw(row.state)) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the recovery is stale"))
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE accounts SET recovery_ed = ? WHERE id = ?`, row.state.Core.RecoveryEd[:], row.account); err != nil {
		return nil, internal(err)
	}
	for _, old := range head.Core.Members {
		if _, ok := row.state.Core.Member(old.ID); !ok {
			if _, err := removeDeviceTx(ctx, tx, row.account, old.ID); err != nil {
				return nil, internal(err)
			}
		}
	}
	if err := registerRecoveredDevice(ctx, tx, row.account, row.statement.NewMember); err != nil {
		return nil, internal(err)
	}
	res, err = tx.ExecContext(ctx, `UPDATE security_recovery SET completed_ms = ? WHERE recovery_id = ? AND completed_ms = 0 AND rejected_ms = 0`,
		nowMs(), req.Msg.RecoveryId)
	if err != nil {
		return nil, internal(err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the recovery is stale"))
	}
	// The old authority is gone: no pairing it created may still complete.
	if _, err := tx.ExecContext(ctx, `UPDATE pairing_admissions SET canceled_ms = ? WHERE account_id = ? AND used_ms = 0 AND canceled_ms = 0`, nowMs(), row.account); err != nil {
		return nil, internal(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, internal(err)
	}
	rs.s.pairing.mu.Lock()
	for _, p := range rs.s.pairing.byID {
		if p.account == row.account && !p.delivered {
			p.canceled = true
			p.payload = nil
			delete(rs.s.pairing.byCode, p.code)
		}
	}
	rs.s.pairing.mu.Unlock()
	for _, old := range head.Core.Members {
		if _, ok := row.state.Core.Member(old.ID); !ok {
			rs.s.revokeStreams(old.ID)
			rs.s.hub.account(row.account).drop(old.ID)
		}
	}
	return connect.NewResponse(&pb.CompleteRecoveryResponse{DeviceId: row.statement.NewAuthorityID, AccountPublicKey: acct.publicKey}), nil
}

func checkRecoveryEpoch(epoch uint64) error {
	if epoch > deviceauth.MaxStates {
		return connect.NewError(connect.CodeResourceExhausted, errors.New("security chain is full"))
	}
	return nil
}

func reqStateRaw(s deviceauth.State) []byte {
	b, _ := s.MarshalBinary()
	return b
}
func reqStatementRaw(s deviceauth.RecoveryStatement) []byte {
	b, _ := s.MarshalBinary()
	return b
}

func registerRecoveredDevice(ctx context.Context, tx *sql.Tx, account string, m deviceauth.Member) error {
	var revoked int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM revoked_devices WHERE id = ?`, m.ID).Scan(&revoked); err != nil {
		return err
	}
	if revoked != 0 {
		return errDeviceRevoked
	}
	reg, _ := proto.Marshal(&pb.DeviceRegistration{DevicePublicKey: m.EdPublic[:], Kind: pb.DeviceKind_DEVICE_KIND_CLIENT, CreatedMs: nowMs()})
	var owner string
	err := tx.QueryRowContext(ctx, `SELECT account_id FROM devices WHERE id = ?`, m.ID).Scan(&owner)
	switch {
	case err == nil && owner != account:
		return errOtherAccount
	case err == nil:
		if _, err := tx.ExecContext(ctx, `DELETE FROM tokens WHERE account_id = ? AND device_id = ?`, account, m.ID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE devices SET public_key = ?, kind = ?, registration = ?, signature = ? WHERE account_id = ? AND id = ?`,
			m.EdPublic[:], int32(pb.DeviceKind_DEVICE_KIND_CLIENT), reg, []byte{}, account, m.ID)
		if err == nil {
			_, err = tx.ExecContext(ctx, `DELETE FROM machines WHERE account_id = ? AND id = ?`, account, m.ID)
		}
		return err
	case errors.Is(err, sql.ErrNoRows):
		_, err = tx.ExecContext(ctx, `INSERT INTO devices (id, account_id, public_key, kind, registration, signature, created_ms) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			m.ID, account, m.EdPublic[:], int32(pb.DeviceKind_DEVICE_KIND_CLIENT), reg, []byte{}, nowMs())
		return err
	default:
		return err
	}
}
