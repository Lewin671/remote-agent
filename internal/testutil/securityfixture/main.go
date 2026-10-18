// securityfixture is a synthetic stdin/stdout peer used only by cross-language
// tests. It is not linked into any product binary or release artifact.
//
// The v3 fixture exposes a phone-signed state chain. Client implementations
// use it to check their own parser, chain verification, content-key walk and
// envelope origin signatures against the Go implementation.
package main

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"google.golang.org/protobuf/proto"
)

type request struct {
	Op          string
	Account     string
	Member      string
	Seed        []byte
	Recovery    []byte
	Kind        uint32
	Header      []byte
	Plain       []byte
	SessionSeed []byte
	Envelope    []byte
	State       []byte
}
type record struct {
	Version int      `json:"version"`
	Account string   `json:"account"`
	Domain  []byte   `json:"domain"`
	Genesis []byte   `json:"genesis"`
	Member  string   `json:"member"`
	X       []byte   `json:"x"`
	States  [][]byte `json:"states"`
	Proofs  [][]byte `json:"proofs"`
}
type response struct {
	Record   json.RawMessage `json:",omitempty"`
	Genesis  []byte          `json:",omitempty"`
	State    []byte          `json:",omitempty"`
	Envelope []byte          `json:",omitempty"`
	Header   []byte          `json:",omitempty"`
	Plain    []byte          `json:",omitempty"`
	Error    string          `json:",omitempty"`
}
type fixture struct {
	mu       sync.Mutex
	account  string
	domain   deviceauth.Hash
	phoneEd  ed25519.PrivateKey
	recovery ed25519.PrivateKey
	pin      deviceauth.Pin
	secret   []byte
	states   []deviceauth.State
	proofs   []*deviceauth.RecoveryStatement
	members  map[string]*member
}
type member struct {
	ed       ed25519.PrivateKey
	x        []byte
	endpoint *deviceauth.Endpoint
}

func (f *fixture) recordFor(id string) (*record, error) {
	m := f.members[id]
	if m == nil {
		return nil, errors.New("unknown synthetic member")
	}
	r := &record{Version: 3, Account: f.pin.Account, Domain: f.pin.Domain[:], Genesis: f.pin.Genesis[:], Member: id, X: m.x}
	for _, s := range f.states {
		raw, err := s.MarshalBinary()
		if err != nil {
			return nil, err
		}
		r.States = append(r.States, raw)
	}
	for _, p := range f.proofs {
		if p == nil {
			r.Proofs = append(r.Proofs, nil)
			continue
		}
		raw, err := p.MarshalBinary()
		if err != nil {
			return nil, err
		}
		r.Proofs = append(r.Proofs, raw)
	}
	return r, nil
}

func (f *fixture) add(ed ed25519.PrivateKey, x []byte, kind deviceauth.Kind) (deviceauth.Member, error) {
	xpub, err := deviceauth.XPublic(x)
	if err != nil {
		return deviceauth.Member{}, err
	}
	m, err := deviceauth.NewMember(ed.Public().(ed25519.PublicKey), xpub, kind)
	if err != nil {
		return deviceauth.Member{}, err
	}
	newSecret := make([]byte, 32)
	if _, err := rand.Read(newSecret); err != nil {
		return deviceauth.Member{}, err
	}
	parent := f.states[len(f.states)-1]
	s, err := deviceauth.BuildPairState(parent, m, xpub, f.secret, newSecret, f.phoneEd, 1_700_000_000_000)
	if err != nil {
		return deviceauth.Member{}, err
	}
	f.states = append(f.states, s)
	f.proofs = append(f.proofs, nil)
	f.secret = newSecret
	// Existing members must see the new head before they can open a frame
	// sealed against it.
	for _, other := range f.members {
		if err := other.endpoint.Install([]deviceauth.State{s}, []*deviceauth.RecoveryStatement{nil}); err != nil {
			return deviceauth.Member{}, err
		}
	}
	endpoint, err := deviceauth.InitEndpoint("", f.pin, f.states, f.proofs, m.ID, ed.Seed(), x)
	if err != nil {
		return deviceauth.Member{}, err
	}
	f.members[m.ID] = &member{ed: ed, x: x, endpoint: endpoint}
	return m, nil
}

func (f *fixture) run(q request) (response, error) {
	switch q.Op {
	case "init":
		if f.phoneEd != nil || len(q.Seed) != 32 {
			return response{}, errors.New("invalid synthetic genesis")
		}
		f.account = q.Account
		f.phoneEd = ed25519.NewKeyFromSeed(q.Seed)
		x, xpub, err := deviceauth.NewXKey()
		if err != nil {
			return response{}, err
		}
		m, err := deviceauth.NewMember(f.phoneEd.Public().(ed25519.PublicKey), xpub, deviceauth.Client)
		if err != nil {
			return response{}, err
		}
		if _, err := rand.Read(f.domain[:]); err != nil {
			return response{}, err
		}
		var recPub [32]byte
		if len(q.Recovery) == 32 {
			f.recovery = ed25519.NewKeyFromSeed(q.Recovery)
		} else {
			_, f.recovery, _ = ed25519.GenerateKey(rand.Reader)
		}
		copy(recPub[:], f.recovery.Public().(ed25519.PublicKey))
		f.secret = make([]byte, 32)
		if _, err := rand.Read(f.secret); err != nil {
			return response{}, err
		}
		g, err := deviceauth.BuildGenesis(f.account, f.domain, m, recPub, f.secret, f.phoneEd, 1_700_000_000_000)
		if err != nil {
			return response{}, err
		}
		f.pin, err = g.Pin()
		if err != nil {
			return response{}, err
		}
		f.states = []deviceauth.State{g}
		f.proofs = []*deviceauth.RecoveryStatement{nil}
		endpoint, err := deviceauth.InitEndpoint("", f.pin, f.states, f.proofs, m.ID, f.phoneEd.Seed(), x)
		if err != nil {
			return response{}, err
		}
		f.members = map[string]*member{m.ID: {ed: f.phoneEd, x: x, endpoint: endpoint}}
		r, err := f.recordFor(m.ID)
		raw, _ := json.Marshal(r)
		genesis, _ := g.MarshalBinary()
		return response{Record: raw, Genesis: genesis}, err
	case "enroll":
		if f.phoneEd == nil || len(q.Seed) != 32 {
			return response{}, errors.New("missing synthetic authority")
		}
		ed := ed25519.NewKeyFromSeed(q.Seed)
		x, _, err := deviceauth.NewXKey()
		if err != nil {
			return response{}, err
		}
		kind := deviceauth.Kind(q.Kind)
		if kind == 0 {
			kind = deviceauth.Machine
		}
		m, err := f.add(ed, x, kind)
		if err != nil {
			return response{}, err
		}
		r, err := f.recordFor(m.ID)
		raw, _ := json.Marshal(r)
		state := f.states[len(f.states)-1]
		sraw, _ := state.MarshalBinary()
		return response{Record: raw, State: sraw}, err
	case "record":
		r, err := f.recordFor(q.Member)
		if err != nil {
			return response{}, err
		}
		raw, _ := json.Marshal(r)
		return response{Record: raw}, nil
	case "state":
		sraw, err := f.states[len(f.states)-1].MarshalBinary()
		return response{State: sraw}, err
	case "seal":
		m := f.members[q.Member]
		if m == nil || len(q.Header) == 0 {
			return response{}, errors.New("missing synthetic sender/header")
		}
		h := &pb.Header{}
		if err := proto.Unmarshal(q.Header, h); err != nil {
			return response{}, err
		}
		header := deviceauth.Header{Kind: uint32(h.Kind), Purpose: deviceauth.Purpose(h.Kind), Machine: h.MachineId, Session: h.SessionId, Request: h.RequestId, Seq: h.Seq, Deadline: uint64(h.DeadlineMs), Compression: uint32(h.Compression)}
		wire, err := m.endpoint.Prepare(header, q.SessionSeed, q.Plain)
		if err != nil {
			return response{}, err
		}
		env, err := wire.Envelope()
		if err != nil {
			return response{}, err
		}
		raw, err := proto.Marshal(env)
		return response{Envelope: raw}, err
	case "open":
		m := f.members[q.Member]
		if m == nil {
			return response{}, errors.New("missing synthetic recipient")
		}
		env := &pb.Envelope{}
		if proto.Unmarshal(q.Envelope, env) != nil {
			return response{}, errors.New("bad fixture envelope")
		}
		wire, err := deviceauth.FromEnvelope(env)
		if err != nil {
			return response{}, err
		}
		header, err := deviceauth.ParseHeader(wire.Header)
		if err != nil {
			return response{}, err
		}
		live := header.Purpose.Control()
		header, plain, err := m.endpoint.Open(wire, q.SessionSeed, live, 1_700_000_500_000)
		if err != nil {
			return response{}, err
		}
		projection, err := proto.Marshal(header.Projection())
		return response{Header: projection, Plain: plain}, err
	}
	return response{}, errors.New("unknown fixture operation")
}

func main() {
	f := &fixture{members: map[string]*member{}}
	scan := bufio.NewScanner(os.Stdin)
	scan.Buffer(make([]byte, 4096), 16<<20)
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	for scan.Scan() {
		var q request
		var result response
		if err := json.Unmarshal(scan.Bytes(), &q); err != nil {
			result.Error = "malformed fixture request"
		} else {
			f.mu.Lock()
			r, e := f.run(q)
			f.mu.Unlock()
			result = r
			if e != nil {
				result.Error = e.Error()
			}
		}
		b, err := json.Marshal(result)
		if err != nil {
			b = []byte(`{"Error":"fixture response encoding failed"}`)
		}
		if _, err = fmt.Fprintln(out, string(b)); err != nil {
			return
		}
		if err = out.Flush(); err != nil {
			return
		}
	}
}
