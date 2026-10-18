package relay

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/paths"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/e2e"
)

// Pairing from the command line, in both roles. The protocol is described
// in internal/e2e/pair.go.

const (
	pairPoll = time.Second
	// pairDeliverWait bounds how long the initiator waits for the new
	// device to take the key after approving.
	pairDeliverWait = 2 * time.Minute
)

// PairLink is what a QR code carries: the code, the initiator's
// commitment, and the server.
type PairLink struct {
	Code   string
	Commit []byte
	Server string
}

// URL is the link for the web client at web: opening it starts joining.
func (l PairLink) URL(web string) string {
	v := url.Values{"code": {l.Code}, "c": {base64.RawURLEncoding.EncodeToString(l.Commit)}, "s": {l.Server}}
	return strings.TrimRight(web, "/") + "/#/pair?" + v.Encode()
}

// ParsePairLink reads a link made by URL.
func ParsePairLink(raw string) (PairLink, error) {
	_, frag, ok := strings.Cut(raw, "#/pair?")
	if !ok {
		return PairLink{}, errors.New("not a pairing link")
	}
	v, err := url.ParseQuery(frag)
	if err != nil {
		return PairLink{}, err
	}
	c, err := base64.RawURLEncoding.DecodeString(v.Get("c"))
	if err != nil || len(c) != 32 || v.Get("code") == "" || v.Get("s") == "" {
		return PairLink{}, errors.New("incomplete pairing link")
	}
	return PairLink{Code: v.Get("code"), Commit: c, Server: v.Get("s")}, nil
}

func pairingClient(server, token string) remoteagentv1connect.PairingServiceClient {
	var opts []connect.ClientOption
	if token != "" {
		opts = append(opts, connect.WithInterceptors(bearer(token)))
	}
	return remoteagentv1connect.NewPairingServiceClient(httpClient(), server, opts...)
}

// Initiate adds a device to this machine's account. show is called with
// the pairing link once there is one; confirm with the emoji to compare
// and the kind of device asking, and must return true only if the user
// saw the same emoji on the new device.
func Initiate(ctx context.Context, acct *Account, show func(PairLink), confirm func(emoji []string, kind pb.DeviceKind) bool) error {
	token, err := login(ctx, acct)
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}
	pc := pairingClient(acct.Server, token)
	key, err := e2e.NewPairKey()
	if err != nil {
		return err
	}
	if acct.Security == nil {
		return deviceauth.ErrStorage
	}
	trust, err := deviceauth.PairTrustDescriptor(acct.Security.Pin, acct.DeviceID)
	if err != nil {
		return err
	}
	commit := e2e.PairDomainCommit(key.Public(), trust)
	created, err := pc.CreatePairing(ctx, connect.NewRequest(&pb.CreatePairingRequest{Commit: commit}))
	if err != nil {
		return err
	}
	id := created.Msg.PairingId
	var securityContext *deviceauth.PairContext
	cancel := func() {
		if securityContext != nil {
			binding, err := securityContext.Binding()
			if err == nil {
				signature := ed25519.Sign(acct.DeviceKey(), append([]byte("ra2/pair-cancel\x00"), binding...))
				var ignored any
				_ = acct.callAuthority(context.Background(), deviceauth.CancelPair, deviceauth.PairCancel{ID: securityContext.ID, Signature: signature}, &ignored)
			}
		}
		_, _ = pc.CancelPairing(context.Background(), connect.NewRequest(&pb.CancelPairingRequest{PairingId: id}))
	}
	show(PairLink{Code: created.Msg.Code, Commit: commit, Server: acct.Server})

	st, err := waitPairing(ctx, pc, id, "", pb.PairingState_PAIRING_STATE_JOINED)
	if err != nil {
		cancel()
		return err
	}
	candidate, err := deviceauth.NewMember(st.DevicePublicKey, st.DeviceHpkePublicKey, deviceauth.Kind(st.JoinerKind))
	if err != nil {
		cancel()
		return err
	}
	var pcx deviceauth.PairContext
	if err := acct.callAuthority(ctx, deviceauth.BeginPair, deviceauth.PairBegin{ID: id, Candidate: candidate}, &pcx); err != nil {
		cancel()
		return err
	}
	if pcx.Verify(acct.Security.Pin) != nil || pcx.Approver != acct.DeviceID || pcx.Candidate != candidate {
		cancel()
		return deviceauth.ErrUntrusted
	}
	securityContext = &pcx
	contextBytes, err := json.Marshal(pcx)
	if err != nil {
		cancel()
		return deviceauth.ErrInvalid
	}
	if _, err := pc.RevealPairing(ctx, connect.NewRequest(&pb.RevealPairingRequest{PairingId: id, InitiatorPublic: key.Public(), SecurityContext: contextBytes})); err != nil {
		cancel()
		return err
	}
	tr := e2e.PairDomainTranscript(id, key.Public(), st.JoinerPublic, st.DevicePublicKey, st.DeviceHpkePublicKey, int32(st.JoinerKind), trust)
	secrets, err := key.Derive(st.JoinerPublic, tr)
	if err != nil {
		cancel()
		return err
	}
	if !confirm(secrets.Emoji(), st.JoinerKind) {
		cancel()
		return errors.New("pairing refused")
	}
	for len(st.CandidateSignature) != ed25519.SignatureSize {
		select {
		case <-ctx.Done():
			cancel()
			return ctx.Err()
		case <-time.After(pairPoll):
		}
		res, err := pc.GetPairing(ctx, connect.NewRequest(&pb.GetPairingRequest{PairingId: id}))
		if err != nil {
			cancel()
			return err
		}
		st = res.Msg
		if st.State == pb.PairingState_PAIRING_STATE_CANCELED {
			cancel()
			return errors.New("pairing canceled")
		}
	}
	approverSig, err := deviceauth.SignPair(pcx, acct.DeviceKey(), false)
	if err != nil {
		cancel()
		return err
	}
	var joinedState deviceauth.State
	if err := acct.callAuthority(ctx, deviceauth.ApprovePair, deviceauth.PairApprove{ID: id, ApproverSignature: approverSig, CandidateSignature: st.CandidateSignature}, &joinedState); err != nil {
		cancel()
		return err
	}
	joinedBytes, err := joinedState.MarshalBinary()
	if err != nil {
		cancel()
		return err
	}
	keys, err := e2e.Derive(acct.Root)
	if err != nil {
		cancel()
		return err
	}
	approval, approvalSig, err := e2e.NewPairApproval(e2e.AccountID(keys.AccountPublicKey()), id, acct.DeviceKey(), st.DevicePublicKey, st.JoinerKind, created.Msg.ExpiresMs)
	if err != nil {
		cancel()
		return err
	}
	env, err := e2e.SealMessage(secrets.Key, &pb.Header{
		Kind: pb.PayloadKind_PAYLOAD_KIND_PAIRING, RequestId: id, Sender: acct.DeviceID,
	}, &pb.PairingPayload{Root: acct.Root, Server: acct.Server, Approval: approval, ApprovalSignature: approvalSig, SecurityState: joinedBytes})
	if err != nil {
		cancel()
		return err
	}
	if _, err := pc.ApprovePairing(ctx, connect.NewRequest(&pb.ApprovePairingRequest{PairingId: id, Payload: env, Approval: approval, ApprovalSignature: approvalSig})); err != nil {
		cancel()
		return err
	}
	// Only the new device confirming it has the key counts as success.
	wctx, stop := context.WithTimeout(ctx, pairDeliverWait)
	defer stop()
	if _, err := waitPairing(wctx, pc, id, "", pb.PairingState_PAIRING_STATE_DELIVERED); err != nil {
		cancel()
		return fmt.Errorf("the new device did not take the account key (%v); check it, and pair again if needed", err)
	}
	return nil
}

// JoinByPairing joins an account through a code shown by an existing
// device. If link carries a commitment (it was scanned or pasted), the
// initiator's key must match it. show gets the emoji to compare.
func JoinByPairing(ctx context.Context, link PairLink, confirm func(emoji []string) bool) (*Account, error) {
	if _, err := os.Stat(paths.Account()); err == nil {
		return nil, errors.New("this machine already has an account; use account security-enroll for one-time security setup")
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	return joinByPairing(ctx, link, confirm, nil)
}

// EnrollByPairing pins the management computer for an existing retained
// device, preserving its account root, history and Ed25519 identity.
func EnrollByPairing(ctx context.Context, link PairLink, confirm func([]string) bool) (*Account, error) {
	existing, err := LoadAccount()
	if err != nil {
		return nil, err
	}
	if existing.Security != nil {
		return nil, errors.New("security is already initialized; automatic authority replacement is not supported")
	}
	if _, err := login(ctx, existing); err != nil {
		return nil, fmt.Errorf("the retained device registration must still be valid: %w", err)
	}
	return joinByPairing(ctx, link, confirm, existing)
}
func joinByPairing(ctx context.Context, link PairLink, confirm func(emoji []string) bool, existing *Account) (*Account, error) {
	server, err := CheckServerURL(link.Server)
	if err != nil {
		return nil, err
	}
	if existing != nil && existing.Server != server {
		return nil, errors.New("security enrollment must use the existing account server")
	}
	pc := pairingClient(server, "")
	key, err := e2e.NewPairKey()
	if err != nil {
		return nil, err
	}
	var pub ed25519.PublicKey
	var priv ed25519.PrivateKey
	if existing != nil {
		priv = existing.DeviceKey()
		pub = priv.Public().(ed25519.PublicKey)
	} else {
		pub, priv, err = ed25519.GenerateKey(nil)
		if err != nil {
			return nil, err
		}
	}
	x, xpub, err := deviceauth.NewXKey()
	if err != nil {
		return nil, err
	}
	peek, err := pc.PeekPairing(ctx, connect.NewRequest(&pb.PeekPairingRequest{Code: link.Code, ProtocolVersion: 9}))
	if err != nil {
		return nil, err
	}
	if len(peek.Msg.Commit) != 32 || peek.Msg.PairingId == "" {
		return nil, deviceauth.ErrInvalid
	}
	if link.Commit != nil && !bytes.Equal(link.Commit, peek.Msg.Commit) {
		return nil, errors.New("the server returned another device's commitment; pairing stopped")
	}
	joined, err := pc.JoinPairing(ctx, connect.NewRequest(&pb.JoinPairingRequest{ProtocolVersion: 9,
		Code: link.Code, JoinerPublic: key.Public(), JoinerKind: pb.DeviceKind_DEVICE_KIND_MACHINE, DevicePublicKey: pub, DeviceHpkePublicKey: xpub,
	}))
	if err != nil {
		return nil, err
	}
	if joined.Msg.PairingId != peek.Msg.PairingId || !bytes.Equal(joined.Msg.Commit, peek.Msg.Commit) {
		return nil, errors.New("the pairing binding changed after revealing this device's key; pairing stopped")
	}
	id, token := joined.Msg.PairingId, joined.Msg.CandidateToken
	commit := joined.Msg.Commit
	if link.Commit != nil && !bytes.Equal(link.Commit, commit) {
		return nil, errors.New("the server returned another device's commitment; pairing stopped")
	}
	st, err := waitPairing(ctx, pc, id, token, pb.PairingState_PAIRING_STATE_REVEALED)
	if err != nil {
		return nil, err
	}
	var pcx deviceauth.PairContext
	if len(st.SecurityContext) > 8<<10 || json.Unmarshal(st.SecurityContext, &pcx) != nil {
		return nil, deviceauth.ErrInvalid
	}
	pin := pcx.Pin()
	candidate, err := deviceauth.NewMember(pub, xpub, deviceauth.Machine)
	if err != nil || pcx.Verify(pin) != nil || pcx.ID != id || pcx.Candidate != candidate {
		return nil, deviceauth.ErrUntrusted
	}
	trust, err := deviceauth.PairTrustDescriptor(pin, pcx.Approver)
	if err != nil || !bytes.Equal(e2e.PairDomainCommit(st.InitiatorPublic, trust), commit) {
		return nil, errors.New("the existing device's trust descriptor does not match its commitment; pairing stopped")
	}
	tr := e2e.PairDomainTranscript(id, st.InitiatorPublic, key.Public(), pub, xpub, int32(pb.DeviceKind_DEVICE_KIND_MACHINE), trust)
	secrets, err := key.Derive(st.InitiatorPublic, tr)
	if err != nil {
		return nil, err
	}
	if confirm == nil || !confirm(secrets.Emoji()) {
		_, _ = pc.CancelPairing(ctx, connect.NewRequest(&pb.CancelPairingRequest{PairingId: id, CandidateToken: token}))
		return nil, errors.New("pairing canceled: this device did not confirm the four pictures")
	}
	candidateSig, err := deviceauth.SignPair(pcx, priv, true)
	if err != nil {
		return nil, err
	}
	if _, err := pc.GetPairing(ctx, connect.NewRequest(&pb.GetPairingRequest{PairingId: id, CandidateToken: token, CandidateSignature: candidateSig})); err != nil {
		return nil, err
	}
	st, err = waitPairing(ctx, pc, id, token, pb.PairingState_PAIRING_STATE_APPROVED)
	if err != nil {
		return nil, err
	}
	payload := &pb.PairingPayload{}
	if _, err := e2e.OpenMessage(secrets.Key, st.Payload, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_PAIRING, RequestId: id}, payload); err != nil {
		return nil, fmt.Errorf("the account key does not open: %w", err)
	}
	if payload.Server != server {
		return nil, errors.New("the account belongs to another server")
	}
	keys, err := e2e.Derive(payload.Root)
	if err != nil {
		return nil, err
	}
	a, err := e2e.VerifyPairApproval(payload.Approval, payload.ApprovalSignature)
	if err != nil || a.AccountId != e2e.AccountID(keys.AccountPublicKey()) || a.PairingId != id || !bytes.Equal(a.CandidateDevicePublicKey, pub) || a.CandidateKind != pb.DeviceKind_DEVICE_KIND_MACHINE || a.ExpiresMs <= time.Now().UnixMilli() {
		return nil, e2e.ErrPairApproval
	}
	if pin.Account != e2e.AccountID(keys.AccountPublicKey()) {
		return nil, deviceauth.ErrUntrusted
	}
	joinedState, err := deviceauth.ParseState(payload.SecurityState)
	if err != nil || pin.Verify(joinedState) != nil || joinedState.Core.Parent != pcx.Expected {
		return nil, deviceauth.ErrUntrusted
	}
	if member, ok := joinedState.Core.Member(candidate.ID); !ok || member != candidate {
		return nil, deviceauth.ErrUntrusted
	}
	if _, err := pin.OpenGrant(joinedState, candidate.ID, x); err != nil {
		return nil, err
	}
	var acct *Account
	if existing != nil {
		if !bytes.Equal(payload.Root, existing.Root) {
			return nil, errors.New("the pairing belongs to a different account; existing keys were preserved")
		}
		acct = existing
	} else {
		j, err := newJoiner(server, payload.Root)
		if err != nil {
			return nil, err
		}
		j.pub, j.priv = pub, priv
		acct, err = j.register(ctx, &pb.RegisterDeviceRequest{ProtocolVersion: 9, Approval: payload.Approval, ApprovalSignature: payload.ApprovalSignature})
		if err != nil {
			return nil, err
		}
	}
	endpoint, err := deviceauth.InitEndpoint(filepath.Join(paths.Dir(), "security-v2.db"), pin, joinedState, acct.DeviceID, acct.DeviceSeed, x)
	if err != nil {
		return nil, err
	}
	if err := endpoint.Close(); err != nil {
		return nil, err
	}
	acct.Security = &SecurityConfig{Pin: pin, XPrivate: x}
	if err := acct.Save(); err != nil {
		return nil, err
	}
	// Tells the initiator it went through. If this is lost, the initiator
	// reports the outcome as unknown; the join itself stands.
	_, _ = pc.GetPairing(ctx, connect.NewRequest(&pb.GetPairingRequest{PairingId: id, CandidateToken: token, Received: true}))
	return acct, nil
}

// waitPairing polls until the pairing reaches want.
func waitPairing(ctx context.Context, pc remoteagentv1connect.PairingServiceClient, id, token string, want pb.PairingState) (*pb.GetPairingResponse, error) {
	for {
		res, err := pc.GetPairing(ctx, connect.NewRequest(&pb.GetPairingRequest{PairingId: id, CandidateToken: token}))
		if err != nil {
			if connect.CodeOf(err) == connect.CodeNotFound {
				return nil, errors.New("the pairing expired or was canceled")
			}
			return nil, err
		}
		switch st := res.Msg.State; {
		case st == pb.PairingState_PAIRING_STATE_CANCELED:
			return nil, errors.New("the other device canceled the pairing")
		case st >= want:
			return res.Msg, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pairPoll):
		}
	}
}
