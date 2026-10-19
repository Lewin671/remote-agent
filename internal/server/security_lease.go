package server

import (
	"bytes"
	"context"
	"errors"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
)

func (t *tstore) putSecurityLease(ctx context.Context, raw []byte) error {
	l, err := deviceauth.ParseLease(raw)
	if err != nil {
		return invalid("invalid lease")
	}
	tx, err := t.db.BeginTx(ctx, nil)
	if err != nil {
		return internal(err)
	}
	defer tx.Rollback()
	var stateRaw []byte
	if err := tx.QueryRowContext(ctx, `SELECT state FROM security_states WHERE account_id=? ORDER BY epoch DESC LIMIT 1`, t.acct).Scan(&stateRaw); err != nil {
		return internal(err)
	}
	head, err := deviceauth.ParseState(stateRaw)
	if err != nil || l.Valid(head, uint64(nowMs())) != nil {
		return connect.NewError(connect.CodePermissionDenied, errors.New("lease does not bind the current head"))
	}
	var oldRaw []byte
	if err := tx.QueryRowContext(ctx, `SELECT certificate FROM security_leases WHERE account_id=?`, t.acct).Scan(&oldRaw); err == nil {
		old, err := deviceauth.ParseLease(oldRaw)
		if err != nil {
			return internal(err)
		}
		if old.Head == l.Head && (old.IssuedAt > l.IssuedAt || old.NotAfter > l.NotAfter || old.IssuedAt == l.IssuedAt && !bytes.Equal(oldRaw, raw)) {
			return connect.NewError(connect.CodeFailedPrecondition, errors.New("lease rollback or conflict"))
		}
	} else if !errors.Is(err, errNoRows) {
		return internal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO security_leases(account_id,certificate) VALUES(?,?)`, t.acct, raw); err != nil {
		return internal(err)
	}
	return tx.Commit()
}

// DeleteMachineRecords is explicitly separate from security revocation. It
// works after removal, when only the retained archive identifies the machine.
func (c *clientService) DeleteMachineRecords(ctx context.Context, req *connect.Request[pb.DeleteMachineRecordsRequest]) (*connect.Response[pb.DeleteMachineRecordsResponse], error) {
	t := c.s.tenantFrom(ctx)
	head, _, err := t.store.securityHead(ctx)
	if err != nil {
		return nil, internal(err)
	}
	d := deviceFrom(ctx)
	if d == nil || d.id != head.Core.AuthorityID {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("use the authority phone"))
	}
	id := req.Msg.MachineId
	if id == "" {
		return nil, invalid("machine required")
	}
	if err := t.store.deleteMachineRecords(ctx, id); err != nil {
		if errors.Is(err, errMachineIsMember) {
			return nil, connect.NewError(connect.CodeFailedPrecondition, err)
		}
		return nil, internal(err)
	}
	t.hub.broadcast(&pb.ServerFrame{Kind: &pb.ServerFrame_MachineRemoved{MachineRemoved: &pb.MachineRemoved{MachineId: id}}})
	return connect.NewResponse(&pb.DeleteMachineRecordsResponse{}), nil
}

var errMachineIsMember = errors.New("remove the machine before deleting its records")

// deleteMachineRecords deletes a machine's archive: its sessions, their
// events and its images. Revoking the machine does not do this.
func (t *tstore) deleteMachineRecords(ctx context.Context, id string) error {
	tx, err := t.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Membership and deletion share one transaction: a concurrent registration
	// or signed head update cannot turn this into deletion of a current machine.
	var registered bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM devices WHERE account_id=? AND id=?)`, t.acct, id).Scan(&registered); err != nil {
		return err
	}
	if registered {
		return errMachineIsMember
	}
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT state FROM security_states WHERE account_id=? ORDER BY epoch DESC LIMIT 1`, t.acct).Scan(&raw); err == nil {
		head, err := deviceauth.ParseState(raw)
		if err != nil {
			return err
		}
		if _, member := head.Core.Member(id); member {
			return errMachineIsMember
		}
	} else if !errors.Is(err, errNoRows) {
		return err
	}

	for _, q := range []string{
		`DELETE FROM events WHERE account_id=?1 AND session_id IN(SELECT session_id FROM sessions WHERE account_id=?1 AND machine_id=?2)`,
		`DELETE FROM images WHERE account_id=?1 AND machine_id=?2`,
		`DELETE FROM sessions WHERE account_id=?1 AND machine_id=?2`,
		`DELETE FROM machines WHERE account_id=?1 AND id=?2`,
		`UPDATE accounts SET event_bytes=(SELECT COALESCE(SUM(LENGTH(envelope)),0) FROM events WHERE account_id=?1) WHERE id=?1`,
	} {
		if _, err := tx.ExecContext(ctx, q, t.acct, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
