package security

import (
	"bytes"
	"errors"
	"slices"

	"github.com/Lewin671/remote-agent/shared/devicesec"
)

const (
	storeCodecVersion uint32 = 4
	storeMagicRecord         = "RA4REC\x00"
	storeMagicOp             = "RA4OP\x00"
	storeMagicIntent         = "RA4INT\x00"
	storeMagicCand           = "RA4CND\x00"
	storeMagicProfile        = "RA4PRF\x00"
	storeMagicNotify         = "RA4NTF\x00"

	maxStoreBlobBytes = 2 << 20
	maxStoreStringLen = 64 << 10
	maxStoreListCount = 8192
)

var errStoreCorrupt = errors.New("security: corrupt persisted record")

type scWriter struct {
	devicesec.Encoder
}

func newSCWriter(magic string) scWriter {
	var w scWriter
	w.Raw([]byte(magic))
	w.U32(storeCodecVersion)
	return w
}

//go:noinline
func (w *scWriter) bool(v bool) {
	if v {
		w.B = append(w.B, 1)
	} else {
		w.B = append(w.B, 0)
	}
}

//go:noinline
func (w *scWriter) pin(p devicesec.Pin) {
	w.Str(p.Account)
	w.Raw(p.Domain[:])
	w.Raw(p.Genesis[:])
}

//go:noinline
func (w *scWriter) stateProofs(states, proofs [][]byte) {
	w.U32(uint32(len(states)))
	for i, s := range states {
		w.LP(s)
		var rp []byte
		if i < len(proofs) {
			rp = proofs[i]
		}
		w.LP(rp)
	}
}

type scReader struct {
	b   []byte
	err error
}

func newSCReader(raw []byte, maxLen int, magic string) (scReader, bool) {
	if len(raw) == 0 || len(raw) > maxLen {
		return scReader{err: errStoreCorrupt}, false
	}
	r := scReader{b: raw}
	if len(r.b) < len(magic) || string(r.b[:len(magic)]) != magic {
		return scReader{err: errStoreCorrupt}, false
	}
	r.b = r.b[len(magic):]
	if r.u32() != storeCodecVersion || r.err != nil {
		return scReader{err: errStoreCorrupt}, false
	}
	return r, true
}

func (r *scReader) u8() byte {
	if r.err != nil || len(r.b) < 1 {
		r.err = errStoreCorrupt
		return 0
	}
	v := r.b[0]
	r.b = r.b[1:]
	return v
}

func (r *scReader) bool() bool {
	v := r.u8()
	if r.err != nil || (v != 0 && v != 1) {
		r.err = errStoreCorrupt
		return false
	}
	return v == 1
}

//go:noinline
func (r *scReader) u32() uint32 {
	if r.err != nil || len(r.b) < 4 {
		r.err = errStoreCorrupt
		return 0
	}
	n := uint32(r.b[0])<<24 | uint32(r.b[1])<<16 | uint32(r.b[2])<<8 | uint32(r.b[3])
	r.b = r.b[4:]
	return n
}

//go:noinline
func (r *scReader) u64() uint64 {
	if r.err != nil || len(r.b) < 8 {
		r.err = errStoreCorrupt
		return 0
	}
	var n uint64
	for _, c := range r.b[:8] {
		n = (n << 8) | uint64(c)
	}
	r.b = r.b[8:]
	return n
}

func (r *scReader) i64() int64 { return int64(r.u64()) }

//go:noinline
func (r *scReader) raw(n int) []byte {
	if r.err != nil || n < 0 || len(r.b) < n {
		r.err = errStoreCorrupt
		return nil
	}
	out := slices.Clone(r.b[:n])
	r.b = r.b[n:]
	return out
}

func (r *scReader) hash(dst []byte) {
	b := r.raw(32)
	if r.err != nil || len(dst) != 32 {
		r.err = errStoreCorrupt
		return
	}
	copy(dst, b)
}

func (r *scReader) pin(p *devicesec.Pin) {
	p.Account = r.str()
	r.hash(p.Domain[:])
	r.hash(p.Genesis[:])
}

//go:noinline
func (r *scReader) lp(max int) []byte {
	n := r.u32()
	if r.err != nil || uint64(n) > uint64(max) || int(n) > len(r.b) {
		r.err = errStoreCorrupt
		return nil
	}
	if n == 0 {
		return nil
	}
	return r.raw(int(n))
}

//go:noinline
func (r *scReader) str() string {
	b := r.lp(maxStoreStringLen)
	if r.err != nil || len(b) == 0 {
		return ""
	}
	return string(b)
}

//go:noinline
func (r *scReader) stateProofs(requireNonEmpty bool) (states, proofs [][]byte) {
	sc := r.u32()
	if r.err != nil || (requireNonEmpty && sc == 0) || sc > devicesec.MaxStates {
		r.err = errStoreCorrupt
		return nil, nil
	}
	states = make([][]byte, sc)
	proofs = make([][]byte, sc)
	for i := range sc {
		states[i] = r.lp(devicesec.MaxStateBytes)
		proofs[i] = r.lp(devicesec.MaxStateBytes)
	}
	return states, proofs
}

//go:noinline
func (r *scReader) done() error {
	if r.err != nil || len(r.b) != 0 {
		return errStoreCorrupt
	}
	return nil
}

func pinValid(p devicesec.Pin) bool {
	return devicesec.IsDeviceID(p.Account) && p.Domain != (devicesec.Hash{}) && p.Genesis != (devicesec.Hash{})
}

func hexStr(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[2*i] = digits[c>>4]
		out[2*i+1] = digits[c&0x0f]
	}
	return string(out)
}

// PublicProfileStorageKey returns the canonical storage key for a pinned account profile.
func PublicProfileStorageKey(server string, pin devicesec.Pin) string {
	return server + "|" + pin.Account + "|" + hexStr(pin.Genesis[:])
}

// PairingCandidateStorageKey returns the canonical storage key for a pending pairing candidate.
func PairingCandidateStorageKey(server, pairingID string) string {
	return server + "|" + pairingID
}

// ContentKeyEntry stores one unlocked 32-byte content secret for a ContentEpoch.
type ContentKeyEntry struct {
	Epoch  uint64
	Secret [32]byte
}

// PersistedRecord is Core's canonical v4 persisted security record.
type PersistedRecord struct {
	Pin                    devicesec.Pin
	MemberID               string
	Member                 bool
	Server                 string
	AccountPublicKey       []byte
	States                 [][]byte
	RecoveryProofs         [][]byte
	Lease                  []byte
	FloorMs                int64
	ContentKeys            []ContentKeyEntry
	NeedsRecoveryRotation  bool
	RecoveryObjectsPending bool
}

// Clone returns a deep copy of r.
func (r *PersistedRecord) Clone() *PersistedRecord {
	if r == nil {
		return nil
	}
	cp := *r
	cp.AccountPublicKey, cp.Lease, cp.ContentKeys = slices.Clone(r.AccountPublicKey), slices.Clone(r.Lease), slices.Clone(r.ContentKeys)
	cp.States = make([][]byte, len(r.States))
	for i, s := range r.States {
		cp.States[i] = slices.Clone(s)
	}
	cp.RecoveryProofs = make([][]byte, len(r.RecoveryProofs))
	for i, p := range r.RecoveryProofs {
		cp.RecoveryProofs[i] = slices.Clone(p)
	}
	return &cp
}

// Encode serializes r in canonical v4 format.
//
//go:noinline
func (r *PersistedRecord) Encode() ([]byte, error) {
	if r == nil || !pinValid(r.Pin) || !devicesec.IsDeviceID(r.MemberID) ||
		len(r.States) == 0 || len(r.States) > devicesec.MaxStates ||
		(len(r.RecoveryProofs) != 0 && len(r.RecoveryProofs) != len(r.States)) {
		return nil, devicesec.ErrInvalid
	}
	w := newSCWriter(storeMagicRecord)
	w.pin(r.Pin)
	w.Str(r.MemberID)
	w.bool(r.Member)
	w.Str(r.Server)
	w.LP(r.AccountPublicKey)
	w.stateProofs(r.States, r.RecoveryProofs)
	w.LP(r.Lease)
	w.U64(uint64(r.FloorMs))
	w.U32(uint32(len(r.ContentKeys)))
	for _, ck := range r.ContentKeys {
		w.U64(ck.Epoch)
		w.Raw(ck.Secret[:])
	}
	w.bool(r.NeedsRecoveryRotation)
	w.bool(r.RecoveryObjectsPending)
	return w.B, nil
}

// DecodePersistedRecord decodes and cryptographically verifies a persisted v4 record.
//
//go:noinline
func DecodePersistedRecord(raw []byte) (*PersistedRecord, error) {
	br, ok := newSCReader(raw, maxStoreBlobBytes, storeMagicRecord)
	if !ok {
		return nil, errStoreCorrupt
	}
	var r PersistedRecord
	br.pin(&r.Pin)
	r.MemberID, r.Member, r.Server, r.AccountPublicKey = br.str(), br.bool(), br.str(), br.lp(64)
	r.States, r.RecoveryProofs = br.stateProofs(true)
	r.Lease, r.FloorMs = br.lp(512), br.i64()
	kc := br.u32()
	if br.err != nil || kc > maxStoreListCount {
		return nil, errStoreCorrupt
	}
	if kc > 0 {
		r.ContentKeys = make([]ContentKeyEntry, kc)
		for i := range kc {
			r.ContentKeys[i].Epoch = br.u64()
			br.hash(r.ContentKeys[i].Secret[:])
		}
	}
	r.NeedsRecoveryRotation, r.RecoveryObjectsPending = br.bool(), br.bool()
	if err := br.done(); err != nil {
		return nil, err
	}
	if !pinValid(r.Pin) || !devicesec.IsDeviceID(r.MemberID) {
		return nil, devicesec.ErrInvalid
	}
	states, _, _, err := parseAndVerifyChain(r.Pin, r.States, r.RecoveryProofs)
	if err != nil {
		return nil, err
	}
	if _, inHead := states[len(states)-1].Core.Member(r.MemberID); inHead != r.Member {
		return nil, errStoreCorrupt
	}
	if len(r.Lease) > 0 {
		if l, err := devicesec.ParseLease(r.Lease); err != nil || l.Verify(states[len(states)-1]) != nil {
			return nil, devicesec.ErrInvalid
		}
	}
	if len(r.AccountPublicKey) == 32 && devicesec.ID(r.AccountPublicKey) != r.Pin.Account {
		return nil, devicesec.ErrUntrusted
	}
	return &r, nil
}

// PersistedOpPair captures pairing metadata attached to a state approval op.
type PersistedOpPair struct {
	PairingID, CandidateID, ReplacesDeviceID string
	CandidateEd, CandidateX, PairKey         []byte
	CandidateKind                            uint32
	ExpiresMs                                int64
}

// PersistedCreateDraft captures durable local account creation draft material.
type PersistedCreateDraft struct {
	AccountPublicKey, Lease []byte
	EdSlot, XSlot           string
	StoragePersisted        bool
	States                  [][]byte
}

// PersistedRecoveryDraft captures durable local recovery draft material.
type PersistedRecoveryDraft struct {
	AccountID, EdSlot, XSlot, RetiringMemberID, RecoveryID string
	AccountPublicKey, CandidateEdPub, CandidateXPub        []byte
	RecoveryEd, Secret, TargetState, Proof                 []byte
	CompleteRequest, RetirementRequest                     []byte
	Domain, Genesis                                        [32]byte
	NowMs, ReadyMs                                         int64
	States, RecoveryProofs                                 [][]byte
}

// PersistedOperation is Core's canonical v4 durable pending security operation.
type PersistedOperation struct {
	ID, Server, MemberID, Kind, Phase, Action, TargetDeviceID string
	Revision                                                  uint64
	Parent                                                    [32]byte
	CodeSaved, OfferRejected, StoragePersisted                bool
	Signed, Delivery, Retry, RecoveryEd                       []byte
	Pair                                                      *PersistedOpPair
	Create                                                    *PersistedCreateDraft
	Recovery                                                  *PersistedRecoveryDraft
}

func phaseOrder(p string) int {
	switch p {
	case "PREPARED":
		return 1
	case "PUBLISHED":
		return 2
	case "DELIVERED":
		return 3
	default:
		return 0
	}
}

// Encode serializes op in canonical v4 format.
//
//go:noinline
func (op *PersistedOperation) Encode() ([]byte, error) {
	if op == nil || op.ID == "" || op.Server == "" || op.MemberID == "" || phaseOrder(op.Phase) == 0 {
		return nil, devicesec.ErrInvalid
	}
	w := newSCWriter(storeMagicOp)
	w.Str(op.ID)
	w.U64(op.Revision)
	w.Str(op.Server)
	w.Str(op.MemberID)
	w.Str(op.Kind)
	w.Raw(op.Parent[:])
	w.Str(op.Phase)
	w.bool(op.CodeSaved)
	w.LP(op.Signed)
	w.LP(op.Delivery)
	w.LP(op.Retry)
	w.Str(op.Action)
	w.Str(op.TargetDeviceID)
	w.LP(op.RecoveryEd)
	w.bool(op.OfferRejected)
	w.bool(op.StoragePersisted)
	w.bool(op.Pair != nil)
	if p := op.Pair; p != nil {
		w.Str(p.PairingID)
		w.Str(p.CandidateID)
		w.LP(p.CandidateEd)
		w.LP(p.CandidateX)
		w.U32(p.CandidateKind)
		w.LP(p.PairKey)
		w.U64(uint64(p.ExpiresMs))
		w.Str(p.ReplacesDeviceID)
	}
	w.bool(op.Create != nil)
	if c := op.Create; c != nil {
		w.LP(c.AccountPublicKey)
		w.Str(c.EdSlot)
		w.Str(c.XSlot)
		w.bool(c.StoragePersisted)
		w.U32(uint32(len(c.States)))
		for _, s := range c.States {
			w.LP(s)
		}
		w.LP(c.Lease)
	}
	w.bool(op.Recovery != nil)
	if r := op.Recovery; r != nil {
		w.Str(r.AccountID)
		w.LP(r.AccountPublicKey)
		w.Raw(r.Domain[:])
		w.Raw(r.Genesis[:])
		w.Str(r.EdSlot)
		w.Str(r.XSlot)
		w.LP(r.CandidateEdPub)
		w.LP(r.CandidateXPub)
		w.LP(r.RecoveryEd)
		w.LP(r.Secret)
		w.U64(uint64(r.NowMs))
		w.Str(r.RetiringMemberID)
		w.Str(r.RecoveryID)
		w.U64(uint64(r.ReadyMs))
		w.LP(r.TargetState)
		w.LP(r.Proof)
		w.stateProofs(r.States, r.RecoveryProofs)
		w.LP(r.CompleteRequest)
		w.LP(r.RetirementRequest)
	}
	return w.B, nil
}

// DecodePersistedOperation decodes a v4 persisted operation.
//
//go:noinline
func DecodePersistedOperation(raw []byte) (*PersistedOperation, error) {
	br, ok := newSCReader(raw, maxStoreBlobBytes, storeMagicOp)
	if !ok {
		return nil, errStoreCorrupt
	}
	var op PersistedOperation
	op.ID, op.Revision, op.Server, op.MemberID, op.Kind = br.str(), br.u64(), br.str(), br.str(), br.str()
	br.hash(op.Parent[:])
	op.Phase, op.CodeSaved = br.str(), br.bool()
	op.Signed, op.Delivery, op.Retry = br.lp(maxStoreBlobBytes), br.lp(maxStoreBlobBytes), br.lp(maxStoreBlobBytes)
	op.Action, op.TargetDeviceID, op.RecoveryEd = br.str(), br.str(), br.lp(64)
	op.OfferRejected, op.StoragePersisted = br.bool(), br.bool()
	if br.bool() {
		op.Pair = &PersistedOpPair{
			PairingID: br.str(), CandidateID: br.str(), CandidateEd: br.lp(64), CandidateX: br.lp(64),
			CandidateKind: br.u32(), PairKey: br.lp(64), ExpiresMs: br.i64(), ReplacesDeviceID: br.str(),
		}
	}
	if br.bool() {
		c := &PersistedCreateDraft{AccountPublicKey: br.lp(64), EdSlot: br.str(), XSlot: br.str(), StoragePersisted: br.bool()}
		sc := br.u32()
		if br.err != nil || sc > devicesec.MaxStates {
			return nil, errStoreCorrupt
		}
		c.States = make([][]byte, sc)
		for i := range sc {
			c.States[i] = br.lp(devicesec.MaxStateBytes)
		}
		c.Lease, op.Create = br.lp(512), c
	}
	if br.bool() {
		r := &PersistedRecoveryDraft{AccountID: br.str(), AccountPublicKey: br.lp(64)}
		br.hash(r.Domain[:])
		br.hash(r.Genesis[:])
		r.EdSlot, r.XSlot = br.str(), br.str()
		r.CandidateEdPub, r.CandidateXPub, r.RecoveryEd, r.Secret = br.lp(64), br.lp(64), br.lp(64), br.lp(64)
		r.NowMs, r.RetiringMemberID, r.RecoveryID, r.ReadyMs = br.i64(), br.str(), br.str(), br.i64()
		r.TargetState, r.Proof = br.lp(devicesec.MaxStateBytes), br.lp(devicesec.MaxStateBytes)
		r.States, r.RecoveryProofs = br.stateProofs(false)
		r.CompleteRequest, r.RetirementRequest = br.lp(maxStoreBlobBytes), br.lp(maxStoreBlobBytes)
		op.Recovery = r
	}
	if err := br.done(); err != nil {
		return nil, err
	}
	if op.ID == "" || op.Server == "" || op.MemberID == "" || phaseOrder(op.Phase) == 0 {
		return nil, errStoreCorrupt
	}
	switch op.Kind {
	case "create", "state", "lease", "recovery", "offer", "delete-records":
		return &op, nil
	default:
		return nil, errStoreCorrupt
	}
}

// ValidateProgress verifies monotonic invariants before saving next over prev.
func (op *PersistedOperation) ValidateProgress(next *PersistedOperation) error {
	if op == nil || next == nil || next.ID != op.ID || next.Kind != op.Kind ||
		next.Server != op.Server || next.MemberID != op.MemberID || next.Parent != op.Parent ||
		next.Revision != op.Revision+1 || phaseOrder(next.Phase) < phaseOrder(op.Phase) ||
		(op.CodeSaved && !next.CodeSaved) ||
		(len(op.Signed) > 0 && !bytes.Equal(op.Signed, next.Signed)) ||
		(len(op.Delivery) > 0 && !bytes.Equal(op.Delivery, next.Delivery)) {
		return devicesec.ErrInvalid
	}
	return nil
}

// PersistedRegistrationIntent stores approved registration state across crashes before completion.
type PersistedRegistrationIntent struct {
	Server, DeviceID, PairingID, CandidateToken, EdSlot, XSlot string
	RegisterRequest, AccountPublicKey                          []byte
	Replace, ReusesDeviceEd                                    bool
	Record                                                     *PersistedRecord
}

// Encode serializes intent in canonical v4 format.
//
//go:noinline
func (in *PersistedRegistrationIntent) Encode() ([]byte, error) {
	if in == nil || in.Server == "" || !devicesec.IsDeviceID(in.DeviceID) || len(in.RegisterRequest) == 0 || in.Record == nil {
		return nil, devicesec.ErrInvalid
	}
	recBytes, err := in.Record.Encode()
	if err != nil {
		return nil, err
	}
	w := newSCWriter(storeMagicIntent)
	w.Str(in.Server)
	w.Str(in.DeviceID)
	w.LP(in.RegisterRequest)
	w.bool(in.Replace)
	w.LP(in.AccountPublicKey)
	w.Str(in.PairingID)
	w.Str(in.CandidateToken)
	w.Str(in.EdSlot)
	w.Str(in.XSlot)
	w.bool(in.ReusesDeviceEd)
	w.LP(recBytes)
	return w.B, nil
}

// DecodePersistedRegistrationIntent decodes and validates a v4 registration intent.
//
//go:noinline
func DecodePersistedRegistrationIntent(raw []byte) (*PersistedRegistrationIntent, error) {
	br, ok := newSCReader(raw, maxStoreBlobBytes, storeMagicIntent)
	if !ok {
		return nil, errStoreCorrupt
	}
	in := PersistedRegistrationIntent{
		Server: br.str(), DeviceID: br.str(), RegisterRequest: br.lp(maxStoreBlobBytes), Replace: br.bool(),
		AccountPublicKey: br.lp(64), PairingID: br.str(), CandidateToken: br.str(),
		EdSlot: br.str(), XSlot: br.str(), ReusesDeviceEd: br.bool(),
	}
	recRaw := br.lp(maxStoreBlobBytes)
	if err := br.done(); err != nil {
		return nil, err
	}
	rec, err := DecodePersistedRecord(recRaw)
	if err != nil {
		return nil, err
	}
	if in.Server == "" || in.DeviceID != rec.MemberID || !rec.Member || len(in.AccountPublicKey) != 32 ||
		devicesec.ID(in.AccountPublicKey) != rec.Pin.Account {
		return nil, errStoreCorrupt
	}
	in.Record = rec
	return &in, nil
}

// PersistedPairingCandidate stores pre-join candidate key slots before approval arrives.
type PersistedPairingCandidate struct {
	Server, PairingID, EdSlot, XSlot string
	EdPublic, XPublic                []byte
	ReusesDeviceEd                   bool
}

// Encode serializes c in canonical v4 format.
func (c *PersistedPairingCandidate) Encode() ([]byte, error) {
	if c == nil || c.Server == "" || c.PairingID == "" || len(c.EdPublic) != 32 || len(c.XPublic) != 32 {
		return nil, devicesec.ErrInvalid
	}
	w := newSCWriter(storeMagicCand)
	w.Str(c.Server)
	w.Str(c.PairingID)
	w.Str(c.EdSlot)
	w.Str(c.XSlot)
	w.LP(c.EdPublic)
	w.LP(c.XPublic)
	w.bool(c.ReusesDeviceEd)
	return w.B, nil
}

// DecodePersistedPairingCandidate decodes a v4 pairing candidate.
func DecodePersistedPairingCandidate(raw []byte) (*PersistedPairingCandidate, error) {
	br, ok := newSCReader(raw, 4096, storeMagicCand)
	if !ok {
		return nil, errStoreCorrupt
	}
	c := PersistedPairingCandidate{
		Server: br.str(), PairingID: br.str(), EdSlot: br.str(), XSlot: br.str(),
		EdPublic: br.lp(64), XPublic: br.lp(64), ReusesDeviceEd: br.bool(),
	}
	if err := br.done(); err != nil {
		return nil, err
	}
	if c.Server == "" || c.PairingID == "" || len(c.EdPublic) != 32 || len(c.XPublic) != 32 {
		return nil, errStoreCorrupt
	}
	return &c, nil
}

// PersistedPublicProfile stores the verified public account identity key bound to a pin.
type PersistedPublicProfile struct {
	Server           string
	Pin              devicesec.Pin
	AccountPublicKey [32]byte
}

// Encode serializes p in canonical v4 format.
func (p *PersistedPublicProfile) Encode() ([]byte, error) {
	if p == nil || p.Server == "" || !pinValid(p.Pin) || devicesec.ID(p.AccountPublicKey[:]) != p.Pin.Account {
		return nil, devicesec.ErrUntrusted
	}
	w := newSCWriter(storeMagicProfile)
	w.Str(p.Server)
	w.pin(p.Pin)
	w.Raw(p.AccountPublicKey[:])
	return w.B, nil
}

// DecodePersistedPublicProfile decodes and checks p against its pin.
func DecodePersistedPublicProfile(raw []byte) (*PersistedPublicProfile, error) {
	br, ok := newSCReader(raw, 2048, storeMagicProfile)
	if !ok {
		return nil, errStoreCorrupt
	}
	var p PersistedPublicProfile
	p.Server = br.str()
	br.pin(&p.Pin)
	br.hash(p.AccountPublicKey[:])
	if err := br.done(); err != nil {
		return nil, err
	}
	if p.Server == "" || !devicesec.IsDeviceID(p.Pin.Account) || devicesec.ID(p.AccountPublicKey[:]) != p.Pin.Account {
		return nil, devicesec.ErrUntrusted
	}
	return &p, nil
}

// PersistedNotifyRecord stores the verified head state and notify-only purpose key for background pushes.
type PersistedNotifyRecord struct {
	Pin        devicesec.Pin
	HeadState  []byte
	PurposeKey [32]byte
}

// Encode serializes n in canonical v4 format.
//
//go:noinline
func (n *PersistedNotifyRecord) Encode() ([]byte, error) {
	if n == nil || !pinValid(n.Pin) {
		return nil, devicesec.ErrInvalid
	}
	if s, err := devicesec.ParseState(n.HeadState); err != nil || n.Pin.Verify(s) != nil {
		return nil, devicesec.ErrUntrusted
	}
	w := newSCWriter(storeMagicNotify)
	w.pin(n.Pin)
	w.LP(n.HeadState)
	w.Raw(n.PurposeKey[:])
	return w.B, nil
}

// DecodePersistedNotifyRecord decodes and verifies n.
//
//go:noinline
func DecodePersistedNotifyRecord(raw []byte) (*PersistedNotifyRecord, error) {
	br, ok := newSCReader(raw, devicesec.MaxStateBytes+512, storeMagicNotify)
	if !ok {
		return nil, errStoreCorrupt
	}
	var n PersistedNotifyRecord
	br.pin(&n.Pin)
	n.HeadState = br.lp(devicesec.MaxStateBytes)
	br.hash(n.PurposeKey[:])
	if err := br.done(); err != nil {
		return nil, err
	}
	if s, err := devicesec.ParseState(n.HeadState); err != nil || n.Pin.Verify(s) != nil {
		return nil, devicesec.ErrUntrusted
	}
	return &n, nil
}
