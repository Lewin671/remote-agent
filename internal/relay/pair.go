package relay

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
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
	commit := e2e.PairCommit(key.Public())
	created, err := pc.CreatePairing(ctx, connect.NewRequest(&pb.CreatePairingRequest{Commit: commit}))
	if err != nil {
		return err
	}
	id := created.Msg.PairingId
	cancel := func() {
		_, _ = pc.CancelPairing(context.Background(), connect.NewRequest(&pb.CancelPairingRequest{PairingId: id}))
	}
	show(PairLink{Code: created.Msg.Code, Commit: commit, Server: acct.Server})

	st, err := waitPairing(ctx, pc, id, "", pb.PairingState_PAIRING_STATE_JOINED)
	if err != nil {
		cancel()
		return err
	}
	if _, err := pc.RevealPairing(ctx, connect.NewRequest(&pb.RevealPairingRequest{PairingId: id, InitiatorPublic: key.Public()})); err != nil {
		cancel()
		return err
	}
	tr := e2e.PairTranscript(id, key.Public(), st.JoinerPublic, int32(st.JoinerKind))
	secrets, err := key.Derive(st.JoinerPublic, tr)
	if err != nil {
		cancel()
		return err
	}
	if !confirm(secrets.Emoji(), st.JoinerKind) {
		cancel()
		return errors.New("pairing refused")
	}
	env, err := e2e.SealMessage(secrets.Key, &pb.Header{
		Kind: pb.PayloadKind_PAYLOAD_KIND_PAIRING, RequestId: id, Sender: acct.DeviceID,
	}, &pb.PairingPayload{Root: acct.Root, Server: acct.Server})
	if err != nil {
		cancel()
		return err
	}
	if _, err := pc.ApprovePairing(ctx, connect.NewRequest(&pb.ApprovePairingRequest{PairingId: id, Payload: env})); err != nil {
		cancel()
		return err
	}
	// Only the new device taking the key counts as success.
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
func JoinByPairing(ctx context.Context, link PairLink, show func(emoji []string)) (*Account, error) {
	server, err := CheckServerURL(link.Server)
	if err != nil {
		return nil, err
	}
	pc := pairingClient(server, "")
	key, err := e2e.NewPairKey()
	if err != nil {
		return nil, err
	}
	joined, err := pc.JoinPairing(ctx, connect.NewRequest(&pb.JoinPairingRequest{
		Code: link.Code, JoinerPublic: key.Public(), JoinerKind: pb.DeviceKind_DEVICE_KIND_MACHINE,
	}))
	if err != nil {
		return nil, err
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
	if !bytes.Equal(e2e.PairCommit(st.InitiatorPublic), commit) {
		return nil, errors.New("the existing device's key does not match its commitment; pairing stopped")
	}
	tr := e2e.PairTranscript(id, st.InitiatorPublic, key.Public(), int32(pb.DeviceKind_DEVICE_KIND_MACHINE))
	secrets, err := key.Derive(st.InitiatorPublic, tr)
	if err != nil {
		return nil, err
	}
	show(secrets.Emoji())
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
	return Join(ctx, server, payload.Root, "")
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
