package server

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"github.com/Lewin671/remote-agent/internal/relayproto"
	"github.com/Lewin671/remote-agent/shared/devicesec"
	"google.golang.org/protobuf/proto"
)

func extractRelayErrorDetail(err error) *pb.RelayErrorDetail {
	var ce *connect.Error
	if !errors.As(err, &ce) {
		return nil
	}
	for _, d := range ce.Details() {
		v, e := d.Value()
		if detail, ok := v.(*pb.RelayErrorDetail); e == nil && ok {
			return detail
		}
	}
	return nil
}

func TestRelayErrorDetails(t *testing.T) {
	ctx := context.Background()

	// Base token-mode server with an active account (machine + client + synthetic phone authority).
	ts := newTestServer(t)
	a := ts.account(t, ts.keys)
	authPhone := ts.authority(t)
	authTok := ts.login(t, authPhone.phoneEd, authPhone.phoneID)
	authPairing := remoteagentv1connect.NewPairingServiceClient(http.DefaultClient, ts.url, withToken(authTok))
	nonAuthPairing := remoteagentv1connect.NewPairingServiceClient(http.DefaultClient, ts.url, withToken(a.ctok))
	anonPairing := remoteagentv1connect.NewPairingServiceClient(http.DefaultClient, ts.url)
	recoveryClient := remoteagentv1connect.NewRecoveryServiceClient(http.DefaultClient, ts.url)

	// Open-signup server for PoW & open-mode reasons.
	openTS := newTestServerWithConfig(t, Config{Signup: SignupOpen, SignupBits: 4, MaxAccounts: 2})

	makeRawReg := func(keys *e2e.Keys, kind pb.DeviceKind) ([]byte, []byte, ed25519.PublicKey, ed25519.PrivateKey) {
		pub, priv, _ := ed25519.GenerateKey(nil)
		reg, _ := proto.Marshal(&pb.DeviceRegistration{
			AccountPublicKey: keys.AccountPublicKey(),
			DevicePublicKey:  pub,
			Kind:             kind,
		})
		return reg, keys.SignRegistration(reg), pub, priv
	}

	// Prepare a pairing that is already joined for PAIRING_ALREADY_JOINED.
	pairKey, _ := e2e.NewPairKey()
	createdPair, err := authPairing.CreatePairing(ctx, connect.NewRequest(&pb.CreatePairingRequest{
		Commit: devicesec.PairDomainCommit(pairKey.Public(), ts.pairTrust(t, authPhone.phoneID)),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := anonPairing.JoinPairing(ctx, connect.NewRequest(&pb.JoinPairingRequest{
		ProtocolVersion:     relayproto.Version,
		Code:                createdPair.Msg.Code,
		JoinerPublic:        pairKey.Public(),
		DevicePublicKey:     pairKey.Public(),
		DeviceHpkePublicKey: pairKey.Public(),
		JoinerKind:          pb.DeviceKind_DEVICE_KIND_CLIENT,
	})); err != nil {
		t.Fatal(err)
	}

	// Prepare a pending recovery for RECOVERY_NOT_READY and a vetoed one for RECOVERY_REJECTED.
	recTS, recHead, recKey, _ := recoveryFixture(t)
	recClient := remoteagentv1connect.NewRecoveryServiceClient(http.DefaultClient, recTS.url)
	pendingRec := buildRecovery(t, recHead, recKey, 0xaa)
	pendingRecID := submitRecovery(t, recClient, pendingRec)

	vetoTS, vetoHead, vetoKey, _ := recoveryFixture(t)
	vetoClient := remoteagentv1connect.NewRecoveryServiceClient(http.DefaultClient, vetoTS.url)
	vetoRec := buildRecovery(t, vetoHead, vetoKey, 0xbb)
	vetoRecID := submitRecovery(t, vetoClient, vetoRec)
	if _, err := vetoTS.srv.store.db.ExecContext(ctx, `UPDATE security_recovery SET rejected_ms = ? WHERE recovery_id = ?`, nowMs(), vetoRecID); err != nil {
		t.Fatal(err)
	}

	type testCase struct {
		name     string
		wantCode connect.Code
		want     pb.RelayErrorReason
		call     func(t *testing.T) error
		check    func(t *testing.T, d *pb.RelayErrorDetail)
	}

	cases := []testCase{
		{
			name:     "LOGIN_FAILED",
			wantCode: connect.CodePermissionDenied,
			want:     pb.RelayErrorReason_RELAY_ERROR_REASON_LOGIN_FAILED,
			call: func(t *testing.T) error {
				_, err := ts.auth.Login(ctx, connect.NewRequest(&pb.LoginRequest{
					DeviceId:        a.cid,
					Challenge:       make([]byte, 32),
					Signature:       make([]byte, ed25519.SignatureSize),
					ProtocolVersion: relayproto.Version,
				}))
				return err
			},
		},
		{
			name:     "PROTOCOL_MISMATCH",
			wantCode: connect.CodeFailedPrecondition,
			want:     pb.RelayErrorReason_RELAY_ERROR_REASON_PROTOCOL_MISMATCH,
			call: func(t *testing.T) error {
				_, err := ts.auth.Login(ctx, connect.NewRequest(&pb.LoginRequest{
					DeviceId:        a.cid,
					ProtocolVersion: relayproto.Version + 1,
				}))
				return err
			},
			check: func(t *testing.T, d *pb.RelayErrorDetail) {
				if d.ServerRelayVersion != relayproto.Version {
					t.Fatalf("server_relay_version = %d, want %d", d.ServerRelayVersion, relayproto.Version)
				}
			},
		},
		{
			name:     "TOKEN_INVALID",
			wantCode: connect.CodeUnauthenticated,
			want:     pb.RelayErrorReason_RELAY_ERROR_REASON_TOKEN_INVALID,
			call: func(t *testing.T) error {
				unauth := remoteagentv1connect.NewClientServiceClient(http.DefaultClient, ts.url)
				_, err := unauth.ListMachines(ctx, connect.NewRequest(&pb.ListMachinesRequest{}))
				return err
			},
		},
		{
			name:     "ACCOUNT_SUSPENDED",
			wantCode: connect.CodePermissionDenied,
			want:     pb.RelayErrorReason_RELAY_ERROR_REASON_ACCOUNT_SUSPENDED,
			call: func(t *testing.T) error {
				susTS := newTestServer(t)
				sa := susTS.account(t, susTS.keys)
				if _, err := susTS.srv.store.db.ExecContext(ctx, `UPDATE accounts SET status = ? WHERE id = ?`, accountSuspended, e2e.AccountID(sa.keys.AccountPublicKey())); err != nil {
					t.Fatal(err)
				}
				_, err := sa.client.ListMachines(ctx, connect.NewRequest(&pb.ListMachinesRequest{}))
				return err
			},
		},
		{
			name:     "DEVICE_NOT_MEMBER",
			wantCode: connect.CodePermissionDenied,
			want:     pb.RelayErrorReason_RELAY_ERROR_REASON_DEVICE_NOT_MEMBER,
			call: func(t *testing.T) error {
				memTS := newTestServer(t)
				ma := memTS.account(t, memTS.keys)
				// Register an extra device directly in DB without adding to the security head.
				extraPub, extraPriv, _ := ed25519.GenerateKey(nil)
				extraID := e2e.DeviceID(extraPub)
				reg, _ := proto.Marshal(&pb.DeviceRegistration{AccountPublicKey: ma.keys.AccountPublicKey(), DevicePublicKey: extraPub, Kind: pb.DeviceKind_DEVICE_KIND_CLIENT})
				if _, err := memTS.srv.store.db.ExecContext(ctx, `INSERT INTO devices (id, account_id, public_key, kind, registration, signature, created_ms) VALUES (?, ?, ?, ?, ?, ?, ?)`,
					extraID, e2e.AccountID(ma.keys.AccountPublicKey()), extraPub, int32(pb.DeviceKind_DEVICE_KIND_CLIENT), reg, []byte{}, nowMs()); err != nil {
					t.Fatal(err)
				}
				tok := memTS.login(t, extraPriv, extraID)
				cli := remoteagentv1connect.NewClientServiceClient(http.DefaultClient, memTS.url, withToken(tok))
				_, err := cli.ListMachines(ctx, connect.NewRequest(&pb.ListMachinesRequest{}))
				return err
			},
		},
		{
			name:     "NO_SECURITY_DOMAIN",
			wantCode: connect.CodePermissionDenied,
			want:     pb.RelayErrorReason_RELAY_ERROR_REASON_NO_SECURITY_DOMAIN,
			call: func(t *testing.T) error {
				noDomTS := newTestServer(t)
				keys, _ := e2e.Derive(e2e.NewKey())
				reg, sig, pub, priv := makeRawReg(keys, pb.DeviceKind_DEVICE_KIND_CLIENT)
				res, err := noDomTS.auth.RegisterDevice(ctx, connect.NewRequest(&pb.RegisterDeviceRequest{
					ProtocolVersion: relayproto.Version,
					Registration:    reg,
					Signature:       sig,
					SetupToken:      "setup",
				}))
				if err != nil {
					t.Fatal(err)
				}
				_ = pub
				tok := noDomTS.login(t, priv, res.Msg.DeviceId)
				cli := remoteagentv1connect.NewClientServiceClient(http.DefaultClient, noDomTS.url, withToken(tok))
				_, err = cli.ListMachines(ctx, connect.NewRequest(&pb.ListMachinesRequest{}))
				return err
			},
		},
		{
			name:     "NOT_AUTHORITY",
			wantCode: connect.CodePermissionDenied,
			want:     pb.RelayErrorReason_RELAY_ERROR_REASON_NOT_AUTHORITY,
			call: func(t *testing.T) error {
				_, err := nonAuthPairing.CreatePairing(ctx, connect.NewRequest(&pb.CreatePairingRequest{
					Commit: make([]byte, 32),
				}))
				return err
			},
		},
		{
			name:     "NOT_AUTHORITY_DeleteMachineRecords",
			wantCode: connect.CodePermissionDenied,
			want:     pb.RelayErrorReason_RELAY_ERROR_REASON_NOT_AUTHORITY,
			call: func(t *testing.T) error {
				_, err := a.client.DeleteMachineRecords(ctx, connect.NewRequest(&pb.DeleteMachineRecordsRequest{MachineId: a.mid}))
				return err
			},
		},
		{
			name:     "CLOCK_AHEAD",
			wantCode: connect.CodePermissionDenied,
			want:     pb.RelayErrorReason_RELAY_ERROR_REASON_CLOCK_AHEAD,
			call: func(t *testing.T) error {
				clkTS := newTestServer(t)
				keys, _ := e2e.Derive(e2e.NewKey())
				reg, sig, pub, priv := makeRawReg(keys, pb.DeviceKind_DEVICE_KIND_CLIENT)
				res, err := clkTS.auth.RegisterDevice(ctx, connect.NewRequest(&pb.RegisterDeviceRequest{
					ProtocolVersion: relayproto.Version,
					Registration:    reg,
					Signature:       sig,
					SetupToken:      "setup",
				}))
				if err != nil {
					t.Fatal(err)
				}
				tok := clkTS.login(t, priv, res.Msg.DeviceId)
				cli := remoteagentv1connect.NewClientServiceClient(http.DefaultClient, clkTS.url, withToken(tok))
				_, xpub, err := deviceauth.NewXKey()
				if err != nil {
					t.Fatal(err)
				}
				member, err := devicesec.NewMember(pub, xpub, devicesec.Client)
				if err != nil {
					t.Fatal(err)
				}
				_, recPub := newRecoveryKey(t, 0xcc)
				futureGenesis, err := deviceauth.BuildGenesis(
					e2e.AccountID(keys.AccountPublicKey()),
					devicesec.Digest("test/domain", []byte("clock-ahead")),
					member,
					recPub,
					make([]byte, 32),
					priv,
					uint64(time.Now().Add(time.Hour).UnixMilli()),
				)
				if err != nil {
					t.Fatal(err)
				}
				raw, _ := futureGenesis.MarshalBinary()
				_, err = cli.PinSecurityDomain(ctx, connect.NewRequest(&pb.PinSecurityDomainRequest{GenesisState: raw}))
				return err
			},
			check: func(t *testing.T, d *pb.RelayErrorDetail) {
				if d.ServerTimeMs <= 0 {
					t.Fatalf("server_time_ms = %d, want > 0", d.ServerTimeMs)
				}
			},
		},
		{
			name:     "SECURITY_CHAIN_FULL",
			wantCode: connect.CodeResourceExhausted,
			want:     pb.RelayErrorReason_RELAY_ERROR_REASON_SECURITY_CHAIN_FULL,
			call: func(t *testing.T) error {
				return checkRecoveryEpoch(devicesec.MaxStates + 1)
			},
		},
		{
			name:     "SETUP_TOKEN_REQUIRED",
			wantCode: connect.CodePermissionDenied,
			want:     pb.RelayErrorReason_RELAY_ERROR_REASON_SETUP_TOKEN_REQUIRED,
			call: func(t *testing.T) error {
				keys, _ := e2e.Derive(e2e.NewKey())
				reg, sig, _, _ := makeRawReg(keys, pb.DeviceKind_DEVICE_KIND_CLIENT)
				_, err := ts.auth.RegisterDevice(ctx, connect.NewRequest(&pb.RegisterDeviceRequest{
					ProtocolVersion: relayproto.Version,
					Registration:    reg,
					Signature:       sig,
					SetupToken:      "wrong",
				}))
				return err
			},
		},
		{
			name:     "SIGNUP_POW_REQUIRED",
			wantCode: connect.CodeFailedPrecondition,
			want:     pb.RelayErrorReason_RELAY_ERROR_REASON_SIGNUP_POW_REQUIRED,
			call: func(t *testing.T) error {
				keys, _ := e2e.Derive(e2e.NewKey())
				reg, sig, _, _ := makeRawReg(keys, pb.DeviceKind_DEVICE_KIND_CLIENT)
				_, err := openTS.auth.RegisterDevice(ctx, connect.NewRequest(&pb.RegisterDeviceRequest{
					ProtocolVersion: relayproto.Version,
					Registration:    reg,
					Signature:       sig,
				}))
				return err
			},
		},
		{
			name:     "SIGNUP_POW_INVALID",
			wantCode: connect.CodePermissionDenied,
			want:     pb.RelayErrorReason_RELAY_ERROR_REASON_SIGNUP_POW_INVALID,
			call: func(t *testing.T) error {
				keys, _ := e2e.Derive(e2e.NewKey())
				reg, sig, _, _ := makeRawReg(keys, pb.DeviceKind_DEVICE_KIND_CLIENT)
				_, err := openTS.auth.RegisterDevice(ctx, connect.NewRequest(&pb.RegisterDeviceRequest{
					ProtocolVersion: relayproto.Version,
					Registration:    reg,
					Signature:       sig,
					SignupProof:     &pb.SignupProof{Challenge: make([]byte, 32), Nonce: []byte("bad")},
				}))
				return err
			},
		},
		{
			name:     "SIGNUP_TOKEN_ONLY",
			wantCode: connect.CodeFailedPrecondition,
			want:     pb.RelayErrorReason_RELAY_ERROR_REASON_SIGNUP_TOKEN_ONLY,
			call: func(t *testing.T) error {
				_, err := ts.auth.GetSignupChallenge(ctx, connect.NewRequest(&pb.GetSignupChallengeRequest{}))
				return err
			},
		},
		{
			name:     "RATE_LIMITED",
			wantCode: connect.CodeResourceExhausted,
			want:     pb.RelayErrorReason_RELAY_ERROR_REASON_RATE_LIMITED,
			call: func(t *testing.T) error {
				rlTS := newTestServer(t)
				rlTS.srv.pairing.mu.Lock()
				for range badCodesPerMinute {
					rlTS.srv.pairing.badCodes = append(rlTS.srv.pairing.badCodes, time.Now())
				}
				rlTS.srv.pairing.mu.Unlock()
				p := remoteagentv1connect.NewPairingServiceClient(http.DefaultClient, rlTS.url)
				_, err := p.PeekPairing(ctx, connect.NewRequest(&pb.PeekPairingRequest{
					Code:            "12345678",
					ProtocolVersion: relayproto.Version,
				}))
				return err
			},
		},
		{
			name:     "RELAY_DISK_LOW",
			wantCode: connect.CodeResourceExhausted,
			want:     pb.RelayErrorReason_RELAY_ERROR_REASON_RELAY_DISK_LOW,
			call: func(t *testing.T) error {
				diskTS := newTestServer(t)
				da := diskTS.account(t, diskTS.keys)
				diskTS.srv.diskLow.Store(true)
				key, _ := opaqueEnvelope(&pb.Header{Version: devicesec.Version, Kind: pb.PayloadKind_PAYLOAD_KIND_SESSION_KEY, MachineId: da.mid, SessionId: "s-disk"}, e2e.NewKey())
				_, err := da.machine.PutSession(ctx, connect.NewRequest(&pb.PutSessionRequest{SessionId: "s-disk", Key: key}))
				return err
			},
		},
		{
			name:     "RELAY_ACCOUNTS_FULL",
			wantCode: connect.CodeResourceExhausted,
			want:     pb.RelayErrorReason_RELAY_ERROR_REASON_RELAY_ACCOUNTS_FULL,
			call: func(t *testing.T) error {
				// ts already has 1 account and MaxAccounts defaults to 1.
				keys, _ := e2e.Derive(e2e.NewKey())
				reg, sig, _, _ := makeRawReg(keys, pb.DeviceKind_DEVICE_KIND_CLIENT)
				_, err := ts.auth.RegisterDevice(ctx, connect.NewRequest(&pb.RegisterDeviceRequest{
					ProtocolVersion: relayproto.Version,
					Registration:    reg,
					Signature:       sig,
					SetupToken:      "setup",
				}))
				return err
			},
		},
		{
			name:     "QUOTA_DEVICES",
			wantCode: connect.CodeResourceExhausted,
			want:     pb.RelayErrorReason_RELAY_ERROR_REASON_QUOTA_DEVICES,
			call: func(t *testing.T) error {
				p := planOf("trial")
				return p.check(accountUsage{devices: p.devices + 1}, growsDevices)
			},
		},
		{
			name:     "QUOTA_MACHINES",
			wantCode: connect.CodeResourceExhausted,
			want:     pb.RelayErrorReason_RELAY_ERROR_REASON_QUOTA_MACHINES,
			call: func(t *testing.T) error {
				p := planOf("trial")
				return p.check(accountUsage{machines: p.machines + 1}, growsDevices)
			},
		},
		{
			name:     "QUOTA_SESSIONS",
			wantCode: connect.CodeResourceExhausted,
			want:     pb.RelayErrorReason_RELAY_ERROR_REASON_QUOTA_SESSIONS,
			call: func(t *testing.T) error {
				p := planOf("trial")
				return p.check(accountUsage{sessions: p.sessions + 1}, growsSessions)
			},
		},
		{
			name:     "QUOTA_STORAGE",
			wantCode: connect.CodeResourceExhausted,
			want:     pb.RelayErrorReason_RELAY_ERROR_REASON_QUOTA_STORAGE,
			call: func(t *testing.T) error {
				p := planOf("trial")
				return p.check(accountUsage{eventBytes: p.eventBytes + 1}, growsEvents)
			},
		},
		{
			name:     "PAIRING_INVALID_OR_EXPIRED",
			wantCode: connect.CodeNotFound,
			want:     pb.RelayErrorReason_RELAY_ERROR_REASON_PAIRING_INVALID_OR_EXPIRED,
			call: func(t *testing.T) error {
				_, err := anonPairing.PeekPairing(ctx, connect.NewRequest(&pb.PeekPairingRequest{
					Code:            "99999999",
					ProtocolVersion: relayproto.Version,
				}))
				return err
			},
		},
		{
			name:     "PAIRING_ALREADY_JOINED",
			wantCode: connect.CodeFailedPrecondition,
			want:     pb.RelayErrorReason_RELAY_ERROR_REASON_PAIRING_ALREADY_JOINED,
			call: func(t *testing.T) error {
				_, err := anonPairing.JoinPairing(ctx, connect.NewRequest(&pb.JoinPairingRequest{
					ProtocolVersion:     relayproto.Version,
					Code:                createdPair.Msg.Code,
					JoinerPublic:        pairKey.Public(),
					DevicePublicKey:     pairKey.Public(),
					DeviceHpkePublicKey: pairKey.Public(),
					JoinerKind:          pb.DeviceKind_DEVICE_KIND_CLIENT,
				}))
				return err
			},
		},
		{
			name:     "RECOVERY_INVALID",
			wantCode: connect.CodeNotFound,
			want:     pb.RelayErrorReason_RELAY_ERROR_REASON_RECOVERY_INVALID,
			call: func(t *testing.T) error {
				_, err := recoveryClient.CompleteRecovery(ctx, connect.NewRequest(&pb.CompleteRecoveryRequest{
					RecoveryId: "nonexistent-recovery",
					Signature:  make([]byte, ed25519.SignatureSize),
				}))
				return err
			},
		},
		{
			name:     "RECOVERY_NOT_READY",
			wantCode: connect.CodeFailedPrecondition,
			want:     pb.RelayErrorReason_RELAY_ERROR_REASON_RECOVERY_NOT_READY,
			call: func(t *testing.T) error {
				sig := ed25519.Sign(pendingRec.ed, append([]byte("ra3/recovery-complete\x00"), []byte(pendingRecID)...))
				_, err := recClient.CompleteRecovery(ctx, connect.NewRequest(&pb.CompleteRecoveryRequest{
					RecoveryId: pendingRecID,
					Signature:  sig,
				}))
				return err
			},
		},
		{
			name:     "RECOVERY_REJECTED",
			wantCode: connect.CodeFailedPrecondition,
			want:     pb.RelayErrorReason_RELAY_ERROR_REASON_RECOVERY_REJECTED,
			call: func(t *testing.T) error {
				sig := ed25519.Sign(vetoRec.ed, append([]byte("ra3/recovery-complete\x00"), []byte(vetoRecID)...))
				_, err := vetoClient.CompleteRecovery(ctx, connect.NewRequest(&pb.CompleteRecoveryRequest{
					RecoveryId: vetoRecID,
					Signature:  sig,
				}))
				return err
			},
		},
	}

	covered := map[pb.RelayErrorReason]bool{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call(t)
			if err == nil {
				t.Fatalf("expected error with reason %v, got nil", tc.want)
			}
			if got := connect.CodeOf(err); got != tc.wantCode {
				t.Fatalf("connect code = %v, want %v (err=%v)", got, tc.wantCode, err)
			}
			d := extractRelayErrorDetail(err)
			if d == nil {
				t.Fatalf("missing RelayErrorDetail on error %v", err)
			}
			if d.Reason != tc.want {
				t.Fatalf("reason = %v, want %v (err=%v)", d.Reason, tc.want, err)
			}
			if tc.check != nil {
				tc.check(t, d)
			}
			covered[tc.want] = true
		})
	}

	// Ensure every non-UNSPECIFIED RelayErrorReason enum value has at least one server test case.
	for num, name := range pb.RelayErrorReason_name {
		r := pb.RelayErrorReason(num)
		if r == pb.RelayErrorReason_RELAY_ERROR_REASON_UNSPECIFIED {
			continue
		}
		if !covered[r] {
			t.Errorf("RelayErrorReason %s (%d) has no server test coverage", name, num)
		}
	}
}
