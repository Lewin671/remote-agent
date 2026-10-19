package relay

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"github.com/Lewin671/remote-agent/internal/protocol"
	"google.golang.org/protobuf/proto"
)

// Joining an account from the command line. Only the phone initiates and
// approves; a computer only scans or types a code and compares four emoji.

const (
	pairPoll = time.Second
	// pairDeliverWait bounds how long the initiator waits for the new
	// device to take the chain after approving.
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

// JoinByPairing joins an account through a code shown by the phone. On a
// machine that already has an account it re-binds that account: the Ed25519
// identity and any retained root are kept and the X25519 key is replaced.
func JoinByPairing(ctx context.Context, link PairLink, confirm func(emoji []string) bool) (*Account, error) {
	existing, err := LoadAccount()
	if err != nil && !errors.Is(err, ErrNoAccount) {
		return nil, err
	}
	server, err := CheckServerURL(link.Server)
	if err != nil {
		return nil, err
	}
	if existing != nil && existing.Server != server {
		return nil, errors.New("pairing must use the existing account's server")
	}
	pc := pairingClient(server, "")
	key, err := e2e.NewPairKey()
	if err != nil {
		return nil, err
	}
	// A protocol 10 security store on this computer makes the pin a trust
	// anchor; a protocol 9 account has none until it is paired again.
	// Only an absent file permits bootstrap. A corrupt/unreadable store is
	// still a trust anchor, even if account metadata no longer names it.
	_, statErr := os.Stat(securityPath())
	localV3 := !errors.Is(statErr, os.ErrNotExist)
	localRemoved := false
	if localV3 {
		if ep, err := deviceauth.OpenEndpoint(securityPath()); err == nil {
			localRemoved = ep.Removed()
			ep.Close()
		}
	}
	if existing != nil {
		if _, err := login(ctx, existing); err != nil {
			if connect.CodeOf(err) == connect.CodePermissionDenied || connect.CodeOf(err) == connect.CodeNotFound {
				localRemoved = true
			} else {
				return nil, fmt.Errorf("verify existing identity: %w", err)
			}
		}
	}
	pub, priv, err := joiningIdentity(existing, localRemoved)
	if err != nil {
		return nil, err
	}
	x, xpub, err := deviceauth.NewXKey()
	if err != nil {
		return nil, err
	}
	peek, err := pc.PeekPairing(ctx, connect.NewRequest(&pb.PeekPairingRequest{Code: link.Code, ProtocolVersion: protocol.Version}))
	if err != nil {
		return nil, err
	}
	if len(peek.Msg.Commit) != 32 || peek.Msg.PairingId == "" {
		return nil, deviceauth.ErrInvalid
	}
	if link.Commit != nil && !bytes.Equal(link.Commit, peek.Msg.Commit) {
		return nil, errors.New("the server returned another device's commitment; pairing stopped")
	}
	joined, err := pc.JoinPairing(ctx, connect.NewRequest(&pb.JoinPairingRequest{
		Code: link.Code, JoinerPublic: key.Public(), JoinerKind: pb.DeviceKind_DEVICE_KIND_MACHINE, DevicePublicKey: pub, DeviceHpkePublicKey: xpub, ProtocolVersion: protocol.Version,
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
	pin, approver, err := deviceauth.ParsePairTrustDescriptor(st.Trust)
	if err != nil || !bytes.Equal(e2e.PairDomainCommit(st.InitiatorPublic, st.Trust), commit) {
		return nil, errors.New("the existing device's trust descriptor does not match its commitment; pairing stopped")
	}
	if err := checkJoiningAccount(existing, pin, localV3); err != nil {
		return nil, err
	}
	tr := e2e.PairDomainTranscript(id, st.InitiatorPublic, key.Public(), pub, xpub, int32(pb.DeviceKind_DEVICE_KIND_MACHINE), st.Trust)
	secrets, err := key.Derive(st.InitiatorPublic, tr)
	if err != nil {
		return nil, err
	}
	if confirm == nil || !confirm(secrets.Emoji()) {
		_, _ = pc.CancelPairing(ctx, connect.NewRequest(&pb.CancelPairingRequest{PairingId: id, CandidateToken: token}))
		return nil, errors.New("pairing canceled: this device did not confirm the four pictures")
	}
	st, err = waitPairing(ctx, pc, id, token, pb.PairingState_PAIRING_STATE_APPROVED)
	if err != nil {
		return nil, err
	}
	payload := &pb.PairingPayload{}
	if _, err := e2e.OpenMessage(secrets.Key, st.Payload, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_PAIRING, RequestId: id}, payload); err != nil {
		return nil, fmt.Errorf("the pairing payload does not open: %w", err)
	}
	if payload.Server != server {
		return nil, errors.New("the account belongs to another server")
	}
	if len(payload.AccountPublicKey) != ed25519.PublicKeySize || e2e.AccountID(payload.AccountPublicKey) != pin.Account {
		return nil, deviceauth.ErrUntrusted
	}
	approval, err := e2e.VerifyPairApproval(payload.Approval, payload.ApprovalSignature)
	if err != nil || approval.AccountId != pin.Account || approval.PairingId != id || !bytes.Equal(approval.CandidateDevicePublicKey, pub) || approval.CandidateKind != pb.DeviceKind_DEVICE_KIND_MACHINE || approval.ExpiresMs <= time.Now().UnixMilli() || e2e.DeviceID(approval.ApproverPublicKey) != approver {
		return nil, e2e.ErrPairApproval
	}
	states, proofs, candidate, err := verifyPairChain(pin, payload.SecurityStates, pub, xpub)
	if err != nil {
		return nil, err
	}
	if localV3 {
		local, err := deviceauth.OpenEndpoint(securityPath())
		if err != nil {
			return nil, errors.New("the local security chain cannot be read; manual handling is required before rejoining")
		}
		defer local.Close()
		if err := local.CheckExtension(states); err != nil {
			return nil, err
		}
	}
	if candidate.ID != e2e.DeviceID(approval.CandidateDevicePublicKey) {
		return nil, deviceauth.ErrUntrusted
	}
	if last := states[len(states)-1]; last.Core.AuthorityID != approver {
		return nil, deviceauth.ErrUntrusted
	}
	if _, err := pin.OpenGrant(states[len(states)-1], candidate.ID, x); err != nil {
		return nil, err
	}

	acct := &Account{Server: server, DeviceSeed: priv.Seed(), DeviceID: candidate.ID}
	if existing != nil {
		acct.Root = existing.Root
		acct.Web = existing.Web
	}
	reg, err := proto.Marshal(&pb.DeviceRegistration{
		AccountPublicKey: payload.AccountPublicKey, DevicePublicKey: pub,
		Kind: pb.DeviceKind_DEVICE_KIND_MACHINE, CreatedMs: time.Now().UnixMilli(),
	})
	if err != nil {
		return nil, err
	}
	auth := remoteagentv1connect.NewAuthServiceClient(httpClient(), server)
	res, err := auth.RegisterDevice(ctx, connect.NewRequest(&pb.RegisterDeviceRequest{
		ProtocolVersion: protocol.Version, Registration: reg, Approval: payload.Approval, ApprovalSignature: payload.ApprovalSignature,
	}))
	if err != nil {
		return nil, fmt.Errorf("register: %w", err)
	}
	if res.Msg.DeviceId != candidate.ID {
		return nil, errors.New("server returned an unexpected device ID")
	}
	endpoint, err := deviceauth.InitEndpoint(securityPath(), pin, states, proofs, candidate.ID, priv.Seed(), x)
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
	// Tells the phone it went through. If this is lost, the phone reports
	// the outcome as unknown; the join itself stands.
	_, _ = pc.GetPairing(ctx, connect.NewRequest(&pb.GetPairingRequest{PairingId: id, CandidateToken: token, Received: true}))
	return acct, nil
}

// verifyPairChain checks every state from the pinned genesis and returns the
// new device's member.
func verifyPairChain(pin deviceauth.Pin, in []*pb.SecurityState, devicePub, deviceX []byte) ([]deviceauth.State, []*deviceauth.RecoveryStatement, deviceauth.Member, error) {
	if len(in) == 0 || len(in) > deviceauth.MaxStates {
		return nil, nil, deviceauth.Member{}, deviceauth.ErrInvalid
	}
	if len(in) > deviceauth.MaxPairStates {
		return nil, nil, deviceauth.Member{}, deviceauth.ErrChainTooLarge
	}
	size := 0
	for _, raw := range in {
		size += len(raw.State) + len(raw.Recovery)
	}
	if size > deviceauth.MaxPairChainBytes {
		return nil, nil, deviceauth.Member{}, deviceauth.ErrChainTooLarge
	}
	states := make([]deviceauth.State, 0, len(in))
	proofs := make([]*deviceauth.RecoveryStatement, 0, len(in))
	for _, raw := range in {
		s, err := deviceauth.ParseState(raw.State)
		if err != nil {
			return nil, nil, deviceauth.Member{}, err
		}
		var proof *deviceauth.RecoveryStatement
		if len(raw.Recovery) > 0 {
			p, err := deviceauth.ParseRecoveryStatement(raw.Recovery)
			if err != nil {
				return nil, nil, deviceauth.Member{}, err
			}
			proof = &p
		}
		states = append(states, s)
		proofs = append(proofs, proof)
	}
	if pin.VerifyGenesis(states[0]) != nil {
		return nil, nil, deviceauth.Member{}, deviceauth.ErrUntrusted
	}
	for i := 1; i < len(states); i++ {
		if err := deviceauth.VerifyTransition(states[i-1], states[i], proofs[i]); err != nil {
			return nil, nil, deviceauth.Member{}, err
		}
	}
	candidate, err := deviceauth.NewMember(devicePub, deviceX, deviceauth.Machine)
	if err != nil {
		return nil, nil, deviceauth.Member{}, err
	}
	return states, proofs, candidate, nil
}

// joiningIdentity reuses an existing device's Ed25519 key unless its signed
// head says it is no longer a member: a removed device rejoins as a new one.
func joiningIdentity(existing *Account, removed bool) (ed25519.PublicKey, ed25519.PrivateKey, error) {
	if existing != nil && !removed {
		priv := existing.DeviceKey()
		return priv.Public().(ed25519.PublicKey), priv, nil
	}
	return ed25519.GenerateKey(nil)
}

// checkJoiningAccount decides whether a pairing may replace an existing
// local account's security state. With a protocol 10 store the pin (account,
// domain and genesis) is a trust anchor and must match exactly; otherwise a
// relay could reply with a forged genesis for the same account. Only a
// protocol 9 account has no such anchor yet: its upgrade rebuilds the domain,
// so just the account identity has to match.
func checkJoiningAccount(existing *Account, pin deviceauth.Pin, localV3 bool) error {
	if existing == nil || existing.Security == nil {
		return nil
	}
	if pin.Account != existing.Security.Pin.Account {
		return errors.New("the code belongs to a different account; existing keys were preserved")
	}
	if localV3 && pin != existing.Security.Pin {
		return errors.New("the code is for another security domain; the relay cannot replace this computer's pinned domain")
	}
	return nil
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
