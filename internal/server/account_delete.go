package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/e2e"
)

func (c *clientService) DeleteAccount(ctx context.Context, req *connect.Request[pb.DeleteAccountRequest]) (*connect.Response[pb.DeleteAccountResponse], error) {
	d, err := clientFrom(ctx)
	if err != nil {
		return nil, err
	}
	r := req.Msg
	if len(r.StateHash) != 32 || len(r.Challenge) != 32 || len(r.Signature) != ed25519.SignatureSize {
		return nil, invalid("invalid account deletion proof")
	}
	if !ed25519.Verify(d.publicKey, e2e.AccountDeletionMessage(d.account, r.StateHash, r.Challenge), r.Signature) || !c.s.takeChallenge(d.id, r.Challenge) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("a fresh account deletion signature is required"))
	}
	tx, err := c.s.store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, internal(err)
	}
	defer tx.Rollback()
	var raw []byte
	if err := tx.QueryRowContext(ctx, `SELECT state FROM security_states WHERE account_id=? ORDER BY epoch DESC LIMIT 1`, d.account).Scan(&raw); err != nil {
		return nil, internal(err)
	}
	head, err := deviceauth.ParseState(raw)
	if err != nil {
		return nil, internal(err)
	}
	hash, err := head.Hash()
	if err != nil {
		return nil, internal(err)
	}
	member, ok := head.Core.Member(d.id)
	if !ok || head.Core.AuthorityID != d.id || !bytes.Equal(member.EdPublic[:], d.publicKey) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("use the current authority client to delete the account"))
	}
	if !bytes.Equal(hash[:], r.StateHash) {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the account security state changed; review the deletion again"))
	}
	if err := deleteAccountRows(ctx, tx, d.account); err != nil {
		return nil, internal(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, internal(err)
	}
	// End every live account stream at once, without waiting for the sweep.
	for _, m := range head.Core.Members {
		c.s.revokeStreams(m.ID)
	}
	c.s.challengeMu.Lock()
	for k, ch := range c.s.challenges {
		if ch.account == d.account {
			delete(c.s.challenges, k)
		}
	}
	c.s.challengeMu.Unlock()
	c.s.pairing.mu.Lock()
	for id, p := range c.s.pairing.byID {
		if p.account == d.account {
			delete(c.s.pairing.byCode, p.code)
			delete(c.s.pairing.byID, id)
		}
	}
	c.s.pairing.mu.Unlock()
	return connect.NewResponse(&pb.DeleteAccountResponse{}), nil
}
