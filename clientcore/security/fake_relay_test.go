package security

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"sync"

	"github.com/Lewin671/remote-agent/clientcore/gen/litewire"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/shared/devicesec"
)

type fakePairingRow struct {
	id         string
	code       string
	accountID  string
	initiator  string
	approverEd []byte
	commit     []byte
	expiresMs  int64
	state      v1.PairingState
	joinerPub  []byte
	joinerKind v1.DeviceKind
	candEd     []byte
	candX      []byte
	candToken  string
	initPub    []byte
	trust      []byte
	payload    *v1.Envelope
	approval   []byte
	appSig     []byte
}

type fakeRecoveryRow struct {
	id        string
	accountID string
	stmt      devicesec.RecoveryStatement
	target    devicesec.State
	newRecEd  []byte
	readyMs   int64
	rejected  bool
	completed bool
}

type fakeRelay struct {
	mu           sync.Mutex
	nowMs        int64
	powBits      uint32
	challengeSeq int

	accounts       map[string][]byte // accountID -> accountPub
	devices        map[string]*v1.Device
	devicePubs     map[string][]byte
	deviceAcct     map[string]string
	removedDevices map[string]bool
	machines       map[string]map[string]bool // accountID -> machineID -> exists
	states         map[string][]*v1.SecurityState
	leases         map[string][]byte
	prefs          map[string]*v1.AccountPrefsObject
	tokens         map[string]string // token -> deviceID
	loginChal      map[string]string // chalHex -> deviceID

	pairings   map[string]*fakePairingRow
	byPairCode map[string]*fakePairingRow

	recChals   map[string]bool
	recoveries map[string]*fakeRecoveryRow

	forceApproveNotFound bool
	bumpMetaOnPutOnce    bool
	failNextMethod       corev1.SecurityRpcMethod
	failNextCode         int32
}

func newFakeRelay(nowMs int64) *fakeRelay {
	return &fakeRelay{
		nowMs:          nowMs,
		accounts:       make(map[string][]byte),
		devices:        make(map[string]*v1.Device),
		devicePubs:     make(map[string][]byte),
		deviceAcct:     make(map[string]string),
		removedDevices: make(map[string]bool),
		machines:       make(map[string]map[string]bool),
		states:         make(map[string][]*v1.SecurityState),
		leases:         make(map[string][]byte),
		prefs:          make(map[string]*v1.AccountPrefsObject),
		tokens:         make(map[string]string),
		loginChal:      make(map[string]string),
		pairings:       make(map[string]*fakePairingRow),
		byPairCode:     make(map[string]*fakePairingRow),
		recChals:       make(map[string]bool),
		recoveries:     make(map[string]*fakeRecoveryRow),
	}
}

func (r *fakeRelay) addMachine(acctID string, m devicesec.Member) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.machines[acctID] == nil {
		r.machines[acctID] = make(map[string]bool)
	}
	r.machines[acctID][m.ID] = true
	r.deviceAcct[m.ID] = acctID
	r.devicePubs[m.ID] = slices.Clone(m.EdPublic[:])
	r.devices[m.ID] = &v1.Device{
		Id:   m.ID,
		Kind: v1.DeviceKind_DEVICE_KIND_MACHINE,
	}
}

func (r *fakeRelay) appendStateLocked(acctID string, sBytes []byte) *rpcError {
	st, err := devicesec.ParseState(sBytes)
	if err != nil {
		return &rpcError{code: connectCodeInvalidArgument, msg: err.Error()}
	}
	cur := r.states[acctID]
	if len(cur) == 0 {
		if st.Core.Epoch != 1 {
			return &rpcError{code: connectCodeFailedPrecondition, msg: "expected genesis"}
		}
		if _, err := st.Pin(); err != nil {
			return &rpcError{code: connectCodeInvalidArgument, msg: err.Error()}
		}
	} else {
		parent, err := devicesec.ParseState(cur[len(cur)-1].State)
		if err != nil {
			return &rpcError{code: connectCodeUnknown, msg: err.Error()}
		}
		if err := devicesec.VerifyTransition(parent, st, nil); err != nil {
			return &rpcError{code: connectCodeFailedPrecondition, msg: err.Error()}
		}
		if rec := r.recoveries[acctID]; rec != nil && !rec.completed && !rec.rejected {
			rec.rejected = true
		}
		for _, rem := range st.Core.Removed {
			if r.deviceAcct[rem] == acctID {
				delete(r.devices, rem)
				r.removedDevices[rem] = true
			}
		}
	}
	r.states[acctID] = append(cur, &v1.SecurityState{State: slices.Clone(sBytes)})
	return nil
}

func (r *fakeRelay) auth(token string) (string, string, *rpcError) {
	if token == "" {
		return "", "", &rpcError{code: connectCodeUnauthenticated, msg: "missing token"}
	}
	devID, ok := r.tokens[token]
	if !ok {
		return "", "", &rpcError{code: connectCodeUnauthenticated, msg: "invalid token"}
	}
	if r.removedDevices[devID] {
		return "", "", &rpcError{code: connectCodePermissionDenied, msg: "removed"}
	}
	return devID, r.deviceAcct[devID], nil
}

func (r *fakeRelay) isAuthorityLocked(acctID, devID string) bool {
	cur := r.states[acctID]
	if len(cur) == 0 {
		return false
	}
	head, err := devicesec.ParseState(cur[len(cur)-1].State)
	if err != nil {
		return false
	}
	return head.Core.AuthorityID == devID
}

func lwMarshalDevice(d *v1.Device) []byte {
	var b []byte
	b = litewire.AppendString(b, 1, d.Id)
	b = litewire.AppendInt32(b, 2, int32(d.Kind))
	b = litewire.AppendVarint(b, 3, uint64(d.CreatedMs))
	if d.Meta != nil {
		b = litewire.AppendMessage(b, 4, d.Meta)
	}
	b = litewire.AppendVarint(b, 5, d.MetaRevision)
	b = litewire.AppendVarint(b, 6, uint64(d.LastSeenMs))
	return b
}

func (r *fakeRelay) call(method corev1.SecurityRpcMethod, token string, reqBytes []byte) ([]byte, *rpcError) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.failNextMethod == method {
		r.failNextMethod = corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_UNSPECIFIED
		code := r.failNextCode
		if code == 0 {
			code = connectCodeUnavailable
		}
		return nil, &rpcError{code: code, msg: "injected", network: code == connectCodeUnavailable}
	}

	switch method {
	case corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_SIGNUP_CHALLENGE:
		ch := make([]byte, 32)
		_, _ = rand.Read(ch)
		var out []byte
		out = litewire.AppendBytes(out, 1, ch)
		out = litewire.AppendVarint(out, 2, uint64(r.powBits))
		out = litewire.AppendVarint(out, 3, uint64(r.nowMs+60_000))
		return out, nil

	case corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_REGISTER_DEVICE:
		var rawReg, sig, app, appSig []byte
		var proof v1.SignupProof
		b := reqBytes
		for len(b) > 0 {
			num, wtyp, err := litewire.ReadTag(&b)
			if err != nil {
				return nil, &rpcError{code: connectCodeInvalidArgument, msg: err.Error()}
			}
			switch {
			case num == 1 && wtyp == litewire.BytesType:
				rawReg, _ = litewire.ReadBytes(&b)
			case num == 2 && wtyp == litewire.BytesType:
				sig, _ = litewire.ReadBytes(&b)
			case num == 4 && wtyp == litewire.BytesType:
				_ = litewire.ReadMessage(&b, &proof)
			case num == 5 && wtyp == litewire.BytesType:
				app, _ = litewire.ReadBytes(&b)
			case num == 6 && wtyp == litewire.BytesType:
				appSig, _ = litewire.ReadBytes(&b)
			default:
				_ = litewire.Skip(&b, num, wtyp)
			}
		}
		var reg v1.DeviceRegistration
		if err := reg.Unmarshal(rawReg); err != nil || len(reg.AccountPublicKey) != 32 || len(reg.DevicePublicKey) != 32 {
			return nil, &rpcError{code: connectCodeInvalidArgument, msg: "bad registration"}
		}
		acctID := devicesec.ID(reg.AccountPublicKey)
		devID := devicesec.ID(reg.DevicePublicKey)
		if _, exists := r.accounts[acctID]; !exists {
			if r.powBits > 0 {
				if len(proof.Challenge) == 0 {
					return nil, &rpcError{code: connectCodeFailedPrecondition, msg: "pow required"}
				}
				buf := append(slices.Clone(proof.Challenge), proof.Nonce...)
				sum := sha256.Sum256(buf)
				for bit := range r.powBits {
					if (sum[bit>>3] & (128 >> (bit & 7))) != 0 {
						return nil, &rpcError{code: connectCodeFailedPrecondition, msg: "bad pow"}
					}
				}
			}
			signMsg := append([]byte(registerContext), rawReg...)
			if !ed25519.Verify(reg.AccountPublicKey, signMsg, sig) {
				return nil, &rpcError{code: connectCodePermissionDenied, msg: "bad root sig"}
			}
			r.accounts[acctID] = slices.Clone(reg.AccountPublicKey)
		} else {
			if len(app) == 0 {
				return nil, &rpcError{code: connectCodePermissionDenied, msg: "missing approval"}
			}
			ap, err := VerifyPairApproval(app, appSig)
			if err != nil || ap.AccountId != acctID || !bytes.Equal(ap.CandidateDevicePublicKey, reg.DevicePublicKey) {
				return nil, &rpcError{code: connectCodePermissionDenied, msg: "bad approval"}
			}
		}
		delete(r.removedDevices, devID)
		r.deviceAcct[devID] = acctID
		r.devicePubs[devID] = slices.Clone(reg.DevicePublicKey)
		r.devices[devID] = &v1.Device{Id: devID, Kind: reg.Kind}
		return litewire.AppendString(nil, 1, devID), nil

	case corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_CHALLENGE:
		var devID string
		b := reqBytes
		for len(b) > 0 {
			num, wtyp, err := litewire.ReadTag(&b)
			if err != nil {
				return nil, &rpcError{code: connectCodeInvalidArgument, msg: err.Error()}
			}
			if num == 1 && wtyp == litewire.BytesType {
				devID, _ = litewire.ReadString(&b)
			} else {
				_ = litewire.Skip(&b, num, wtyp)
			}
		}
		ch := make([]byte, 32)
		r.challengeSeq++
		ch[0] = byte(r.challengeSeq >> 8)
		ch[1] = byte(r.challengeSeq)
		r.loginChal[hex.EncodeToString(ch)] = devID
		return litewire.AppendBytes(nil, 1, ch), nil

	case corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_LOGIN:
		var devID string
		var chal, sig []byte
		b := reqBytes
		for len(b) > 0 {
			num, wtyp, err := litewire.ReadTag(&b)
			if err != nil {
				return nil, &rpcError{code: connectCodeInvalidArgument, msg: err.Error()}
			}
			switch {
			case num == 1 && wtyp == litewire.BytesType:
				devID, _ = litewire.ReadString(&b)
			case num == 2 && wtyp == litewire.BytesType:
				chal, _ = litewire.ReadBytes(&b)
			case num == 3 && wtyp == litewire.BytesType:
				sig, _ = litewire.ReadBytes(&b)
			default:
				_ = litewire.Skip(&b, num, wtyp)
			}
		}
		pub := r.devicePubs[devID]
		if r.devices[devID] == nil || len(pub) != 32 || r.removedDevices[devID] {
			return nil, &rpcError{code: connectCodePermissionDenied, msg: "unknown or removed device"}
		}
		if r.loginChal[hex.EncodeToString(chal)] != devID {
			return nil, &rpcError{code: connectCodePermissionDenied, msg: "bad challenge"}
		}
		msg := append([]byte(loginContext), chal...)
		if !ed25519.Verify(pub, msg, sig) {
			return nil, &rpcError{code: connectCodePermissionDenied, msg: "bad sig"}
		}
		tok := "tok-" + devID + "-" + hex.EncodeToString(chal[:4])
		r.tokens[tok] = devID
		var out []byte
		out = litewire.AppendString(out, 1, tok)
		out = litewire.AppendVarint(out, 2, uint64(r.nowMs+7*dayMs))
		return out, nil

	case corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_PIN_SECURITY_DOMAIN:
		devID, acctID, aErr := r.auth(token)
		if aErr != nil {
			return nil, aErr
		}
		var genBytes []byte
		b := reqBytes
		for len(b) > 0 {
			num, wtyp, err := litewire.ReadTag(&b)
			if err != nil {
				return nil, &rpcError{code: connectCodeInvalidArgument, msg: err.Error()}
			}
			if num == 1 && wtyp == litewire.BytesType {
				genBytes, _ = litewire.ReadBytes(&b)
			} else {
				_ = litewire.Skip(&b, num, wtyp)
			}
		}
		gen, err := devicesec.ParseState(genBytes)
		if err != nil || gen.Core.Account != acctID || gen.Core.AuthorityID != devID {
			return nil, &rpcError{code: connectCodePermissionDenied, msg: "bad genesis"}
		}
		if len(r.states[acctID]) == 0 {
			if rErr := r.appendStateLocked(acctID, genBytes); rErr != nil {
				return nil, rErr
			}
		}
		return nil, nil

	case corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_PUT_SECURITY_STATE:
		devID, acctID, aErr := r.auth(token)
		if aErr != nil {
			return nil, aErr
		}
		var reqStates []*v1.SecurityState
		var reqLease []byte
		b := reqBytes
		for len(b) > 0 {
			num, wtyp, err := litewire.ReadTag(&b)
			if err != nil {
				return nil, &rpcError{code: connectCodeInvalidArgument, msg: err.Error()}
			}
			switch {
			case num == 1 && wtyp == litewire.BytesType:
				el := &v1.SecurityState{}
				_ = litewire.ReadMessage(&b, el)
				reqStates = append(reqStates, el)
			case num == 2 && wtyp == litewire.BytesType:
				reqLease, _ = litewire.ReadBytes(&b)
			default:
				_ = litewire.Skip(&b, num, wtyp)
			}
		}
		for _, ss := range reqStates {
			if !r.isAuthorityLocked(acctID, devID) {
				return nil, &rpcError{code: connectCodePermissionDenied, msg: "not authority"}
			}
			st, err := devicesec.ParseState(ss.State)
			if err != nil {
				return nil, &rpcError{code: connectCodeInvalidArgument, msg: err.Error()}
			}
			if len(r.states[acctID]) > 0 {
				curHead, _ := devicesec.ParseState(r.states[acctID][len(r.states[acctID])-1].State)
				if st.Core.Epoch <= curHead.Core.Epoch {
					continue
				}
			}
			if rErr := r.appendStateLocked(acctID, ss.State); rErr != nil {
				return nil, rErr
			}
		}
		if len(reqLease) > 0 {
			if len(r.states[acctID]) == 0 {
				return nil, &rpcError{code: connectCodeFailedPrecondition, msg: "no states"}
			}
			headBytes := r.states[acctID][len(r.states[acctID])-1].State
			head, _ := devicesec.ParseState(headBytes)
			if head.Core.AuthorityID != devID {
				return nil, &rpcError{code: connectCodePermissionDenied, msg: "not authority for lease"}
			}
			l, err := devicesec.ParseLease(reqLease)
			if err != nil || l.Verify(head) != nil {
				return nil, &rpcError{code: connectCodeInvalidArgument, msg: "bad lease"}
			}
			r.leases[acctID] = slices.Clone(reqLease)
		}
		cur := r.states[acctID]
		headBytes := cur[len(cur)-1].State
		head, _ := devicesec.ParseState(headBytes)
		var out []byte
		out = litewire.AppendVarint(out, 1, head.Core.Epoch)
		out = litewire.AppendBytes(out, 2, headBytes)
		return out, nil

	case corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_SECURITY_STATES:
		_, acctID, aErr := r.auth(token)
		if aErr != nil {
			return nil, aErr
		}
		var afterEpoch, limit uint64
		b := reqBytes
		for len(b) > 0 {
			num, wtyp, _ := litewire.ReadTag(&b)
			switch {
			case num == 1 && wtyp == litewire.VarintType:
				afterEpoch, _ = litewire.ReadVarint(&b)
			case num == 2 && wtyp == litewire.VarintType:
				limit, _ = litewire.ReadVarint(&b)
			default:
				_ = litewire.Skip(&b, num, wtyp)
			}
		}
		all := r.states[acctID]
		var slice []*v1.SecurityState
		for _, ss := range all {
			st, _ := devicesec.ParseState(ss.State)
			if st.Core.Epoch > afterEpoch {
				slice = append(slice, ss)
				if limit > 0 && len(slice) >= int(limit) {
					break
				}
			}
		}
		var headEpoch uint64
		var headState devicesec.State
		var hasHeadState bool
		if len(all) > 0 {
			h, hErr := devicesec.ParseState(all[len(all)-1].State)
			if hErr == nil {
				headEpoch = h.Core.Epoch
				headState = h
				hasHeadState = true
			}
		}
		var recPending bool
		var recReady uint64
		if rec := r.recoveries[acctID]; rec != nil && !rec.completed && !rec.rejected {
			recPending = true
			recReady = uint64(max(0, rec.readyMs))
		}
		var out []byte
		for _, ss := range slice {
			out = litewire.AppendMessage(out, 1, ss)
		}
		out = litewire.AppendVarint(out, 2, headEpoch)
		out = litewire.AppendBool(out, 3, recPending)
		out = litewire.AppendVarint(out, 4, recReady)
		if lease := r.leases[acctID]; len(lease) > 0 && hasHeadState {
			if l, err := devicesec.ParseLease(lease); err == nil && l.Verify(headState) == nil {
				out = litewire.AppendBytes(out, 5, lease)
			}
		}
		return out, nil

	case corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_CREATE_PAIRING:
		devID, acctID, aErr := r.auth(token)
		if aErr != nil {
			return nil, aErr
		}
		if !r.isAuthorityLocked(acctID, devID) {
			return nil, &rpcError{code: connectCodePermissionDenied, msg: "not authority"}
		}
		var commit []byte
		b := reqBytes
		for len(b) > 0 {
			num, wtyp, _ := litewire.ReadTag(&b)
			if num == 1 && wtyp == litewire.BytesType {
				commit, _ = litewire.ReadBytes(&b)
			} else {
				_ = litewire.Skip(&b, num, wtyp)
			}
		}
		if len(commit) != 32 {
			return nil, &rpcError{code: connectCodeInvalidArgument, msg: "bad commit"}
		}
		pid := devicesec.ID(append([]byte("pair-id"), commit[:25]...))
		code := "12345678"
		for r.byPairCode[code] != nil {
			code = u64Str(uint64(len(r.pairings) + 12345679))
		}
		row := &fakePairingRow{
			id:         pid,
			code:       code,
			accountID:  acctID,
			initiator:  devID,
			approverEd: slices.Clone(r.devicePubs[devID]),
			commit:     slices.Clone(commit),
			expiresMs:  r.nowMs + 5*60*1000,
			state:      v1.PairingState_PAIRING_STATE_OPEN,
		}
		r.pairings[pid] = row
		r.byPairCode[code] = row
		var out []byte
		out = litewire.AppendString(out, 1, pid)
		out = litewire.AppendString(out, 2, code)
		out = litewire.AppendVarint(out, 3, uint64(row.expiresMs))
		out = litewire.AppendBytes(out, 4, row.approverEd)
		return out, nil

	case corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_PEEK_PAIRING:
		var code string
		b := reqBytes
		for len(b) > 0 {
			num, wtyp, _ := litewire.ReadTag(&b)
			if num == 1 && wtyp == litewire.BytesType {
				code, _ = litewire.ReadString(&b)
			} else {
				_ = litewire.Skip(&b, num, wtyp)
			}
		}
		row := r.byPairCode[code]
		if row == nil || row.state == v1.PairingState_PAIRING_STATE_CANCELED {
			return nil, &rpcError{code: connectCodeNotFound, msg: "no pairing"}
		}
		var out []byte
		out = litewire.AppendString(out, 1, row.id)
		out = litewire.AppendBytes(out, 2, row.commit)
		return out, nil

	case corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_JOIN_PAIRING:
		var code string
		var jPub, devPub, devX []byte
		var jKind v1.DeviceKind
		b := reqBytes
		for len(b) > 0 {
			num, wtyp, _ := litewire.ReadTag(&b)
			switch {
			case num == 1 && wtyp == litewire.BytesType:
				code, _ = litewire.ReadString(&b)
			case num == 2 && wtyp == litewire.BytesType:
				jPub, _ = litewire.ReadBytes(&b)
			case num == 3 && wtyp == litewire.VarintType:
				v, _ := litewire.ReadVarint(&b)
				jKind = v1.DeviceKind(v)
			case num == 4 && wtyp == litewire.BytesType:
				devPub, _ = litewire.ReadBytes(&b)
			case num == 5 && wtyp == litewire.BytesType:
				devX, _ = litewire.ReadBytes(&b)
			default:
				_ = litewire.Skip(&b, num, wtyp)
			}
		}
		row := r.byPairCode[code]
		if row == nil || row.state != v1.PairingState_PAIRING_STATE_OPEN {
			return nil, &rpcError{code: connectCodeFailedPrecondition, msg: "not open"}
		}
		row.state = v1.PairingState_PAIRING_STATE_JOINED
		row.joinerPub = slices.Clone(jPub)
		row.joinerKind = jKind
		row.candEd = slices.Clone(devPub)
		row.candX = slices.Clone(devX)
		row.candToken = "cand-" + row.id
		var out []byte
		out = litewire.AppendString(out, 1, row.id)
		out = litewire.AppendBytes(out, 2, row.commit)
		out = litewire.AppendString(out, 3, row.candToken)
		return out, nil

	case corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_REVEAL_PAIRING:
		devID, _, aErr := r.auth(token)
		if aErr != nil {
			return nil, aErr
		}
		var pid string
		var initPub, trust []byte
		b := reqBytes
		for len(b) > 0 {
			num, wtyp, _ := litewire.ReadTag(&b)
			switch {
			case num == 1 && wtyp == litewire.BytesType:
				pid, _ = litewire.ReadString(&b)
			case num == 2 && wtyp == litewire.BytesType:
				initPub, _ = litewire.ReadBytes(&b)
			case num == 3 && wtyp == litewire.BytesType:
				trust, _ = litewire.ReadBytes(&b)
			default:
				_ = litewire.Skip(&b, num, wtyp)
			}
		}
		row := r.pairings[pid]
		if row == nil || row.initiator != devID {
			return nil, &rpcError{code: connectCodeNotFound, msg: "no pairing"}
		}
		row.initPub = slices.Clone(initPub)
		row.trust = slices.Clone(trust)
		row.state = v1.PairingState_PAIRING_STATE_REVEALED
		return nil, nil

	case corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_APPROVE_PAIRING:
		devID, _, aErr := r.auth(token)
		if aErr != nil {
			return nil, aErr
		}
		var pid string
		var payload v1.Envelope
		var hasPayload bool
		var app, appSig []byte
		b := reqBytes
		for len(b) > 0 {
			num, wtyp, _ := litewire.ReadTag(&b)
			switch {
			case num == 1 && wtyp == litewire.BytesType:
				pid, _ = litewire.ReadString(&b)
			case num == 2 && wtyp == litewire.BytesType:
				hasPayload = true
				_ = litewire.ReadMessage(&b, &payload)
			case num == 3 && wtyp == litewire.BytesType:
				app, _ = litewire.ReadBytes(&b)
			case num == 4 && wtyp == litewire.BytesType:
				appSig, _ = litewire.ReadBytes(&b)
			default:
				_ = litewire.Skip(&b, num, wtyp)
			}
		}
		if r.forceApproveNotFound {
			r.forceApproveNotFound = false
			return nil, &rpcError{code: connectCodeNotFound, msg: "expired"}
		}
		row := r.pairings[pid]
		if row == nil || row.initiator != devID {
			return nil, &rpcError{code: connectCodeNotFound, msg: "no pairing"}
		}
		if row.state == v1.PairingState_PAIRING_STATE_DELIVERED {
			return nil, nil
		}
		if row.state == v1.PairingState_PAIRING_STATE_APPROVED && bytes.Equal(row.approval, app) && bytes.Equal(row.appSig, appSig) {
			return nil, nil
		}
		if hasPayload {
			row.payload = &payload
		}
		row.approval = slices.Clone(app)
		row.appSig = slices.Clone(appSig)
		row.state = v1.PairingState_PAIRING_STATE_APPROVED
		return nil, nil

	case corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_PAIRING:
		var pid, candTok string
		var received bool
		b := reqBytes
		for len(b) > 0 {
			num, wtyp, _ := litewire.ReadTag(&b)
			switch {
			case num == 1 && wtyp == litewire.BytesType:
				pid, _ = litewire.ReadString(&b)
			case num == 2 && wtyp == litewire.BytesType:
				candTok, _ = litewire.ReadString(&b)
			case num == 3 && wtyp == litewire.VarintType:
				v, _ := litewire.ReadVarint(&b)
				received = litewire.DecodeBool(v)
			default:
				_ = litewire.Skip(&b, num, wtyp)
			}
		}
		row := r.pairings[pid]
		if row == nil {
			return nil, &rpcError{code: connectCodeNotFound, msg: "no pairing"}
		}
		if candTok != "" {
			if candTok != row.candToken {
				return nil, &rpcError{code: connectCodePermissionDenied, msg: "bad token"}
			}
			if received {
				row.payload = nil
				row.state = v1.PairingState_PAIRING_STATE_DELIVERED
			}
		} else {
			devID, _, aErr := r.auth(token)
			if aErr != nil || devID != row.initiator {
				return nil, &rpcError{code: connectCodePermissionDenied, msg: "unauthorized"}
			}
		}
		var out []byte
		out = litewire.AppendInt32(out, 1, int32(row.state))
		out = litewire.AppendBytes(out, 2, row.commit)
		out = litewire.AppendBytes(out, 3, row.joinerPub)
		out = litewire.AppendInt32(out, 4, int32(row.joinerKind))
		out = litewire.AppendBytes(out, 5, row.initPub)
		if row.payload != nil {
			out = litewire.AppendMessage(out, 6, row.payload)
		}
		out = litewire.AppendBytes(out, 7, row.candEd)
		out = litewire.AppendBytes(out, 8, row.candX)
		out = litewire.AppendBytes(out, 9, row.trust)
		return out, nil

	case corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_CANCEL_PAIRING:
		var pid string
		b := reqBytes
		for len(b) > 0 {
			num, wtyp, _ := litewire.ReadTag(&b)
			if num == 1 && wtyp == litewire.BytesType {
				pid, _ = litewire.ReadString(&b)
			} else {
				_ = litewire.Skip(&b, num, wtyp)
			}
		}
		if row := r.pairings[pid]; row != nil {
			row.state = v1.PairingState_PAIRING_STATE_CANCELED
			delete(r.byPairCode, row.code)
		}
		return nil, nil

	case corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_LIST_DEVICES:
		_, acctID, aErr := r.auth(token)
		if aErr != nil {
			return nil, aErr
		}
		var out []byte
		for id, d := range r.devices {
			if r.deviceAcct[id] == acctID {
				out = litewire.AppendBytes(out, 1, lwMarshalDevice(d))
			}
		}
		return out, nil

	case corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_LIST_MACHINES:
		_, acctID, aErr := r.auth(token)
		if aErr != nil {
			return nil, aErr
		}
		var out []byte
		for mid, ok := range r.machines[acctID] {
			if ok {
				out = litewire.AppendMessage(out, 1, &v1.Machine{Id: mid})
			}
		}
		return out, nil

	case corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_PUT_DEVICE_META:
		_, acctID, aErr := r.auth(token)
		if aErr != nil {
			return nil, aErr
		}
		var devID string
		var expRev uint64
		var meta v1.Envelope
		var hasMeta bool
		b := reqBytes
		for len(b) > 0 {
			num, wtyp, _ := litewire.ReadTag(&b)
			switch {
			case num == 1 && wtyp == litewire.BytesType:
				devID, _ = litewire.ReadString(&b)
			case num == 2 && wtyp == litewire.VarintType:
				expRev, _ = litewire.ReadVarint(&b)
			case num == 3 && wtyp == litewire.BytesType:
				hasMeta = true
				_ = litewire.ReadMessage(&b, &meta)
			default:
				_ = litewire.Skip(&b, num, wtyp)
			}
		}
		dev := r.devices[devID]
		if dev == nil || r.deviceAcct[devID] != acctID {
			return nil, &rpcError{code: connectCodeNotFound, msg: "no device"}
		}
		if r.bumpMetaOnPutOnce {
			r.bumpMetaOnPutOnce = false
			dev.MetaRevision++
		}
		applied := expRev == dev.MetaRevision
		if applied && hasMeta {
			dev.Meta = &meta
			dev.MetaRevision++
		}
		return litewire.AppendBool(nil, 1, applied), nil

	case corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_DELETE_MACHINE_RECORDS:
		devID, acctID, aErr := r.auth(token)
		if aErr != nil {
			return nil, aErr
		}
		if !r.isAuthorityLocked(acctID, devID) {
			return nil, &rpcError{code: connectCodePermissionDenied, msg: "not authority"}
		}
		var mid string
		b := reqBytes
		for len(b) > 0 {
			num, wtyp, _ := litewire.ReadTag(&b)
			if num == 1 && wtyp == litewire.BytesType {
				mid, _ = litewire.ReadString(&b)
			} else {
				_ = litewire.Skip(&b, num, wtyp)
			}
		}
		delete(r.machines[acctID], mid)
		return nil, nil

	case corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_DELETE_ACCOUNT:
		devID, acctID, aErr := r.auth(token)
		if aErr != nil {
			return nil, aErr
		}
		if !r.isAuthorityLocked(acctID, devID) {
			return nil, &rpcError{code: connectCodePermissionDenied, msg: "not authority"}
		}
		delete(r.accounts, acctID)
		delete(r.states, acctID)
		delete(r.leases, acctID)
		delete(r.recoveries, acctID)
		for id, a := range r.deviceAcct {
			if a == acctID {
				r.removedDevices[id] = true
				delete(r.devices, id)
				delete(r.deviceAcct, id)
				delete(r.devicePubs, id)
			}
		}
		return nil, nil

	case corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_RECOVERY_CHALLENGE:
		ch := make([]byte, 32)
		r.challengeSeq++
		ch[0] = byte(r.challengeSeq >> 8)
		ch[1] = byte(r.challengeSeq)
		r.recChals[hex.EncodeToString(ch)] = true
		return litewire.AppendBytes(nil, 1, ch), nil

	case corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_RECOVERY_CONTEXT:
		var recEd, chal, sig []byte
		var recID string
		b := reqBytes
		for len(b) > 0 {
			num, wtyp, _ := litewire.ReadTag(&b)
			switch {
			case num == 1 && wtyp == litewire.BytesType:
				recEd, _ = litewire.ReadBytes(&b)
			case num == 2 && wtyp == litewire.BytesType:
				recID, _ = litewire.ReadString(&b)
			case num == 3 && wtyp == litewire.BytesType:
				chal, _ = litewire.ReadBytes(&b)
			case num == 4 && wtyp == litewire.BytesType:
				sig, _ = litewire.ReadBytes(&b)
			default:
				_ = litewire.Skip(&b, num, wtyp)
			}
		}
		if !r.recChals[hex.EncodeToString(chal)] {
			return nil, &rpcError{code: connectCodePermissionDenied, msg: "bad rec challenge"}
		}
		msg := buildRecoveryContextSignBytes(chal, recEd, recID)
		var acctID string
		var recRow *fakeRecoveryRow
		if recID != "" {
			for _, row := range r.recoveries {
				if row.id == recID {
					recRow = row
					acctID = row.accountID
					break
				}
			}
			if recRow == nil {
				return nil, &rpcError{code: connectCodeNotFound, msg: "no recovery"}
			}
			if !ed25519.Verify(recRow.stmt.NewMember.EdPublic[:], msg, sig) {
				return nil, &rpcError{code: connectCodePermissionDenied, msg: "bad rec sig"}
			}
		} else {
			if len(recEd) != 32 || !ed25519.Verify(recEd, msg, sig) {
				return nil, &rpcError{code: connectCodePermissionDenied, msg: "bad rec key sig"}
			}
			for a, cur := range r.states {
				if len(cur) > 0 {
					head, _ := devicesec.ParseState(cur[len(cur)-1].State)
					if bytes.Equal(head.Core.RecoveryEd[:], recEd) {
						acctID = a
						recRow = r.recoveries[a]
						break
					}
				}
			}
			if acctID == "" {
				return nil, &rpcError{code: connectCodeNotFound, msg: "unknown rec key"}
			}
		}
		var out []byte
		out = litewire.AppendString(out, 1, acctID)
		for _, ss := range r.states[acctID] {
			out = litewire.AppendMessage(out, 2, ss)
		}
		if recRow != nil {
			var phase v1.GetRecoveryContextResponse_Phase
			switch {
			case recRow.rejected:
				phase = v1.GetRecoveryContextResponse_PHASE_REJECTED
			case recRow.completed:
				phase = v1.GetRecoveryContextResponse_PHASE_PUBLISHED
			case r.nowMs < recRow.readyMs:
				phase = v1.GetRecoveryContextResponse_PHASE_PENDING
			default:
				phase = v1.GetRecoveryContextResponse_PHASE_READY
			}
			out = litewire.AppendInt32(out, 3, int32(phase))
			out = litewire.AppendVarint(out, 4, uint64(max(0, recRow.readyMs)))
			out = litewire.AppendString(out, 5, recRow.id)
		}
		out = litewire.AppendBytes(out, 6, r.accounts[acctID])
		return out, nil

	case corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_SUBMIT_RECOVERY:
		var stmtBytes, stBytes []byte
		b := reqBytes
		for len(b) > 0 {
			num, wtyp, _ := litewire.ReadTag(&b)
			switch {
			case num == 1 && wtyp == litewire.BytesType:
				stmtBytes, _ = litewire.ReadBytes(&b)
			case num == 2 && wtyp == litewire.BytesType:
				stBytes, _ = litewire.ReadBytes(&b)
			default:
				_ = litewire.Skip(&b, num, wtyp)
			}
		}
		stmt, err := devicesec.ParseRecoveryStatement(stmtBytes)
		if err != nil {
			return nil, &rpcError{code: connectCodeInvalidArgument, msg: err.Error()}
		}
		st, err := devicesec.ParseState(stBytes)
		if err != nil {
			return nil, &rpcError{code: connectCodeInvalidArgument, msg: err.Error()}
		}
		cur := r.states[stmt.Account]
		if len(cur) == 0 {
			return nil, &rpcError{code: connectCodeFailedPrecondition, msg: "no states"}
		}
		head, _ := devicesec.ParseState(cur[len(cur)-1].State)
		if err := devicesec.VerifyTransition(head, st, &stmt); err != nil {
			return nil, &rpcError{code: connectCodePermissionDenied, msg: err.Error()}
		}
		rid := "rec-" + hex.EncodeToString(stmt.Nonce[:6])
		readyMs := r.nowMs + 2*dayMs
		r.recoveries[stmt.Account] = &fakeRecoveryRow{
			id:        rid,
			accountID: stmt.Account,
			stmt:      stmt,
			target:    st,
			newRecEd:  slices.Clone(stmt.NewRecoveryEd[:]),
			readyMs:   readyMs,
		}
		var out []byte
		out = litewire.AppendString(out, 1, rid)
		out = litewire.AppendVarint(out, 2, uint64(readyMs))
		return out, nil

	case corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_COMPLETE_RECOVERY:
		var rid string
		var sig []byte
		b := reqBytes
		for len(b) > 0 {
			num, wtyp, _ := litewire.ReadTag(&b)
			switch {
			case num == 1 && wtyp == litewire.BytesType:
				rid, _ = litewire.ReadString(&b)
			case num == 2 && wtyp == litewire.BytesType:
				sig, _ = litewire.ReadBytes(&b)
			default:
				_ = litewire.Skip(&b, num, wtyp)
			}
		}
		var rec *fakeRecoveryRow
		for _, row := range r.recoveries {
			if row.id == rid {
				rec = row
				break
			}
		}
		if rec == nil {
			return nil, &rpcError{code: connectCodeNotFound, msg: "no rec"}
		}
		label := append([]byte(recoveryDonePrefix), []byte(rec.id)...)
		if !ed25519.Verify(rec.stmt.NewMember.EdPublic[:], label, sig) {
			return nil, &rpcError{code: connectCodePermissionDenied, msg: "bad sig"}
		}
		if rec.rejected {
			return nil, &rpcError{code: connectCodeFailedPrecondition, msg: "vetoed"}
		}
		if !rec.completed {
			if r.nowMs < rec.readyMs {
				return nil, &rpcError{code: connectCodeFailedPrecondition, msg: "not ready"}
			}
			cur := r.states[rec.accountID]
			head, _ := devicesec.ParseState(cur[len(cur)-1].State)
			if err := devicesec.VerifyTransition(head, rec.target, &rec.stmt); err != nil {
				rec.rejected = true
				return nil, &rpcError{code: connectCodeFailedPrecondition, msg: err.Error()}
			}
			tb, _ := rec.target.MarshalBinary()
			sb, _ := rec.stmt.MarshalBinary()
			r.states[rec.accountID] = append(cur, &v1.SecurityState{State: tb, Recovery: sb})
			for _, rem := range rec.target.Core.Removed {
				if r.deviceAcct[rem] == rec.accountID {
					delete(r.devices, rem)
					r.removedDevices[rem] = true
				}
			}
			delete(r.removedDevices, rec.stmt.NewMember.ID)
			r.deviceAcct[rec.stmt.NewMember.ID] = rec.accountID
			r.devicePubs[rec.stmt.NewMember.ID] = slices.Clone(rec.stmt.NewMember.EdPublic[:])
			r.devices[rec.stmt.NewMember.ID] = &v1.Device{
				Id:   rec.stmt.NewMember.ID,
				Kind: v1.DeviceKind_DEVICE_KIND_CLIENT,
			}
			rec.completed = true
		}
		var out []byte
		out = litewire.AppendString(out, 1, rec.stmt.NewAuthorityID)
		out = litewire.AppendBytes(out, 2, r.accounts[rec.accountID])
		return out, nil

	case corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_ACCOUNT_PREFS:
		_, acctID, aErr := r.auth(token)
		if aErr != nil {
			return nil, aErr
		}
		var objID string
		b := reqBytes
		for len(b) > 0 {
			num, wtyp, _ := litewire.ReadTag(&b)
			if num == 1 && wtyp == litewire.BytesType {
				objID, _ = litewire.ReadString(&b)
			} else {
				_ = litewire.Skip(&b, num, wtyp)
			}
		}
		var out []byte
		if obj := r.prefs[acctID+":"+objID]; obj != nil {
			out = litewire.AppendMessage(out, 1, obj)
		}
		return out, nil

	case corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_PUT_ACCOUNT_PREFS:
		_, acctID, aErr := r.auth(token)
		if aErr != nil {
			return nil, aErr
		}
		var objID string
		var expRev uint64
		var env v1.Envelope
		var hasEnv bool
		b := reqBytes
		for len(b) > 0 {
			num, wtyp, _ := litewire.ReadTag(&b)
			switch {
			case num == 1 && wtyp == litewire.BytesType:
				objID, _ = litewire.ReadString(&b)
			case num == 2 && wtyp == litewire.VarintType:
				expRev, _ = litewire.ReadVarint(&b)
			case num == 3 && wtyp == litewire.BytesType:
				hasEnv = true
				_ = litewire.ReadMessage(&b, &env)
			default:
				_ = litewire.Skip(&b, num, wtyp)
			}
		}
		key := acctID + ":" + objID
		cur := r.prefs[key]
		var curRev uint64
		if cur != nil {
			curRev = cur.Revision
		}
		if expRev != curRev {
			return litewire.AppendBool(nil, 1, false), nil
		}
		var ep *v1.Envelope
		if hasEnv {
			ep = &env
		}
		obj := &v1.AccountPrefsObject{
			ObjectId: objID,
			Revision: curRev + 1,
			Envelope: ep,
		}
		r.prefs[key] = obj
		var out []byte
		out = litewire.AppendBool(out, 1, true)
		out = litewire.AppendMessage(out, 2, obj)
		return out, nil
	}

	return nil, &rpcError{code: connectCodeInvalidArgument, msg: "unhandled rpc"}
}
