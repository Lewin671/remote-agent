package deviceauth

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"io"
)

const MaxRPCBytes = 1 << 20

type Method uint32

const (
	Authorize        Method = 1
	Catalog          Method = 2
	States           Method = 3
	HistoryKeys      Method = 4
	BeginPair        Method = 5
	ApprovePair      Method = 6
	CancelPair       Method = 7
	Delete           Method = 8
	GetCompletion    Method = 9
	GetFences        Method = 10
	AckFence         Method = 11
	CompletionStates Method = 12
)

type Capsule struct {
	From   string
	To     string
	Method Method
	Enc    []byte
	Cipher []byte
}

func (c Capsule) MarshalBinary() ([]byte, error) {
	if !deviceID(c.From) || !deviceID(c.To) || c.Method < Authorize || c.Method > CompletionStates || len(c.Enc) != 32 || len(c.Cipher) < 16 || len(c.Cipher) > MaxRPCBytes {
		return nil, ErrInvalid
	}
	e := encoder{}
	e.str(c.From)
	e.str(c.To)
	e.u32(uint32(c.Method))
	e.lp(c.Enc)
	e.lp(c.Cipher)
	return e.b, nil
}
func ParseCapsule(b []byte) (c Capsule, err error) {
	if len(b) > MaxRPCBytes+512 {
		return c, ErrInvalid
	}
	d := decoder{b: b}
	c.From = d.str()
	c.To = d.str()
	c.Method = Method(d.u32())
	c.Enc = d.lp(32)
	c.Cipher = d.lp(MaxRPCBytes)
	if d.done() != nil {
		return c, ErrInvalid
	}
	_, err = c.MarshalBinary()
	return c, err
}
func rpcContext(p Pin, from, to string, method Method, response bool) []byte {
	e := encoder{b: []byte("ra2/rpc\x00")}
	e.str(p.Account)
	e.raw(p.Domain[:])
	e.str(p.AuthorityID)
	e.str(from)
	e.str(to)
	e.u32(uint32(method))
	if response {
		e.u32(1)
	} else {
		e.u32(0)
	}
	return e.b
}
func sealCapsule(p Pin, from, to string, method Method, response bool, pub, plain []byte) (Capsule, error) {
	if len(plain) > MaxRPCBytes-16 {
		return Capsule{}, ErrCapacity
	}
	ctx := rpcContext(p, from, to, method, response)
	enc, ct, err := sealHPKE(pub, append([]byte("ra2/rpc-info\x00"), ctx...), append([]byte("ra2/rpc-aad\x00"), ctx...), plain)
	return Capsule{From: from, To: to, Method: method, Enc: enc, Cipher: ct}, err
}
func openCapsule(p Pin, c Capsule, response bool, private []byte) ([]byte, error) {
	if _, err := c.MarshalBinary(); err != nil {
		return nil, err
	}
	ctx := rpcContext(p, c.From, c.To, c.Method, response)
	return openHPKE(private, c.Enc, append([]byte("ra2/rpc-info\x00"), ctx...), append([]byte("ra2/rpc-aad\x00"), ctx...), c.Cipher)
}

type rpcRequest struct {
	Nonce     Hash
	Body      []byte
	Signature []byte
}

func (r rpcRequest) signBytes(p Pin, c Capsule) ([]byte, error) {
	if r.Nonce == (Hash{}) || len(r.Body) > MaxRPCBytes-1024 {
		return nil, ErrInvalid
	}
	e := encoder{b: []byte("ra2/auth-request\x00")}
	e.raw(rpcContext(p, c.From, c.To, c.Method, false))
	e.raw(r.Nonce[:])
	e.lp(r.Body)
	return e.b, nil
}

type rpcResponse struct {
	Nonce       Hash
	RequestHash Hash
	Body        []byte
	Error       string
	Signature   []byte
}

func (r rpcResponse) signBytes(p Pin, c Capsule) ([]byte, error) {
	if r.Nonce == (Hash{}) || r.RequestHash == (Hash{}) || len(r.Body) > MaxRPCBytes-1024 || len(r.Error) > 32 {
		return nil, ErrInvalid
	}
	e := encoder{b: []byte("ra2/auth-response\x00")}
	e.raw(rpcContext(p, c.From, c.To, c.Method, true))
	e.raw(r.Nonce[:])
	e.raw(r.RequestHash[:])
	e.str(r.Error)
	e.lp(r.Body)
	return e.b, nil
}
func decodeJSON(b []byte, out any) error {
	if len(b) > MaxRPCBytes {
		return ErrInvalid
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return ErrInvalid
	}
	return nil
}

// Waiter is caller-local evidence. Reply verifies its unpredictable nonce AND
// hash of the exact signed request; another member's valid response won't do.
type Waiter struct {
	Pin         Pin
	Sender      string
	Method      Method
	Nonce       Hash
	RequestHash Hash
}

func Request(p Pin, ed ed25519.PrivateKey, method Method, body any) (Capsule, Waiter, error) {
	if len(ed) != 64 {
		return Capsule{}, Waiter{}, ErrInvalid
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return Capsule{}, Waiter{}, ErrInvalid
	}
	nonce, err := randomHash()
	if err != nil {
		return Capsule{}, Waiter{}, err
	}
	c := Capsule{From: ID(ed.Public().(ed25519.PublicKey)), To: p.AuthorityID, Method: method}
	r := rpcRequest{Nonce: nonce, Body: raw}
	b, err := r.signBytes(p, c)
	if err != nil {
		return Capsule{}, Waiter{}, err
	}
	r.Signature = ed25519.Sign(ed, b)
	rh := digest("ra2/request-digest\x00", b, r.Signature)
	raw, err = json.Marshal(r)
	if err != nil {
		return Capsule{}, Waiter{}, ErrInvalid
	}
	c, err = sealCapsule(p, c.From, c.To, method, false, p.AuthorityX[:], raw)
	return c, Waiter{p, c.From, method, nonce, rh}, err
}
func (w Waiter) Reply(c Capsule, xPrivate []byte, out any) error {
	if c.From != w.Pin.AuthorityID || c.To != w.Sender || c.Method != w.Method {
		return ErrUntrusted
	}
	raw, err := openCapsule(w.Pin, c, true, xPrivate)
	if err != nil {
		return err
	}
	var r rpcResponse
	if decodeJSON(raw, &r) != nil {
		return ErrInvalid
	}
	b, err := r.signBytes(w.Pin, c)
	if err != nil || r.Nonce != w.Nonce || r.RequestHash != w.RequestHash || !ed25519.Verify(w.Pin.AuthorityEd[:], b, r.Signature) {
		return ErrUntrusted
	}
	switch r.Error {
	case "":
	case "stale":
		return ErrStale
	case "conflict":
		return ErrConflict
	case "pending":
		return ErrPending
	case "capacity":
		return ErrCapacity
	case "storage":
		return ErrStorage
	default:
		return ErrUntrusted
	}
	return decodeJSON(r.Body, out)
}
func errorCode(err error) string {
	switch err {
	case nil:
		return ""
	case ErrStale:
		return "stale"
	case ErrConflict:
		return "conflict"
	case ErrPending:
		return "pending"
	case ErrCapacity:
		return "capacity"
	case ErrStorage:
		return "storage"
	default:
		return "denied"
	}
}

// Handle authenticates the HPKE payload's member signature independently of
// transport identity. Unknown/root-invented senders never reach dispatch.
func (a *Authority) Handle(c Capsule) (Capsule, error) {
	a.mu.Lock()
	if err := a.disk.check(); err != nil {
		a.mu.Unlock()
		return Capsule{}, err
	}
	p := a.data.Pin
	x := bytes.Clone(a.data.XPrivate)
	a.mu.Unlock()
	if c.To != p.AuthorityID {
		return Capsule{}, ErrUntrusted
	}
	raw, err := openCapsule(p, c, false, x)
	if err != nil {
		return Capsule{}, err
	}
	var req rpcRequest
	if decodeJSON(raw, &req) != nil {
		return Capsule{}, ErrInvalid
	}
	b, err := req.signBytes(p, c)
	if err != nil {
		return Capsule{}, err
	}
	rh := digest("ra2/request-digest\x00", b, req.Signature)
	a.mu.Lock()
	member, ok := a.data.State.Core.Member(c.From)
	edseed := bytes.Clone(a.data.EdSeed)
	// An already-removed requester may only retrieve the PUBLIC signed receipt
	// for an operation it signed while current. No catalog, grants or keyring.
	if !ok && (c.Method == GetCompletion || c.Method == CompletionStates) {
		var q CompletionQuery
		if decodeJSON(req.Body, &q) == nil {
			for _, co := range a.data.Completed {
				for _, op := range co.Group.Operations {
					if op.Request.Operation == q.Operation && op.Request.Requester == c.From {
						member, ok = co.Group.Base.Core.Member(c.From)
					}
				}
			}
			if a.data.Pending != nil {
				g := a.data.Pending
				for _, op := range g.Operations {
					if op.Request.Operation == q.Operation && op.Request.Requester == c.From {
						member, ok = g.Base.Core.Member(c.From)
					}
				}
			}
		}
	}
	a.mu.Unlock()
	if !ok || !ed25519.Verify(member.EdPublic[:], b, req.Signature) {
		return Capsule{}, ErrUntrusted
	}
	body, callErr := a.dispatch(c.From, c.Method, req.Nonce, req.Body)
	response := rpcResponse{Nonce: req.Nonce, RequestHash: rh, Error: errorCode(callErr)}
	if callErr == nil {
		response.Body, err = json.Marshal(body)
		if err != nil {
			return Capsule{}, ErrInvalid
		}
	}
	out := Capsule{From: p.AuthorityID, To: c.From, Method: c.Method}
	sign, err := response.signBytes(p, out)
	if err != nil {
		return Capsule{}, err
	}
	response.Signature = ed25519.Sign(ed25519.NewKeyFromSeed(edseed), sign)
	raw, err = json.Marshal(response)
	if err != nil {
		return Capsule{}, ErrInvalid
	}
	return sealCapsule(p, out.From, out.To, out.Method, true, member.XPublic[:], raw)
}

type AuthorizeRequest struct {
	Header []byte
	Commit Hash
	Origin []byte
	Cipher *Ciphertext
}
type AuthorizeReply struct {
	Authorization []byte
	Signature     []byte
}
type StateQuery struct{ After uint64 }
type StateReply struct {
	Current Hash
	States  []State
	Group   *RevocationGroup `json:",omitempty"`
}
type KeyQuery struct{ After uint64 }
type EpochKey struct {
	Epoch  uint64
	Secret []byte
}
type KeyReply struct{ Keys []EpochKey }
type PairBegin struct {
	ID        string
	Candidate Member
}
type PairApprove struct {
	ID                 string
	ApproverSignature  []byte
	CandidateSignature []byte
}
type PairCancel struct {
	ID        string
	Signature []byte
}
type CompletionQuery struct {
	Operation string
	After     uint64
}

func (a *Authority) dispatch(sender string, method Method, nonce Hash, body []byte) (any, error) {
	switch method {
	case Authorize:
		var q AuthorizeRequest
		if decodeJSON(body, &q) != nil {
			return nil, ErrInvalid
		}
		return a.authorize(sender, nonce, q)
	case Catalog:
		var q CatalogQuery
		if decodeJSON(body, &q) != nil {
			return nil, ErrInvalid
		}
		return a.catalog(sender, q)
	case States:
		var q StateQuery
		if decodeJSON(body, &q) != nil {
			return nil, ErrInvalid
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if _, ok := a.data.State.Core.Member(sender); !ok {
			return nil, ErrUntrusted
		}
		sh, _ := a.data.State.Hash()
		page, err := a.disk.page(q.After)
		return StateReply{Current: sh, States: page}, err
	case HistoryKeys:
		var q KeyQuery
		if decodeJSON(body, &q) != nil {
			return nil, ErrInvalid
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		if _, ok := a.data.State.Core.Member(sender); !ok {
			return nil, ErrUntrusted
		}
		if err := a.disk.check(); err != nil {
			return nil, err
		}
		out := KeyReply{}
		if q.After > a.data.State.Core.ContentEpoch {
			return nil, ErrInvalid
		}
		for ep := q.After + 1; ep <= a.data.State.Core.ContentEpoch && len(out.Keys) < 64; ep++ {
			if q.After >= a.data.State.Core.ContentEpoch {
				break
			}
			key, ok := a.data.Secrets[ep]
			if !ok {
				return nil, ErrStorage
			}
			out.Keys = append(out.Keys, EpochKey{ep, bytes.Clone(key)})
		}
		return out, nil
	case BeginPair:
		var q PairBegin
		if decodeJSON(body, &q) != nil {
			return nil, ErrInvalid
		}
		return a.BeginPair(q.ID, sender, q.Candidate)
	case ApprovePair:
		var q PairApprove
		if decodeJSON(body, &q) != nil {
			return nil, ErrInvalid
		}
		a.mu.Lock()
		p, ok := a.data.Contexts[q.ID]
		a.mu.Unlock()
		if !ok || p.Approver != sender {
			return nil, ErrUntrusted
		}
		return a.ApprovePair(q.ID, q.ApproverSignature, q.CandidateSignature)
	case CancelPair:
		var q PairCancel
		if decodeJSON(body, &q) != nil {
			return nil, ErrInvalid
		}
		a.mu.Lock()
		p, ok := a.data.Contexts[q.ID]
		a.mu.Unlock()
		if !ok || p.Approver != sender {
			return nil, ErrUntrusted
		}
		return struct{}{}, a.CancelPair(q.ID, q.Signature)
	case Delete:
		var q DeleteRequest
		if decodeJSON(body, &q) != nil || q.Requester != sender {
			return nil, ErrInvalid
		}
		return a.Delete(q)
	case GetCompletion:
		var q CompletionQuery
		if decodeJSON(body, &q) != nil {
			return nil, ErrInvalid
		}
		return a.Completion(q.Operation)
	case CompletionStates:
		var q CompletionQuery
		if decodeJSON(body, &q) != nil {
			return nil, ErrInvalid
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		var group *RevocationGroup
		for _, c := range a.data.Completed {
			for _, op := range c.Group.Operations {
				if op.Request.Operation == q.Operation && op.Request.Requester == sender {
					g := c.Group
					group = &g
				}
			}
		}
		if a.data.Pending != nil {
			for _, op := range a.data.Pending.Operations {
				if op.Request.Operation == q.Operation && op.Request.Requester == sender {
					group = a.data.Pending
				}
			}
		}
		if group == nil {
			return nil, ErrUntrusted
		}
		if q.After == 0 {
			sh, _ := group.Head.Hash()
			return StateReply{Current: sh, Group: clone(group)}, nil
		}
		if q.After < group.Base.Core.Epoch || q.After > group.Head.Core.Epoch {
			return nil, ErrUntrusted
		}
		states, err := a.disk.page(q.After)
		if err != nil {
			return nil, err
		}
		filtered := []State{}
		for _, state := range states {
			if state.Core.Epoch <= group.Head.Core.Epoch {
				filtered = append(filtered, state)
			}
		}
		sh, _ := group.Head.Hash()
		return StateReply{Current: sh, States: filtered}, nil

	case GetFences:
		a.mu.Lock()
		_, ok := a.data.State.Core.Member(sender)
		a.mu.Unlock()
		if !ok {
			return nil, ErrUntrusted
		}
		requests, err := a.FenceRequests()
		filtered := []FenceRequest{}
		for _, r := range requests {
			if r.Executor == sender {
				filtered = append(filtered, r)
			}
		}
		return filtered, err
	case AckFence:
		var q FenceReceipt
		if decodeJSON(body, &q) != nil || q.Executor != sender {
			return nil, ErrInvalid
		}
		return a.Ack(q)
	}
	return nil, ErrInvalid
}
