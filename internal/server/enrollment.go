package server

import (
	"bytes"
	"context"
	"database/sql"
	"errors"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/e2e"
)

var errAdmissionRequired = errors.New("adding a device requires a fresh pairing approved by a currently enrolled device")
var errEnrollmentCommitted = errors.New("device already joined; remove it from the device list")

type enrollmentApproval struct {
	raw, signature []byte
	info           *pb.PairApproval
}

func parseApproval(raw, signature []byte) (*enrollmentApproval, error) {
	if len(raw) == 0 && len(signature) == 0 {
		return nil, nil
	}
	a, err := e2e.VerifyPairApproval(raw, signature)
	if err != nil {
		println("DEBUG approval", len(raw), len(signature), err.Error())
		return nil, errAdmissionRequired
	}
	return &enrollmentApproval{raw: bytes.Clone(raw), signature: bytes.Clone(signature), info: a}, nil
}

func checkApprover(ctx context.Context, tx *sql.Tx, a *pb.PairApproval) error {
	var pub []byte
	var state int
	err := tx.QueryRowContext(ctx, `SELECT d.public_key, a.status FROM devices d JOIN accounts a ON a.id = d.account_id WHERE d.account_id = ? AND d.id = ?`, a.AccountId, a.ApproverDeviceId).Scan(&pub, &state)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errAdmissionRequired
		}
		return err
	}
	if state != accountActive || !bytes.Equal(pub, a.ApproverPublicKey) {
		return errAdmissionRequired
	}
	// On an established account only the current authority phone may
	// authorize a registration. Before the genesis exists, the first device
	// bootstraps the account.
	var headRaw []byte
	err = tx.QueryRowContext(ctx, `SELECT state FROM security_states WHERE account_id = ? ORDER BY epoch DESC LIMIT 1`, a.AccountId).Scan(&headRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	head, err := deviceauth.ParseState(headRaw)
	if err != nil || head.Core.AuthorityID != a.ApproverDeviceId {
		return errAdmissionRequired
	}
	return nil
}

// approveAdmission stores only public identity and signature bytes. It does
// not trust the caller's earlier authentication to survive a revocation race.
func (s *store) approveAdmission(ctx context.Context, a *enrollmentApproval, candidateTokenHash []byte) error {
	if a == nil || a.info.ExpiresMs <= nowMs() || len(candidateTokenHash) != 32 {
		return errAdmissionRequired
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := checkApprover(ctx, tx, a.info); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM pairing_admissions WHERE expires_ms <= ?`, nowMs()); err != nil {
		return err
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pairing_admissions`).Scan(&n); err != nil {
		return err
	}
	if n >= maxPairings {
		return errFull("too many device approvals in progress")
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO pairing_admissions (pairing_id, account_id, approver_device_id, candidate_device_id, candidate_kind, approval, signature, expires_ms, candidate_token_hash) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, a.info.PairingId, a.info.AccountId, a.info.ApproverDeviceId, e2e.DeviceID(a.info.CandidateDevicePublicKey), int32(a.info.CandidateKind), a.raw, a.signature, a.info.ExpiresMs, candidateTokenHash)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// consumeAdmission is deliberately called inside addDevice's insertion
// transaction, including when a concurrent first-device request lost bootstrap.
func consumeAdmission(ctx context.Context, tx *sql.Tx, account, id string, reg *pb.DeviceRegistration, a *enrollmentApproval) error {
	if a == nil {
		return errAdmissionRequired
	}
	p := a.info
	if p.AccountId != account || e2e.DeviceID(p.CandidateDevicePublicKey) != id || !bytes.Equal(p.CandidateDevicePublicKey, reg.DevicePublicKey) || p.CandidateKind != reg.Kind || p.ExpiresMs <= nowMs() {
		return errAdmissionRequired
	}
	if err := checkApprover(ctx, tx, p); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE pairing_admissions SET used_ms = ? WHERE pairing_id = ? AND account_id = ? AND approver_device_id = ? AND candidate_device_id = ? AND candidate_kind = ? AND approval = ? AND signature = ? AND expires_ms > ? AND used_ms = 0 AND canceled_ms = 0`, nowMs(), p.PairingId, account, p.ApproverDeviceId, id, int32(reg.Kind), a.raw, a.signature, nowMs())
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 1 {
		return nil
	}
	// No stored admission row: a fresh phone-signed approval is the
	// authorization. Record its one use so it cannot admit another device
	// (the candidate key is already bound by the signature).
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pairing_admissions WHERE pairing_id = ?`, p.PairingId).Scan(&exists); err != nil {
		return err
	}
	if exists != 0 {
		return errAdmissionRequired
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO pairing_admissions (pairing_id, account_id, approver_device_id, candidate_device_id, candidate_kind, approval, signature, candidate_token_hash, expires_ms, used_ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.PairingId, account, p.ApproverDeviceId, id, int32(reg.Kind), a.raw, a.signature, make([]byte, 32), p.ExpiresMs, nowMs())
	return err
}

func (s *store) cancelAdmission(ctx context.Context, pairingID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var used int64
	if err := tx.QueryRowContext(ctx, `SELECT used_ms FROM pairing_admissions WHERE pairing_id = ?`, pairingID).Scan(&used); err == nil && used != 0 {
		return errEnrollmentCommitted
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE pairing_admissions SET canceled_ms = ? WHERE pairing_id = ? AND used_ms = 0`, nowMs(), pairingID); err != nil {
		return err
	}
	return tx.Commit()
}
