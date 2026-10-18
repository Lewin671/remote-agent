package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
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

// recoveryRate bounds unauthenticated recovery lookups per address.
const (
	recoveryPerMinute = 120
	recoveryGlobal    = 600
)

type recoveryService struct {
	s *Server

	mu     sync.Mutex
	byAddr map[string][]time.Time
	global []time.Time
}

func newRecoveryService(s *Server) *recoveryService {
	return &recoveryService{s: s, byAddr: map[string][]time.Time{}}
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

type recoveryRow struct {
	account    string
	recoveryID string
	statement  deviceauth.RecoveryStatement
	state      deviceauth.State
	submitted  int64
	ready      int64
	completed  int64
	rejected   int64
	deviceID   string
}

func scanRecovery(sc interface{ Scan(...any) error }) (*recoveryRow, error) {
	var stmtRaw, stateRaw []byte
	r := &recoveryRow{}
	err := sc.Scan(&r.account, &r.recoveryID, &stmtRaw, &stateRaw, &r.submitted, &r.ready, &r.completed, &r.rejected, &r.deviceID)
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

func (rs *recoveryService) row(ctx context.Context, req *pb.GetRecoveryContextRequest) (*recoveryRow, error) {
	if id := req.GetRecoveryId(); id != "" {
		return scanRecovery(rs.s.store.db.QueryRowContext(ctx, `SELECT account_id, recovery_id, statement, state, submitted_ms, ready_ms, completed_ms, rejected_ms, device_id FROM security_recovery WHERE recovery_id = ?`, id))
	}
	if len(req.GetRecoveryEd()) != 32 {
		return nil, errNoRows
	}
	var account string
	err := rs.s.store.db.QueryRowContext(ctx, `SELECT id FROM accounts WHERE recovery_ed = ?`, req.GetRecoveryEd()).Scan(&account)
	if err != nil {
		// The new key is only in a pending statement until the head changes.
		rows, qerr := rs.s.store.db.QueryContext(ctx, `SELECT account_id, recovery_id, statement, state, submitted_ms, ready_ms, completed_ms, rejected_ms, device_id FROM security_recovery WHERE completed_ms = 0 AND rejected_ms = 0`)
		if qerr != nil {
			return nil, qerr
		}
		defer rows.Close()
		for rows.Next() {
			r, e := scanRecovery(rows)
			if e != nil {
				return nil, e
			}
			if bytes.Equal(r.statement.NewRecoveryEd[:], req.GetRecoveryEd()) {
				return r, nil
			}
		}
		return nil, errNoRows
	}
	return scanRecovery(rs.s.store.db.QueryRowContext(ctx, `SELECT account_id, recovery_id, statement, state, submitted_ms, ready_ms, completed_ms, rejected_ms, device_id FROM security_recovery WHERE account_id = ?`, account))
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

func (rs *recoveryService) GetRecoveryContext(ctx context.Context, req *connect.Request[pb.GetRecoveryContextRequest]) (*connect.Response[pb.GetRecoveryContextResponse], error) {
	if !rs.allow(rs.s.clientAddr(req.Peer().Addr, req.Header())) {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("too many recovery lookups; try again in a minute"))
	}
	r, err := rs.row(ctx, req.Msg)
	if errors.Is(err, errNoRows) {
		// A valid recovery key may be looked up before anything is
		// submitted: the new phone needs the chain to build its statement.
		if len(req.Msg.RecoveryEd) == 32 {
			var account string
			var publicKey []byte
			e := rs.s.store.db.QueryRowContext(ctx, `SELECT id, public_key FROM accounts WHERE recovery_ed = ?`, req.Msg.RecoveryEd).Scan(&account, &publicKey)
			if e == nil {
				chain, e := rs.chain(ctx, account)
				if e != nil {
					return nil, internal(e)
				}
				return connect.NewResponse(&pb.GetRecoveryContextResponse{AccountId: account, States: chain, AccountPublicKey: publicKey}), nil
			}
		}
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no recovery is pending for that key"))
	}
	if err != nil {
		return nil, internal(err)
	}
	acct, err := rs.s.store.account(ctx, r.account)
	if err != nil {
		return nil, internal(err)
	}
	chain, err := rs.chain(ctx, r.account)
	if err != nil {
		return nil, internal(err)
	}
	res := &pb.GetRecoveryContextResponse{AccountId: r.account, States: chain, ReadyMs: uint64(max(r.ready, 0)), RecoveryId: r.recoveryID, AccountPublicKey: acct.publicKey}
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
	return connect.NewResponse(res), nil
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
	existing, err := scanRecovery(tx.QueryRowContext(ctx, `SELECT account_id, recovery_id, statement, state, submitted_ms, ready_ms, completed_ms, rejected_ms, device_id FROM security_recovery WHERE account_id = ?`, stmt.Account))
	if err == nil {
		switch {
		case existing.completed != 0:
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("authority recovery already completed"))
		case existing.statement.Parent == stmt.Parent && bytes.Equal(reqStatementRaw(existing.statement), stmtRaw) && bytes.Equal(reqStateRaw(existing.state), stateRaw):
			// An exact retry keeps the original 48-hour clock.
			return connect.NewResponse(&pb.SubmitRecoveryResponse{RecoveryId: existing.recoveryID, ReadyMs: uint64(max(existing.ready, 0))}), nil
		default:
			// The same parent with a different new phone (S12), or a pending
			// row for a parent the head has already moved past: replace it.
		}
	} else if !errors.Is(err, errNoRows) {
		return nil, internal(err)
	}
	id := make([]byte, 16)
	if _, err := rand.Read(id); err != nil {
		return nil, internal(err)
	}
	recoveryID := hex.EncodeToString(id)
	ready := nowMs() + recoveryDelay.Milliseconds()
	if _, err := tx.ExecContext(ctx, `INSERT INTO security_recovery (account_id, recovery_id, statement, state, submitted_ms, ready_ms) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (account_id) DO UPDATE SET recovery_id = excluded.recovery_id, statement = excluded.statement, state = excluded.state, submitted_ms = excluded.submitted_ms, ready_ms = excluded.ready_ms, completed_ms = 0, rejected_ms = 0, device_id = '', token_hash = NULL`,
		stmt.Account, recoveryID, stmtRaw, stateRaw, nowMs(), ready); err != nil {
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
	row, err := scanRecovery(rs.s.store.db.QueryRowContext(ctx, `SELECT account_id, recovery_id, statement, state, submitted_ms, ready_ms, completed_ms, rejected_ms, device_id FROM security_recovery WHERE recovery_id = ?`, req.Msg.RecoveryId))
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
	if row.rejected != 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the recovery was vetoed"))
	}
	if row.completed != 0 {
		token, err := rs.issueToken(ctx, row.account, row.statement.NewAuthorityID)
		if err != nil {
			return nil, internal(err)
		}
		acct, err := rs.s.store.account(ctx, row.account)
		if err != nil {
			return nil, internal(err)
		}
		return connect.NewResponse(&pb.CompleteRecoveryResponse{DeviceId: row.deviceID, Token: token, AccountPublicKey: acct.publicKey}), nil
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
	token := newToken()
	expires := time.Now().Add(rs.s.cfg.TokenTTL).UnixMilli()
	if _, err := tx.ExecContext(ctx, `INSERT INTO tokens (hash, account_id, device_id, expires_ms) VALUES (?, ?, ?, ?)`, hashToken(token), row.account, row.statement.NewAuthorityID, expires); err != nil {
		return nil, internal(err)
	}
	res, err = tx.ExecContext(ctx, `UPDATE security_recovery SET completed_ms = ?, device_id = ?, token_hash = ? WHERE recovery_id = ? AND completed_ms = 0 AND rejected_ms = 0`,
		nowMs(), row.statement.NewAuthorityID, hashToken(token), req.Msg.RecoveryId)
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
	acct, err := rs.s.store.account(ctx, row.account)
	if err != nil {
		return nil, internal(err)
	}
	for _, old := range head.Core.Members {
		if _, ok := row.state.Core.Member(old.ID); !ok {
			rs.s.revokeStreams(old.ID)
			rs.s.hub.account(row.account).drop(old.ID)
		}
	}
	return connect.NewResponse(&pb.CompleteRecoveryResponse{DeviceId: row.statement.NewAuthorityID, Token: token, AccountPublicKey: acct.publicKey}), nil
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

// issueToken gives a completed recovery another bearer token for the device.
func (rs *recoveryService) issueToken(ctx context.Context, account, deviceID string) (string, error) {
	token := newToken()
	expires := time.Now().Add(rs.s.cfg.TokenTTL).UnixMilli()
	t := rs.s.store.tenant(account)
	if err := t.addToken(ctx, hashToken(token), deviceID, expires); err != nil {
		return "", err
	}
	return token, nil
}
