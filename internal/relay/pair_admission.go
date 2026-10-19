package relay

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"github.com/Lewin671/remote-agent/internal/paths"
	"google.golang.org/protobuf/proto"
)

// The whole admission is durable before the registration transaction can
// transfer an archive. In particular, its signed replacement receipt must
// survive a lost response, not just the candidate's private keys.
type pairAdmission struct {
	Account        *Account
	Request        []byte
	PairingID      string
	CandidateToken string
	States         []deviceauth.State
	Proofs         []*deviceauth.RecoveryStatement
	Lease          deviceauth.LeaseCertificate
}

func pairAdmissionPath(server, id string) string {
	candidate := pairingIdentityPath(server, id)
	return filepath.Join(filepath.Dir(candidate), strings.Replace(filepath.Base(candidate), "pair-candidate-", "pair-admission-", 1))
}
func savePairAdmission(a pairAdmission) error {
	raw, err := json.Marshal(a)
	if err != nil {
		return err
	}
	return saveExclusivePrivate(pairAdmissionPath(a.Account.Server, a.PairingID), raw)
}
func clearPairAdmission(a pairAdmission) error {
	for _, path := range []string{pairingIdentityPath(a.Account.Server, a.PairingID), pairAdmissionPath(a.Account.Server, a.PairingID)} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return syncPrivateDirectory(paths.Dir())
}

// Finish an authorized registration before starting another SAS. A verified
// local endpoint is the trust anchor; the relay cannot nominate a new account.
// Refused/expired admissions remain as owned lineage for a fresh authority-
// signed rejoin, since a prior registration may have committed before revocation.

// ResumeRegistration is called by daemon startup before it loads the account.
func ResumeRegistration(ctx context.Context) error {
	files, err := filepath.Glob(filepath.Join(paths.Dir(), "pair-admission-*.json"))
	if err != nil {
		return err
	}
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var pending pairAdmission
		if len(raw) > 16<<20 || json.Unmarshal(raw, &pending) != nil || pending.Account == nil {
			return deviceauth.ErrInvalid
		}
		if _, _, err := resumePairAdmissions(ctx, pending.Account.Server); err != nil {
			return err
		}
	}
	return nil
}
func bootstrapAdmission(pending pairAdmission) error {
	a := pending.Account
	if a == nil || a.Security == nil || len(pending.States) == 0 {
		return deviceauth.ErrInvalid
	}
	if _, err := os.Stat(securityPath()); err == nil {
		ep, err := deviceauth.OpenEndpoint(securityPath())
		if err != nil {
			return err
		}
		pin, head, _ := ep.Snapshot()
		ep.Close()
		if pin != a.Security.Pin {
			return deviceauth.ErrUntrusted
		}
		if head.Core.Epoch >= pending.States[len(pending.States)-1].Core.Epoch {
			return nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	ep, err := deviceauth.InitEndpoint(securityPath(), a.Security.Pin, pending.States, pending.Proofs, a.DeviceID, a.DeviceSeed, a.Security.XPrivate, pending.Lease)
	if err != nil {
		return err
	}
	return ep.Close()
}

func resumePairAdmissions(ctx context.Context, server string) (*Account, map[string]*Account, error) {
	owned := map[string]*Account{}
	files, err := filepath.Glob(filepath.Join(paths.Dir(), "pair-admission-*.json"))
	if err != nil {
		return nil, owned, err
	}
	if len(files) == 0 {
		return nil, owned, nil
	}
	// This local receipt was written only after SAS and full chain verification.
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, owned, err
		}
		var pending pairAdmission
		if len(raw) > 16<<20 || json.Unmarshal(raw, &pending) != nil || pending.Account == nil {
			return nil, owned, deviceauth.ErrInvalid
		}
		if pending.Account.Server == server {
			if err := bootstrapAdmission(pending); err != nil {
				return nil, owned, err
			}
		}
	}
	endpoint, err := deviceauth.OpenEndpoint(securityPath())
	if err != nil {
		return nil, owned, err
	}
	defer endpoint.Close()
	pin, head, currentID := endpoint.Snapshot()
	states := endpoint.VerifiedStates()
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, owned, err
		}
		var pending pairAdmission
		if len(raw) > 16<<20 || json.Unmarshal(raw, &pending) != nil || pending.Account == nil {
			return nil, owned, deviceauth.ErrInvalid
		}
		a := pending.Account
		if a.Server != server {
			continue
		}
		if a.Security == nil || a.Security.Pin != pin || len(a.DeviceSeed) != 32 {
			return nil, owned, deviceauth.ErrUntrusted
		}
		ed := ed25519.NewKeyFromSeed(a.DeviceSeed)
		if e2e.DeviceID(ed.Public().(ed25519.PublicKey)) != a.DeviceID {
			return nil, owned, deviceauth.ErrUntrusted
		}
		var req pb.RegisterDeviceRequest
		var registration pb.DeviceRegistration
		var approval pb.PairApproval
		if proto.Unmarshal(pending.Request, &req) != nil || proto.Unmarshal(req.Registration, &registration) != nil || proto.Unmarshal(req.Approval, &approval) != nil {
			return nil, owned, deviceauth.ErrInvalid
		}
		if registration.Kind != pb.DeviceKind_DEVICE_KIND_MACHINE || e2e.AccountID(registration.AccountPublicKey) != pin.Account || !bytes.Equal(registration.DevicePublicKey, ed.Public().(ed25519.PublicKey)) || approval.PairingId != pending.PairingID || !bytes.Equal(approval.CandidateDevicePublicKey, registration.DevicePublicKey) {
			return nil, owned, deviceauth.ErrUntrusted
		}
		signerKnown := false
		for _, state := range states {
			member, ok := state.Core.Member(a.DeviceID)
			signer, issuer := state.Core.Member(approval.ApproverDeviceId)
			if ok && issuer && member.Kind == deviceauth.Machine && state.Core.AuthorityID == approval.ApproverDeviceId && bytes.Equal(member.EdPublic[:], registration.DevicePublicKey) && bytes.Equal(signer.EdPublic[:], approval.ApproverPublicKey) && ed25519.Verify(signer.EdPublic[:], append([]byte(e2e.PairApprovalContext), req.Approval...), req.ApprovalSignature) {
				signerKnown = true
				break
			}
		}
		if !signerKnown {
			return nil, owned, deviceauth.ErrUntrusted
		}
		owned[a.DeviceID] = a
		if a.DeviceID != currentID {
			continue
		}
		member, ok := head.Core.Member(a.DeviceID)
		if !ok {
			continue
		}
		x, err := ecdh.X25519().NewPrivateKey(a.Security.XPrivate)
		if err != nil || !bytes.Equal(x.PublicKey().Bytes(), member.XPublic[:]) {
			continue
		}
		token, loginErr := login(ctx, a)
		if loginErr != nil {
			if code := connect.CodeOf(loginErr); code != connect.CodePermissionDenied && code != connect.CodeNotFound {
				return nil, owned, loginErr
			}
			auth := remoteagentv1connect.NewAuthServiceClient(httpClient(), server)
			res, registrationErr := auth.RegisterDevice(ctx, connect.NewRequest(&req))
			if registrationErr != nil {
				switch connect.CodeOf(registrationErr) {
				case connect.CodePermissionDenied, connect.CodeNotFound, connect.CodeFailedPrecondition:
					continue
				default:
					return nil, owned, registrationErr
				}
			}
			if res.Msg.DeviceId != a.DeviceID {
				return nil, owned, deviceauth.ErrUntrusted
			}
		}
		_ = token
		if err := a.Save(); err != nil {
			return nil, owned, err
		}
		_, _ = pairingClient(server, "").GetPairing(ctx, connect.NewRequest(&pb.GetPairingRequest{PairingId: pending.PairingID, CandidateToken: pending.CandidateToken, Received: true}))
		if err := clearPairAdmission(pending); err != nil {
			return nil, owned, err
		}
		return a, owned, nil
	}
	return nil, owned, nil
}
