package server

import (
	"bytes"
	"context"
	"errors"
	"log/slog"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
)

// The server stores the phone-signed membership chain and enforces it on
// every authenticated call. It validates signatures and membership; it never
// sees a content key and cannot open an envelope.

const (
	maxSecurityStates     = 8
	maxSecurityBatchBytes = 3 << 20
)

var errNoSecurityChain = errors.New("account has no security domain yet")

type securityRow struct {
	epoch    uint64
	state    deviceauth.State
	raw      []byte
	hash     deviceauth.Hash
	recovery *deviceauth.RecoveryStatement
	recRaw   []byte
}

// securityGenesis returns the pinned genesis bytes and parsed state.
func (t *tstore) securityGenesis(ctx context.Context) ([]byte, deviceauth.State, error) {
	var raw []byte
	if err := t.db.QueryRowContext(ctx, `SELECT security_genesis FROM accounts WHERE id = ?`, t.acct).Scan(&raw); err != nil {
		return nil, deviceauth.State{}, err
	}
	if len(raw) == 0 {
		return nil, deviceauth.State{}, errNoRows
	}
	s, err := deviceauth.ParseState(raw)
	return raw, s, err
}

// securityHead returns the current head state and hash, or errNoRows.
func (t *tstore) securityHead(ctx context.Context) (deviceauth.State, deviceauth.Hash, error) {
	var raw []byte
	err := t.db.QueryRowContext(ctx, `SELECT state FROM security_states WHERE account_id = ? ORDER BY epoch DESC LIMIT 1`, t.acct).Scan(&raw)
	if err != nil {
		return deviceauth.State{}, deviceauth.Hash{}, err
	}
	s, err := deviceauth.ParseState(raw)
	if err != nil {
		return deviceauth.State{}, deviceauth.Hash{}, err
	}
	h, err := s.Hash()
	return s, h, err
}

// securityStates returns rows after epoch, oldest first.
func (t *tstore) securityStates(ctx context.Context, after uint64, limit int) ([]securityRow, uint64, error) {
	rows, err := t.db.QueryContext(ctx, `SELECT epoch, state, hash, recovery FROM security_states WHERE account_id = ? AND epoch > ? ORDER BY epoch LIMIT ?`, t.acct, after, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []securityRow
	for rows.Next() {
		var r securityRow
		var raw, hash, rec []byte
		if err := rows.Scan(&r.epoch, &raw, &hash, &rec); err != nil {
			return nil, 0, err
		}
		s, err := deviceauth.ParseState(raw)
		if err != nil {
			return nil, 0, err
		}
		r.state, r.raw = s, raw
		copy(r.hash[:], hash)
		if len(rec) > 0 {
			st, err := deviceauth.ParseRecoveryStatement(rec)
			if err != nil {
				return nil, 0, err
			}
			r.recovery, r.recRaw = &st, rec
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	var head uint64
	if err := t.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(epoch), 0) FROM security_states WHERE account_id = ?`, t.acct).Scan(&head); err != nil {
		return nil, 0, err
	}
	return out, head, nil
}

func (t *tstore) securityMember(ctx context.Context, id string) (bool, error) {
	head, _, err := t.securityHead(ctx)
	if errors.Is(err, errNoRows) {
		return false, errNoSecurityChain
	}
	if err != nil {
		return false, err
	}
	_, ok := head.Core.Member(id)
	return ok, nil
}

// pinSecurityDomain is called by the new authority phone while it creates
// the account. It verifies the genesis fully and stores the exact bytes.
func (c *clientService) PinSecurityDomain(ctx context.Context, req *connect.Request[pb.PinSecurityDomainRequest]) (*connect.Response[pb.PinSecurityDomainResponse], error) {
	d, err := clientFrom(ctx)
	if err != nil {
		return nil, err
	}
	raw := req.Msg.GenesisState
	if len(raw) == 0 || len(raw) > deviceauth.MaxStateBytes {
		return nil, invalid("invalid security genesis")
	}
	state, err := deviceauth.ParseState(raw)
	if err != nil {
		return nil, invalid("invalid security genesis")
	}
	if state.Core.IssuedAt > uint64(nowMs())+deviceauth.MaxClockSkew {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("the security genesis is from the future; check this device's clock"))
	}
	pin, err := state.Pin()
	if err != nil || pin.VerifyGenesis(state) != nil || pin.Account != d.account {
		return nil, invalid("invalid security genesis")
	}
	m, ok := state.Core.Member(state.Core.AuthorityID)
	if !ok || state.Core.AuthorityID != d.id || m.Kind != deviceauth.Client || len(state.Core.Members) != 1 || !bytes.Equal(m.EdPublic[:], d.publicKey) || state.Core.RecoveryEd == [32]byte{} {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("only the new authority phone installs its genesis"))
	}
	t := c.s.tenantFrom(ctx)
	tx, err := t.store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, internal(err)
	}
	defer tx.Rollback()
	var current []byte
	var offline int
	if err := tx.QueryRowContext(ctx, `SELECT security_genesis, offline_pin FROM accounts WHERE id = ?`, t.acct).Scan(&current, &offline); err != nil {
		return nil, internal(err)
	}
	if len(current) > 0 {
		if !bytes.Equal(current, raw) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("another security domain is pinned; manual setup is required"))
		}
	} else if offline != 0 {
		// Protocol 10 upgraded this account from layout 5. Old device tokens
		// may still exist, so letting any signed-in device pin over the
		// network would let one race the phone; the operator pins the
		// exported genesis while the relay is stopped instead.
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("this account was upgraded from protocol 9: export the genesis and pin it on the relay offline, with the relay stopped"))
	} else if _, err := tx.ExecContext(ctx, `UPDATE accounts SET security_genesis = ?, recovery_ed = ? WHERE id = ?`, raw, state.Core.RecoveryEd[:], t.acct); err != nil {
		return nil, internal(err)
	}
	h, _ := state.Hash()
	if _, err := tx.ExecContext(ctx, `INSERT INTO security_states (account_id, epoch, state, hash) VALUES (?, 1, ?, ?) ON CONFLICT (account_id, epoch) DO NOTHING`, t.acct, raw, h[:]); err != nil {
		return nil, internal(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, internal(err)
	}
	return connect.NewResponse(&pb.PinSecurityDomainResponse{}), nil
}

// PutSecurityState accepts a contiguous signed segment, validates every
// transition, stores it atomically and revokes removed devices.
func (c *clientService) PutSecurityState(ctx context.Context, req *connect.Request[pb.PutSecurityStateRequest]) (*connect.Response[pb.PutSecurityStateResponse], error) {
	if len(req.Msg.States) == 0 || len(req.Msg.States) > maxSecurityStates {
		return nil, invalid("at most %d states per upload", maxSecurityStates)
	}
	var size int
	for _, s := range req.Msg.States {
		size += len(s.State) + len(s.Recovery)
	}
	if size > maxSecurityBatchBytes {
		return nil, invalid("security chain segment too large")
	}
	t := c.s.tenantFrom(ctx)
	genesisRaw, _, err := t.store.securityGenesis(ctx)
	if errors.Is(err, errNoRows) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the security domain is not pinned yet"))
	}
	if err != nil {
		return nil, internal(err)
	}
	head, _, err := t.store.securityHead(ctx)
	if errors.Is(err, errNoRows) {
		// Only the pinned genesis itself may start the chain.
		head = deviceauth.State{}
	} else if err != nil {
		return nil, internal(err)
	}

	var accepted []securityRow
	var removed []string
	prev := head
	haveHead := head.Core.Epoch != 0
	for _, in := range req.Msg.States {
		s, err := deviceauth.ParseState(in.State)
		if err != nil {
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("invalid security state"))
		}
		if s.Core.IssuedAt > uint64(nowMs())+deviceauth.MaxClockSkew {
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("the security state is from the future; check this device's clock"))
		}
		raw := in.State
		var proof *deviceauth.RecoveryStatement
		if len(in.Recovery) > 0 {
			// The 48-hour delay is enforced by the recovery service; letting a
			// current member publish a recovery transition here would bypass it.
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("recovery transitions must use the recovery service"))
		}
		h, err := s.Hash()
		if err != nil {
			return nil, connect.NewError(connect.CodePermissionDenied, errors.New("invalid security state"))
		}
		if s.Core.Epoch == 1 {
			if !bytes.Equal(raw, genesisRaw) {
				return nil, connect.NewError(connect.CodePermissionDenied, errors.New("the first state is not the pinned genesis"))
			}
		}
		if s.Core.Epoch <= prev.Core.Epoch && haveHead {
			// Already stored: byte-identical is a retry; anything else is a fork.
			var stored []byte
			if err := t.store.db.QueryRowContext(ctx, `SELECT state FROM security_states WHERE account_id = ? AND epoch = ?`, t.acct, s.Core.Epoch).Scan(&stored); err != nil {
				return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("security chain gap"))
			}
			if !bytes.Equal(stored, raw) {
				return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("security chain fork"))
			}
			continue
		}
		if !haveHead && s.Core.Epoch != 1 {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("security chain must start at the genesis"))
		}
		if haveHead && s.Core.Epoch != prev.Core.Epoch+1 {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("security chain gap"))
		}
		if haveHead {
			if err := deviceauth.VerifyTransition(prev, s, proof); err != nil {
				return nil, connect.NewError(connect.CodePermissionDenied, errors.New("security transition rejected"))
			}
		} else if s.Core.Epoch != 1 {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("security chain must start at the genesis"))
		}
		if s.Core.Epoch > deviceauth.MaxStates {
			return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("security chain is full"))
		}
		accepted = append(accepted, securityRow{epoch: s.Core.Epoch, state: s, raw: raw, hash: h, recovery: proof, recRaw: in.Recovery})
		if haveHead {
			for _, m := range prev.Core.Members {
				if _, ok := s.Core.Member(m.ID); !ok {
					removed = append(removed, m.ID)
				}
			}
		}
		prev, haveHead = s, true
	}
	if len(accepted) == 0 {
		// Everything was already stored; answer with the current head.
		raw, err := head.MarshalBinary()
		if err != nil {
			return nil, internal(err)
		}
		return connect.NewResponse(&pb.PutSecurityStateResponse{HeadEpoch: head.Core.Epoch, Head: raw}), nil
	}

	tx, err := t.store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, internal(err)
	}
	defer tx.Rollback()
	for _, r := range accepted {
		var rec []byte
		if r.recRaw != nil {
			rec = r.recRaw
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO security_states (account_id, epoch, state, hash, recovery) VALUES (?, ?, ?, ?, ?) ON CONFLICT (account_id, epoch) DO NOTHING`,
			t.acct, r.epoch, r.raw, r.hash[:], rec)
		if err != nil {
			return nil, internal(err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			var stored []byte
			if err := tx.QueryRowContext(ctx, `SELECT state FROM security_states WHERE account_id = ? AND epoch = ?`, t.acct, r.epoch).Scan(&stored); err != nil || !bytes.Equal(stored, r.raw) {
				return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("security chain fork"))
			}
		}
	}
	last := accepted[len(accepted)-1]
	if _, err := tx.ExecContext(ctx, `UPDATE accounts SET recovery_ed = ? WHERE id = ?`, last.state.Core.RecoveryEd[:], t.acct); err != nil {
		return nil, internal(err)
	}
	// Any new head makes a pending recovery statement stale: its Parent can
	// no longer match. This is the veto path.
	if _, err := tx.ExecContext(ctx, `UPDATE security_recovery SET rejected_ms = ? WHERE account_id = ? AND completed_ms = 0 AND rejected_ms = 0`, nowMs(), t.acct); err != nil {
		return nil, internal(err)
	}
	for _, id := range removed {
		if _, err := removeDeviceTx(ctx, tx, t.acct, id); err != nil && !errors.Is(err, errNoRows) {
			return nil, internal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, internal(err)
	}
	raw, err := last.state.MarshalBinary()
	if err != nil {
		return nil, internal(err)
	}
	for _, id := range removed {
		c.s.revokeStreams(id)
		t.hub.drop(id)
		t.hub.broadcast(&pb.ServerFrame{Kind: &pb.ServerFrame_DeviceRemoved{DeviceRemoved: &pb.DeviceRemoved{DeviceId: id}}})
		t.hub.broadcast(&pb.ServerFrame{Kind: &pb.ServerFrame_MachineRemoved{MachineRemoved: &pb.MachineRemoved{MachineId: id}}})
		slog.Info("device removed by a security state", "device", id)
	}
	return connect.NewResponse(&pb.PutSecurityStateResponse{HeadEpoch: last.epoch, Head: raw}), nil
}

// GetSecurityStates serves the chain to any current member, machine or client.
func (c *clientService) GetSecurityStates(ctx context.Context, req *connect.Request[pb.GetSecurityStatesRequest]) (*connect.Response[pb.GetSecurityStatesResponse], error) {
	limit := int(req.Msg.Limit)
	if limit <= 0 {
		limit = maxSecurityStates
	}
	limit = min(limit, maxSecurityStates)
	t := c.s.tenantFrom(ctx)
	rows, head, err := t.store.securityStates(ctx, req.Msg.AfterEpoch, limit)
	if err != nil {
		return nil, internal(err)
	}
	out := &pb.GetSecurityStatesResponse{HeadEpoch: head}
	for _, r := range rows {
		out.States = append(out.States, &pb.SecurityState{State: r.raw, Recovery: r.recRaw})
	}
	// The authority phone sees a pending recovery even without a push. The
	// unauthenticated recovery service no longer tells it: only a key bound
	// to the account may ask, and the phone does not keep the recovery key.
	if d := deviceFrom(ctx); d != nil {
		if h, _, err := t.store.securityHead(ctx); err == nil && h.Core.AuthorityID == d.id {
			var pending, ready int64
			if err := t.store.db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(ready_ms), 0) FROM security_recovery WHERE account_id = ? AND completed_ms = 0 AND rejected_ms = 0`, t.acct).Scan(&pending, &ready); err == nil && pending > 0 {
				out.RecoveryPending = true
				out.RecoveryReadyMs = uint64(max(ready, 0))
			}
		}
	}
	return connect.NewResponse(out), nil
}

// authorizeDevice enforces the current head's member list on every
// authenticated call. Before a genesis exists only PinSecurityDomain is
// allowed, so a freshly registered device cannot use an account that the
// phone has not established yet.
func (s *Server) authorizeDevice(ctx context.Context, d *device, procedure string) error {
	t := s.store.tenant(d.account)
	head, _, err := t.securityHead(ctx)
	if errors.Is(err, errNoRows) {
		if procedure == remoteagentv1connect.ClientServicePinSecurityDomainProcedure {
			return nil
		}
		return connect.NewError(connect.CodePermissionDenied, errors.New("this account has no security domain yet; set it up in the app"))
	}
	if err != nil {
		return internal(err)
	}
	if _, ok := head.Core.Member(d.id); !ok {
		return connect.NewError(connect.CodePermissionDenied, errors.New("this device is not a current account member"))
	}
	return nil
}

// authorityDevice returns the current authority phone ID.
func (t *tstore) authorityDevice(ctx context.Context) (string, error) {
	head, _, err := t.securityHead(ctx)
	if err != nil {
		return "", err
	}
	return head.Core.AuthorityID, nil
}

// seedSecurityChains fills the epoch-1 row from a genesis pinned offline,
// validating it fully. A bad pin leaves the account without a chain, so every
// authenticated call for it is refused.
func (s *Server) seedSecurityChains(ctx context.Context) error {
	rows, err := s.store.db.QueryContext(ctx, `SELECT id, security_genesis FROM accounts WHERE security_genesis IS NOT NULL`)
	if err != nil {
		return err
	}
	defer rows.Close()
	type entry struct {
		acct string
		raw  []byte
	}
	var entries []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.acct, &e.raw); err != nil {
			return err
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, e := range entries {
		if len(e.raw) == 0 {
			continue
		}
		st, err := deviceauth.ParseState(e.raw)
		if err != nil {
			slog.Error("pinned security genesis is invalid", "account", e.acct)
			continue
		}
		pin, err := st.Pin()
		if err != nil || pin.VerifyGenesis(st) != nil || st.Core.Account != e.acct {
			slog.Error("pinned security genesis is invalid", "account", e.acct)
			continue
		}
		h, err := st.Hash()
		if err != nil {
			continue
		}
		var n int
		if err := s.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM security_states WHERE account_id = ?`, e.acct).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		if _, err := s.store.db.ExecContext(ctx, `INSERT INTO security_states (account_id, epoch, state, hash) VALUES (?, 1, ?, ?)`, e.acct, e.raw, h[:]); err != nil {
			return err
		}
		if _, err := s.store.db.ExecContext(ctx, `UPDATE accounts SET recovery_ed = ? WHERE id = ?`, st.Core.RecoveryEd[:], e.acct); err != nil {
			return err
		}
	}
	return nil
}
