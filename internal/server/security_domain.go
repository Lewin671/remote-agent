package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
)

// This transport ACL protects an honest relay from stale bearer credentials.
// It is never a source of endpoint trust: endpoints pin their authority by SAS.
func (m *machineService) PinSecurityDomain(ctx context.Context, req *connect.Request[pb.PinSecurityDomainRequest]) (*connect.Response[pb.PinSecurityDomainResponse], error) {
	id, err := machineFrom(ctx)
	if err != nil {
		return nil, err
	}
	state, err := deviceauth.ParseState(req.Msg.GenesisState)
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("invalid security genesis"))
	}
	pin, err := state.Pin()
	t := m.s.tenantFrom(ctx)
	if err != nil || pin.Verify(state) != nil || pin.Account != t.acct || pin.AuthorityID != id || len(state.Core.Members) != 1 || state.Core.Members[0].ID != id || state.Core.Members[0].Kind != deviceauth.Machine || !bytes.Equal(state.Core.Members[0].EdPublic[:], deviceFrom(ctx).publicKey) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("only the authority host installs its genesis"))
	}
	if _, err = t.store.db.ExecContext(ctx, `UPDATE accounts SET security_genesis=? WHERE id=? AND security_genesis IS NULL`, req.Msg.GenesisState, t.acct); err != nil {
		return nil, internal(err)
	}
	var current []byte
	if err = t.store.db.QueryRowContext(ctx, `SELECT security_genesis FROM accounts WHERE id=?`, t.acct).Scan(&current); err != nil {
		return nil, internal(err)
	}
	if !bytes.Equal(current, req.Msg.GenesisState) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("another security domain is pinned; manual setup is required"))
	}
	return connect.NewResponse(&pb.PinSecurityDomainResponse{}), nil
}

func (t *tstore) verifyRemoval(ctx context.Context, requester string, req *pb.RemoveDeviceRequest) error {
	var genesis []byte
	if err := t.db.QueryRowContext(ctx, `SELECT security_genesis FROM accounts WHERE id=?`, t.acct).Scan(&genesis); err != nil {
		return err
	}
	state, err := deviceauth.ParseState(genesis)
	if err != nil {
		return deviceauth.ErrUntrusted
	}
	pin, err := state.Pin()
	if err != nil || pin.Verify(state) != nil || pin.Account != t.acct || req.DeviceId == pin.AuthorityID {
		return deviceauth.ErrUntrusted
	}
	if len(req.SecurityCompletion) > deviceauth.MaxRPCBytes || len(req.SecurityRequest) > 64<<10 || len(req.SecurityChain) == 0 || len(req.SecurityChain) > 128 {
		return deviceauth.ErrCapacity
	}
	var completion deviceauth.Completion
	var original deviceauth.DeleteRequest
	if json.Unmarshal(req.SecurityCompletion, &completion) != nil || json.Unmarshal(req.SecurityRequest, &original) != nil || original.Requester != requester {
		return deviceauth.ErrUntrusted
	}
	found := false
	for _, target := range original.Targets {
		if target.ID == req.DeviceId {
			found = true
		}
	}
	if !found {
		return deviceauth.ErrUntrusted
	}
	chain := make([]deviceauth.State, 0, len(req.SecurityChain))
	size := 0
	for _, raw := range req.SecurityChain {
		size += len(raw)
		if size > 3<<20 {
			return deviceauth.ErrCapacity
		}
		s, e := deviceauth.ParseState(raw)
		if e != nil {
			return e
		}
		chain = append(chain, s)
	}
	return completion.Verify(pin, original, chain)
}
