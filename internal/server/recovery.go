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
	if !rs.allow(req.Peer().Addr) {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("too many recovery lookups; try again in a minute"))
	}
	r, err := rs.row(ctx, req.Msg)
	if errors.Is(err, errNoRows) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no recovery is pending for that key"))
	}
	if err != nil {
		return nil, internal(err)
	}
	chain, err := rs.chain(ctx, r.account)
	if err != nil {
		return nil, internal(err)
	}
	res := &pb.GetRecoveryContextResponse{AccountId: r.account, States: chain, ReadyMs: uint64(max(r.ready, 0)), RecoveryId: r.recoveryID}
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
	if !rs.allow(req.Peer().Addr) {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("too many recovery submissions; try again in a minute"))
	}
	stmt, err := deviceauth.ParseRecoveryStatement(req.Msg.Statement)
	if err != nil {
		return nil, invalid("invalid recovery statement")
	}
	state, err := deviceauth.ParseState(req.Msg.State)
	if err != nil {
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
	t := rs.s.store.tenant(stmt.Account)
	head, _, err := t.securityHead(ctx)
	if errors.Is(err, errNoRows) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the account has no security domain"))
	}
	if err != nil {
		return nil, internal(err)
	}
	if err := deviceauth.VerifyTransition(head, state, &stmt); err != nil {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("the recovery statement does not match the current head"))
	}
	stmtRaw, _ := stmt.MarshalBinary()
	stateRaw, _ := state.MarshalBinary()
	tx, err := t.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, internal(err)
	}
	defer tx.Rollback()
	existing, err := scanRecovery(tx.QueryRowContext(ctx, `SELECT account_id, recovery_id, statement, state, submitted_ms, ready_ms, completed_ms, rejected_ms, device_id FROM security_recovery WHERE account_id = ?`, stmt.Account))
	if err == nil {
		switch {
		case existing.completed != 0:
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("authority recovery already completed"))
		case existing.rejected == 0:
			// A pending recovery keeps its 48-hour clock; a second
			// submission for the same head does not restart it.
			if existing.statement.Parent == stmt.Parent {
				return connect.NewResponse(&pb.SubmitRecoveryResponse{RecoveryId: existing.recoveryID, ReadyMs: uint64(max(existing.ready, 0))}), nil
			}
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("another recovery is pending"))
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
	if err := tx.Commit(); err != nil {
		return nil, internal(err)
	}
	// Generic text only; the push never signs anything.
	_ = rs.s.push.notify(t, "", pb.NotifyKind_NOTIFY_KIND_SECURITY, nil, "security/"+recoveryID)
	return connect.NewResponse(&pb.SubmitRecoveryResponse{RecoveryId: recoveryID, ReadyMs: uint64(ready)}), nil
}

func (rs *recoveryService) CompleteRecovery(ctx context.Context, req *connect.Request[pb.CompleteRecoveryRequest]) (*connect.Response[pb.CompleteRecoveryResponse], error) {
	if !rs.allow(req.Peer().Addr) {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("too many recovery requests; try again in a minute"))
	}
	r, err := scanRecovery(rs.s.store.db.QueryRowContext(ctx, `SELECT account_id, recovery_id, statement, state, submitted_ms, ready_ms, completed_ms, rejected_ms, device_id FROM security_recovery WHERE recovery_id = ?`, req.Msg.RecoveryId))
	if errors.Is(err, errNoRows) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such recovery"))
	}
	if err != nil {
		return nil, internal(err)
	}
	label := append([]byte("ra3/recovery-complete\x00"), []byte(req.Msg.RecoveryId)...)
	if len(req.Msg.Signature) != ed25519.SignatureSize || !ed25519.Verify(r.statement.NewMember.EdPublic[:], label, req.Msg.Signature) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("bad recovery signature"))
	}
	if r.rejected != 0 {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the recovery was vetoed"))
	}
	if r.completed != 0 {
		// Retried after a lost response: the device exists, issue a fresh
		// login token rather than a second registration.
		token, err := rs.issueToken(ctx, r.account, r.statement.NewAuthorityID)
		if err != nil {
			return nil, internal(err)
		}
		return connect.NewResponse(&pb.CompleteRecoveryResponse{DeviceId: r.deviceID, Token: token}), nil
	}
	if nowMs() < r.ready {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the recovery is not ready yet"))
	}
	t := rs.s.store.tenant(r.account)
	head, headHash, err := t.securityHead(ctx)
	if err != nil {
		return nil, internal(err)
	}
	verifyErr := deviceauth.VerifyTransition(head, r.state, &r.statement)
	if r.statement.Parent != headHash || verifyErr != nil {
		// The old phone published a newer state: the recovery is stale.
		_, _ = t.db.ExecContext(ctx, `UPDATE security_recovery SET rejected_ms = ? WHERE recovery_id = ? AND completed_ms = 0`, nowMs(), req.Msg.RecoveryId)
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the recovery is stale"))
	}
	if r.state.Core.Parent != headHash {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the recovery is stale"))
	}
	tx, err := t.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, internal(err)
	}
	defer tx.Rollback()
	h, _ := r.state.Hash()
	if _, err := tx.ExecContext(ctx, `INSERT INTO security_states (account_id, epoch, state, hash, recovery) VALUES (?, ?, ?, ?, ?) ON CONFLICT (account_id, epoch) DO NOTHING`,
		r.account, r.state.Core.Epoch, reqStateRaw(r.state), h[:], reqStatementRaw(r.statement)); err != nil {
		return nil, internal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE accounts SET recovery_ed = ? WHERE id = ?`, r.state.Core.RecoveryEd[:], r.account); err != nil {
		return nil, internal(err)
	}
	// The recovery transition must have removed the old authority; revoke it
	// in the same transaction.
	for _, old := range head.Core.Members {
		if _, ok := r.state.Core.Member(old.ID); !ok {
			if _, err := removeDeviceTx(ctx, tx, r.account, old.ID); err != nil && !errors.Is(err, errNoRows) {
				return nil, internal(err)
			}
		}
	}
	if err := registerRecoveredDevice(ctx, tx, r.account, r.statement.NewMember); err != nil {
		return nil, internal(err)
	}
	token := newToken()
	expires := time.Now().Add(rs.s.cfg.TokenTTL).UnixMilli()
	if _, err := tx.ExecContext(ctx, `INSERT INTO tokens (hash, account_id, device_id, expires_ms) VALUES (?, ?, ?, ?)`, hashToken(token), r.account, r.statement.NewAuthorityID, expires); err != nil {
		return nil, internal(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE security_recovery SET completed_ms = ?, device_id = ?, token_hash = ? WHERE recovery_id = ?`, nowMs(), r.statement.NewAuthorityID, hashToken(token), req.Msg.RecoveryId); err != nil {
		return nil, internal(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, internal(err)
	}
	for _, old := range head.Core.Members {
		if _, ok := r.state.Core.Member(old.ID); !ok {
			rs.s.revokeStreams(old.ID)
			rs.s.hub.account(r.account).drop(old.ID)
		}
	}
	return connect.NewResponse(&pb.CompleteRecoveryResponse{DeviceId: r.statement.NewAuthorityID, Token: token}), nil
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
	reg, _ := proto.Marshal(&pb.DeviceRegistration{DevicePublicKey: m.EdPublic[:], Kind: pb.DeviceKind_DEVICE_KIND_CLIENT, CreatedMs: nowMs()})
	_, err := tx.ExecContext(ctx, `INSERT INTO devices (id, account_id, public_key, kind, registration, signature, created_ms) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (id) DO NOTHING`, m.ID, account, m.EdPublic[:], int32(pb.DeviceKind_DEVICE_KIND_CLIENT), reg, []byte{}, nowMs())
	return err
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
