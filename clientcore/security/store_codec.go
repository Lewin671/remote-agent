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

type binReader struct {
	b   []byte
	err error
}

func (r *binReader) u8() byte {
	if r.err != nil || len(r.b) < 1 {
		r.err = errStoreCorrupt
		return 0
	}
	v := r.b[0]
	r.b = r.b[1:]
	return v
}

func (r *binReader) bool() bool {
	v := r.u8()
	if r.err != nil || (v != 0 && v != 1) {
		r.err = errStoreCorrupt
		return false
	}
	return v == 1
}

func (r *binReader) u32() uint32 {
	if r.err != nil || len(r.b) < 4 {
		r.err = errStoreCorrupt
		return 0
	}
	n := uint32(r.b[0])<<24 | uint32(r.b[1])<<16 | uint32(r.b[2])<<8 | uint32(r.b[3])
	r.b = r.b[4:]
	return n
}

func (r *binReader) u64() uint64 {
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

func (r *binReader) i64() int64 {
	return int64(r.u64())
}

func (r *binReader) raw(n int) []byte {
	if r.err != nil || n < 0 || len(r.b) < n {
		r.err = errStoreCorrupt
		return nil
	}
	out := slices.Clone(r.b[:n])
	r.b = r.b[n:]
	return out
}

func (r *binReader) lp(max int) []byte {
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

func (r *binReader) str() string {
	b := r.lp(maxStoreStringLen)
	if r.err != nil || len(b) == 0 {
		return ""
	}
	return string(b)
}

func (r *binReader) checkMagic(magic string, ver uint32) bool {
	if r.err != nil || len(r.b) < len(magic) || string(r.b[:len(magic)]) != magic {
		r.err = errStoreCorrupt
		return false
	}
	r.b = r.b[len(magic):]
	if r.u32() != ver {
		r.err = errStoreCorrupt
		return false
	}
	return r.err == nil
}

func (r *binReader) done() error {
	if r.err != nil {
		return r.err
	}
	if len(r.b) != 0 {
		return errStoreCorrupt
	}
	return nil
}

func encodeBool(e *devicesec.Encoder, v bool) {
	if v {
		e.B = append(e.B, 1)
	} else {
		e.B = append(e.B, 0)
	}
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
	cp.AccountPublicKey = slices.Clone(r.AccountPublicKey)
	cp.States = make([][]byte, len(r.States))
	for i, s := range r.States {
		cp.States[i] = slices.Clone(s)
	}
	cp.RecoveryProofs = make([][]byte, len(r.RecoveryProofs))
	for i, p := range r.RecoveryProofs {
		cp.RecoveryProofs[i] = slices.Clone(p)
	}
	cp.Lease = slices.Clone(r.Lease)
	cp.ContentKeys = slices.Clone(r.ContentKeys)
	return &cp
}

// Encode serializes r in canonical v4 format.
func (r *PersistedRecord) Encode() ([]byte, error) {
	if r == nil || !devicesec.IsDeviceID(r.Pin.Account) || r.Pin.Domain == (devicesec.Hash{}) ||
		r.Pin.Genesis == (devicesec.Hash{}) || !devicesec.IsDeviceID(r.MemberID) ||
		len(r.States) == 0 || len(r.States) > devicesec.MaxStates ||
		(len(r.RecoveryProofs) != 0 && len(r.RecoveryProofs) != len(r.States)) {
		return nil, devicesec.ErrInvalid
	}
	var e devicesec.Encoder
	e.Raw([]byte(storeMagicRecord))
	e.U32(storeCodecVersion)
	e.Str(r.Pin.Account)
	e.Raw(r.Pin.Domain[:])
	e.Raw(r.Pin.Genesis[:])
	e.Str(r.MemberID)
	encodeBool(&e, r.Member)
	e.Str(r.Server)
	e.LP(r.AccountPublicKey)
	e.U32(uint32(len(r.States)))
	for i, s := range r.States {
		e.LP(s)
		var rp []byte
		if i < len(r.RecoveryProofs) {
			rp = r.RecoveryProofs[i]
		}
		e.LP(rp)
	}
	e.LP(r.Lease)
	e.U64(uint64(r.FloorMs))
	e.U32(uint32(len(r.ContentKeys)))
	for _, ck := range r.ContentKeys {
		e.U64(ck.Epoch)
		e.Raw(ck.Secret[:])
	}
	encodeBool(&e, r.NeedsRecoveryRotation)
	encodeBool(&e, r.RecoveryObjectsPending)
	return e.B, nil
}

// DecodePersistedRecord decodes and cryptographically verifies a persisted v4 record.
func DecodePersistedRecord(raw []byte) (*PersistedRecord, error) {
	if len(raw) == 0 || len(raw) > maxStoreBlobBytes {
		return nil, errStoreCorrupt
	}
	br := binReader{b: raw}
	if !br.checkMagic(storeMagicRecord, storeCodecVersion) {
		return nil, errStoreCorrupt
	}
	var r PersistedRecord
	r.Pin.Account = br.str()
	copy(r.Pin.Domain[:], br.raw(32))
	copy(r.Pin.Genesis[:], br.raw(32))
	r.MemberID = br.str()
	r.Member = br.bool()
	r.Server = br.str()
	r.AccountPublicKey = br.lp(64)
	sc := br.u32()
	if br.err != nil || sc == 0 || sc > devicesec.MaxStates {
		return nil, errStoreCorrupt
	}
	r.States = make([][]byte, sc)
	r.RecoveryProofs = make([][]byte, sc)
	for i := range sc {
		r.States[i] = br.lp(devicesec.MaxStateBytes)
		r.RecoveryProofs[i] = br.lp(devicesec.MaxStateBytes)
	}
	r.Lease = br.lp(512)
	r.FloorMs = br.i64()
	kc := br.u32()
	if br.err != nil || kc > maxStoreListCount {
		return nil, errStoreCorrupt
	}
	if kc > 0 {
		r.ContentKeys = make([]ContentKeyEntry, kc)
		for i := range kc {
			r.ContentKeys[i].Epoch = br.u64()
			copy(r.ContentKeys[i].Secret[:], br.raw(32))
		}
	}
	r.NeedsRecoveryRotation = br.bool()
	r.RecoveryObjectsPending = br.bool()
	if err := br.done(); err != nil {
		return nil, err
	}
	if !devicesec.IsDeviceID(r.Pin.Account) || !devicesec.IsDeviceID(r.MemberID) ||
		r.Pin.Domain == (devicesec.Hash{}) || r.Pin.Genesis == (devicesec.Hash{}) {
		return nil, devicesec.ErrInvalid
	}
	states, _, _, err := parseAndVerifyChain(r.Pin, r.States, r.RecoveryProofs)
	if err != nil {
		return nil, err
	}
	head := states[len(states)-1]
	_, inHead := head.Core.Member(r.MemberID)
	if inHead != r.Member {
		return nil, errStoreCorrupt
	}
	if len(r.Lease) > 0 {
		l, err := devicesec.ParseLease(r.Lease)
		if err != nil || l.Verify(head) != nil {
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
	PairingID        string
	CandidateID      string
	CandidateEd      []byte
	CandidateX       []byte
	CandidateKind    uint32
	PairKey          []byte
	ExpiresMs        int64
	ReplacesDeviceID string
}

// PersistedCreateDraft captures durable local account creation draft material.
type PersistedCreateDraft struct {
	AccountPublicKey []byte
	EdSlot           string
	XSlot            string
	StoragePersisted bool
	States           [][]byte
	Lease            []byte
}

// PersistedRecoveryDraft captures durable local recovery draft material.
type PersistedRecoveryDraft struct {
	AccountID         string
	AccountPublicKey  []byte
	Domain            [32]byte
	Genesis           [32]byte
	EdSlot            string
	XSlot             string
	CandidateEdPub    []byte
	CandidateXPub     []byte
	RecoveryEd        []byte
	Secret            []byte
	NowMs             int64
	RetiringMemberID  string
	RecoveryID        string
	ReadyMs           int64
	TargetState       []byte
	Proof             []byte
	States            [][]byte
	RecoveryProofs    [][]byte
	CompleteRequest   []byte
	RetirementRequest []byte
}

// PersistedOperation is Core's canonical v4 durable pending security operation.
type PersistedOperation struct {
	ID               string
	Revision         uint64
	Server           string
	MemberID         string
	Kind             string // "create", "state", "lease", "recovery", "offer", "delete-records"
	Parent           [32]byte
	Phase            string // "PREPARED", "PUBLISHED", "DELIVERED"
	CodeSaved        bool
	Signed           []byte
	Delivery         []byte
	Retry            []byte
	Action           string // "", "remove", "recovery", "handoff", "pair"
	TargetDeviceID   string
	RecoveryEd       []byte
	OfferRejected    bool
	StoragePersisted bool
	Pair             *PersistedOpPair
	Create           *PersistedCreateDraft
	Recovery         *PersistedRecoveryDraft
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
func (op *PersistedOperation) Encode() ([]byte, error) {
	if op == nil || op.ID == "" || op.Server == "" || op.MemberID == "" || phaseOrder(op.Phase) == 0 {
		return nil, devicesec.ErrInvalid
	}
	var e devicesec.Encoder
	e.Raw([]byte(storeMagicOp))
	e.U32(storeCodecVersion)
	e.Str(op.ID)
	e.U64(op.Revision)
	e.Str(op.Server)
	e.Str(op.MemberID)
	e.Str(op.Kind)
	e.Raw(op.Parent[:])
	e.Str(op.Phase)
	encodeBool(&e, op.CodeSaved)
	e.LP(op.Signed)
	e.LP(op.Delivery)
	e.LP(op.Retry)
	e.Str(op.Action)
	e.Str(op.TargetDeviceID)
	e.LP(op.RecoveryEd)
	encodeBool(&e, op.OfferRejected)
	encodeBool(&e, op.StoragePersisted)

	encodeBool(&e, op.Pair != nil)
	if op.Pair != nil {
		p := op.Pair
		e.Str(p.PairingID)
		e.Str(p.CandidateID)
		e.LP(p.CandidateEd)
		e.LP(p.CandidateX)
		e.U32(p.CandidateKind)
		e.LP(p.PairKey)
		e.U64(uint64(p.ExpiresMs))
		e.Str(p.ReplacesDeviceID)
	}

	encodeBool(&e, op.Create != nil)
	if op.Create != nil {
		c := op.Create
		e.LP(c.AccountPublicKey)
		e.Str(c.EdSlot)
		e.Str(c.XSlot)
		encodeBool(&e, c.StoragePersisted)
		e.U32(uint32(len(c.States)))
		for _, s := range c.States {
			e.LP(s)
		}
		e.LP(c.Lease)
	}

	encodeBool(&e, op.Recovery != nil)
	if op.Recovery != nil {
		r := op.Recovery
		e.Str(r.AccountID)
		e.LP(r.AccountPublicKey)
		e.Raw(r.Domain[:])
		e.Raw(r.Genesis[:])
		e.Str(r.EdSlot)
		e.Str(r.XSlot)
		e.LP(r.CandidateEdPub)
		e.LP(r.CandidateXPub)
		e.LP(r.RecoveryEd)
		e.LP(r.Secret)
		e.U64(uint64(r.NowMs))
		e.Str(r.RetiringMemberID)
		e.Str(r.RecoveryID)
		e.U64(uint64(r.ReadyMs))
		e.LP(r.TargetState)
		e.LP(r.Proof)
		e.U32(uint32(len(r.States)))
		for i, s := range r.States {
			e.LP(s)
			var rp []byte
			if i < len(r.RecoveryProofs) {
				rp = r.RecoveryProofs[i]
			}
			e.LP(rp)
		}
		e.LP(r.CompleteRequest)
		e.LP(r.RetirementRequest)
	}
	return e.B, nil
}

// DecodePersistedOperation decodes a v4 persisted operation.
func DecodePersistedOperation(raw []byte) (*PersistedOperation, error) {
	if len(raw) == 0 || len(raw) > maxStoreBlobBytes {
		return nil, errStoreCorrupt
	}
	br := binReader{b: raw}
	if !br.checkMagic(storeMagicOp, storeCodecVersion) {
		return nil, errStoreCorrupt
	}
	var op PersistedOperation
	op.ID = br.str()
	op.Revision = br.u64()
	op.Server = br.str()
	op.MemberID = br.str()
	op.Kind = br.str()
	copy(op.Parent[:], br.raw(32))
	op.Phase = br.str()
	op.CodeSaved = br.bool()
	op.Signed = br.lp(maxStoreBlobBytes)
	op.Delivery = br.lp(maxStoreBlobBytes)
	op.Retry = br.lp(maxStoreBlobBytes)
	op.Action = br.str()
	op.TargetDeviceID = br.str()
	op.RecoveryEd = br.lp(64)
	op.OfferRejected = br.bool()
	op.StoragePersisted = br.bool()

	if br.bool() {
		p := &PersistedOpPair{
			PairingID:        br.str(),
			CandidateID:      br.str(),
			CandidateEd:      br.lp(64),
			CandidateX:       br.lp(64),
			CandidateKind:    br.u32(),
			PairKey:          br.lp(64),
			ExpiresMs:        br.i64(),
			ReplacesDeviceID: br.str(),
		}
		op.Pair = p
	}
	if br.bool() {
		c := &PersistedCreateDraft{
			AccountPublicKey: br.lp(64),
			EdSlot:           br.str(),
			XSlot:            br.str(),
			StoragePersisted: br.bool(),
		}
		sc := br.u32()
		if br.err != nil || sc > devicesec.MaxStates {
			return nil, errStoreCorrupt
		}
		c.States = make([][]byte, sc)
		for i := range sc {
			c.States[i] = br.lp(devicesec.MaxStateBytes)
		}
		c.Lease = br.lp(512)
		op.Create = c
	}
	if br.bool() {
		r := &PersistedRecoveryDraft{
			AccountID:        br.str(),
			AccountPublicKey: br.lp(64),
		}
		copy(r.Domain[:], br.raw(32))
		copy(r.Genesis[:], br.raw(32))
		r.EdSlot = br.str()
		r.XSlot = br.str()
		r.CandidateEdPub = br.lp(64)
		r.CandidateXPub = br.lp(64)
		r.RecoveryEd = br.lp(64)
		r.Secret = br.lp(64)
		r.NowMs = br.i64()
		r.RetiringMemberID = br.str()
		r.RecoveryID = br.str()
		r.ReadyMs = br.i64()
		r.TargetState = br.lp(devicesec.MaxStateBytes)
		r.Proof = br.lp(devicesec.MaxStateBytes)
		sc := br.u32()
		if br.err != nil || sc > devicesec.MaxStates {
			return nil, errStoreCorrupt
		}
		r.States = make([][]byte, sc)
		r.RecoveryProofs = make([][]byte, sc)
		for i := range sc {
			r.States[i] = br.lp(devicesec.MaxStateBytes)
			r.RecoveryProofs[i] = br.lp(devicesec.MaxStateBytes)
		}
		r.CompleteRequest = br.lp(maxStoreBlobBytes)
		r.RetirementRequest = br.lp(maxStoreBlobBytes)
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
	default:
		return nil, errStoreCorrupt
	}
	return &op, nil
}

// ValidateProgress verifies monotonic invariants before saving next over prev.
func (op *PersistedOperation) ValidateProgress(next *PersistedOperation) error {
	if op == nil || next == nil {
		return devicesec.ErrInvalid
	}
	if next.ID != op.ID || next.Kind != op.Kind || next.Server != op.Server ||
		next.MemberID != op.MemberID || next.Parent != op.Parent ||
		next.Revision != op.Revision+1 {
		return devicesec.ErrInvalid
	}
	if phaseOrder(next.Phase) < phaseOrder(op.Phase) {
		return devicesec.ErrInvalid
	}
	if op.CodeSaved && !next.CodeSaved {
		return devicesec.ErrInvalid
	}
	if len(op.Signed) > 0 && !bytes.Equal(op.Signed, next.Signed) {
		return devicesec.ErrInvalid
	}
	if len(op.Delivery) > 0 && !bytes.Equal(op.Delivery, next.Delivery) {
		return devicesec.ErrInvalid
	}
	return nil
}

// PersistedRegistrationIntent stores approved registration state across crashes before completion.
type PersistedRegistrationIntent struct {
	Server           string
	DeviceID         string
	RegisterRequest  []byte
	Replace          bool
	AccountPublicKey []byte
	PairingID        string
	CandidateToken   string
	EdSlot           string
	XSlot            string
	ReusesDeviceEd   bool
	Record           *PersistedRecord
}

// Encode serializes intent in canonical v4 format.
func (in *PersistedRegistrationIntent) Encode() ([]byte, error) {
	if in == nil || in.Server == "" || !devicesec.IsDeviceID(in.DeviceID) || len(in.RegisterRequest) == 0 || in.Record == nil {
		return nil, devicesec.ErrInvalid
	}
	recBytes, err := in.Record.Encode()
	if err != nil {
		return nil, err
	}
	var e devicesec.Encoder
	e.Raw([]byte(storeMagicIntent))
	e.U32(storeCodecVersion)
	e.Str(in.Server)
	e.Str(in.DeviceID)
	e.LP(in.RegisterRequest)
	encodeBool(&e, in.Replace)
	e.LP(in.AccountPublicKey)
	e.Str(in.PairingID)
	e.Str(in.CandidateToken)
	e.Str(in.EdSlot)
	e.Str(in.XSlot)
	encodeBool(&e, in.ReusesDeviceEd)
	e.LP(recBytes)
	return e.B, nil
}

// DecodePersistedRegistrationIntent decodes and validates a v4 registration intent.
func DecodePersistedRegistrationIntent(raw []byte) (*PersistedRegistrationIntent, error) {
	if len(raw) == 0 || len(raw) > maxStoreBlobBytes {
		return nil, errStoreCorrupt
	}
	br := binReader{b: raw}
	if !br.checkMagic(storeMagicIntent, storeCodecVersion) {
		return nil, errStoreCorrupt
	}
	var in PersistedRegistrationIntent
	in.Server = br.str()
	in.DeviceID = br.str()
	in.RegisterRequest = br.lp(maxStoreBlobBytes)
	in.Replace = br.bool()
	in.AccountPublicKey = br.lp(64)
	in.PairingID = br.str()
	in.CandidateToken = br.str()
	in.EdSlot = br.str()
	in.XSlot = br.str()
	in.ReusesDeviceEd = br.bool()
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
	Server         string
	PairingID      string
	EdSlot         string
	XSlot          string
	EdPublic       []byte
	XPublic        []byte
	ReusesDeviceEd bool
}

// Encode serializes c in canonical v4 format.
func (c *PersistedPairingCandidate) Encode() ([]byte, error) {
	if c == nil || c.Server == "" || c.PairingID == "" || len(c.EdPublic) != 32 || len(c.XPublic) != 32 {
		return nil, devicesec.ErrInvalid
	}
	var e devicesec.Encoder
	e.Raw([]byte(storeMagicCand))
	e.U32(storeCodecVersion)
	e.Str(c.Server)
	e.Str(c.PairingID)
	e.Str(c.EdSlot)
	e.Str(c.XSlot)
	e.LP(c.EdPublic)
	e.LP(c.XPublic)
	encodeBool(&e, c.ReusesDeviceEd)
	return e.B, nil
}

// DecodePersistedPairingCandidate decodes a v4 pairing candidate.
func DecodePersistedPairingCandidate(raw []byte) (*PersistedPairingCandidate, error) {
	if len(raw) == 0 || len(raw) > 4096 {
		return nil, errStoreCorrupt
	}
	br := binReader{b: raw}
	if !br.checkMagic(storeMagicCand, storeCodecVersion) {
		return nil, errStoreCorrupt
	}
	var c PersistedPairingCandidate
	c.Server = br.str()
	c.PairingID = br.str()
	c.EdSlot = br.str()
	c.XSlot = br.str()
	c.EdPublic = br.lp(64)
	c.XPublic = br.lp(64)
	c.ReusesDeviceEd = br.bool()
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
	if p == nil || p.Server == "" || !devicesec.IsDeviceID(p.Pin.Account) ||
		p.Pin.Domain == (devicesec.Hash{}) || p.Pin.Genesis == (devicesec.Hash{}) ||
		devicesec.ID(p.AccountPublicKey[:]) != p.Pin.Account {
		return nil, devicesec.ErrUntrusted
	}
	var e devicesec.Encoder
	e.Raw([]byte(storeMagicProfile))
	e.U32(storeCodecVersion)
	e.Str(p.Server)
	e.Str(p.Pin.Account)
	e.Raw(p.Pin.Domain[:])
	e.Raw(p.Pin.Genesis[:])
	e.Raw(p.AccountPublicKey[:])
	return e.B, nil
}

// DecodePersistedPublicProfile decodes and checks p against its pin.
func DecodePersistedPublicProfile(raw []byte) (*PersistedPublicProfile, error) {
	if len(raw) == 0 || len(raw) > 2048 {
		return nil, errStoreCorrupt
	}
	br := binReader{b: raw}
	if !br.checkMagic(storeMagicProfile, storeCodecVersion) {
		return nil, errStoreCorrupt
	}
	var p PersistedPublicProfile
	p.Server = br.str()
	p.Pin.Account = br.str()
	copy(p.Pin.Domain[:], br.raw(32))
	copy(p.Pin.Genesis[:], br.raw(32))
	copy(p.AccountPublicKey[:], br.raw(32))
	if err := br.done(); err != nil {
		return nil, err
	}
	if p.Server == "" || !devicesec.IsDeviceID(p.Pin.Account) ||
		devicesec.ID(p.AccountPublicKey[:]) != p.Pin.Account {
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
func (n *PersistedNotifyRecord) Encode() ([]byte, error) {
	if n == nil || !devicesec.IsDeviceID(n.Pin.Account) ||
		n.Pin.Domain == (devicesec.Hash{}) || n.Pin.Genesis == (devicesec.Hash{}) {
		return nil, devicesec.ErrInvalid
	}
	s, err := devicesec.ParseState(n.HeadState)
	if err != nil || n.Pin.Verify(s) != nil {
		return nil, devicesec.ErrUntrusted
	}
	var e devicesec.Encoder
	e.Raw([]byte(storeMagicNotify))
	e.U32(storeCodecVersion)
	e.Str(n.Pin.Account)
	e.Raw(n.Pin.Domain[:])
	e.Raw(n.Pin.Genesis[:])
	e.LP(n.HeadState)
	e.Raw(n.PurposeKey[:])
	return e.B, nil
}

// DecodePersistedNotifyRecord decodes and verifies n.
func DecodePersistedNotifyRecord(raw []byte) (*PersistedNotifyRecord, error) {
	if len(raw) == 0 || len(raw) > devicesec.MaxStateBytes+512 {
		return nil, errStoreCorrupt
	}
	br := binReader{b: raw}
	if !br.checkMagic(storeMagicNotify, storeCodecVersion) {
		return nil, errStoreCorrupt
	}
	var n PersistedNotifyRecord
	n.Pin.Account = br.str()
	copy(n.Pin.Domain[:], br.raw(32))
	copy(n.Pin.Genesis[:], br.raw(32))
	n.HeadState = br.lp(devicesec.MaxStateBytes)
	copy(n.PurposeKey[:], br.raw(32))
	if err := br.done(); err != nil {
		return nil, err
	}
	s, err := devicesec.ParseState(n.HeadState)
	if err != nil || n.Pin.Verify(s) != nil {
		return nil, devicesec.ErrUntrusted
	}
	return &n, nil
}
