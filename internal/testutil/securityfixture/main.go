// securityfixture is a synthetic stdin/stdout peer used only by cross-language
// tests. It is not linked into any product binary or release artifact.
package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"google.golang.org/protobuf/proto"
)

type request struct {
	Op          string
	Account     string
	Member      string
	Approver    string
	Pair        string
	Seed        []byte
	Kind        uint32
	Header      []byte
	Plain       []byte
	SessionSeed []byte
	Envelope    []byte
	Capsule     []byte
}
type response struct {
	Record      []byte                  `json:",omitempty"`
	Genesis     []byte                  `json:",omitempty"`
	State       []byte                  `json:",omitempty"`
	PairContext *deviceauth.PairContext `json:",omitempty"`
	Envelope    []byte                  `json:",omitempty"`
	Header      []byte                  `json:",omitempty"`
	Plain       []byte                  `json:",omitempty"`
	Capsule     []byte                  `json:",omitempty"`
	Error       string                  `json:",omitempty"`
}
type member struct {
	ed     ed25519.PrivateKey
	x      []byte
	remote *deviceauth.Remote
}
type fixture struct {
	mu        sync.Mutex
	dir       string
	authority *deviceauth.Authority
	members   map[string]*member
}

func field(out []byte, b []byte) []byte {
	out = binary.BigEndian.AppendUint32(out, uint32(len(b)))
	return append(out, b...)
}
func (f *fixture) record(id string) ([]byte, error) {
	m := f.members[id]
	if m == nil {
		return nil, errors.New("unknown synthetic member")
	}
	if err := m.remote.Sync(context.Background()); err != nil {
		return nil, err
	}
	pin, state, _ := m.remote.Endpoint.Snapshot()
	raw, err := state.MarshalBinary()
	if err != nil {
		return nil, err
	}
	out := binary.BigEndian.AppendUint32(nil, 2)
	out = field(out, []byte(pin.Account))
	out = append(out, pin.Domain[:]...)
	out = append(out, pin.Genesis[:]...)
	out = field(out, []byte(pin.AuthorityID))
	out = append(out, pin.AuthorityEd[:]...)
	out = append(out, pin.AuthorityX[:]...)
	out = field(out, raw)
	out = field(out, []byte(id))
	out = append(out, m.x...)
	out = binary.BigEndian.AppendUint64(out, 0)
	return out, nil
}
func (f *fixture) install(ed ed25519.PrivateKey, x []byte, state deviceauth.State) (string, error) {
	id := deviceauth.ID(ed.Public().(ed25519.PublicKey))
	pin, _ := f.authority.Snapshot()
	endpoint, err := deviceauth.InitEndpoint(filepath.Join(f.dir, id+".db"), pin, state, id, ed.Seed(), x)
	if err != nil {
		return "", err
	}
	f.members[id] = &member{ed: ed, x: x, remote: &deviceauth.Remote{Endpoint: endpoint, Exchange: func(_ context.Context, c deviceauth.Capsule) (deviceauth.Capsule, error) {
		return f.authority.Handle(c)
	}}}
	return id, nil
}
func (f *fixture) run(q request) (response, error) {
	ctx := context.Background()
	switch q.Op {
	case "init":
		if f.authority != nil || len(q.Seed) != 32 {
			return response{}, errors.New("invalid synthetic genesis")
		}
		ed := ed25519.NewKeyFromSeed(q.Seed)
		x, xp, err := deviceauth.NewXKey()
		if err != nil {
			return response{}, err
		}
		m, err := deviceauth.NewMember(ed.Public().(ed25519.PublicKey), xp, deviceauth.Machine)
		if err != nil {
			return response{}, err
		}
		f.authority, err = deviceauth.InitAuthority(filepath.Join(f.dir, "authority.db"), q.Account, m)
		if err != nil {
			return response{}, err
		}
		_, state := f.authority.Snapshot()
		id, err := f.install(ed, x, state)
		if err != nil {
			return response{}, err
		}
		record, err := f.record(id)
		genesis, _ := state.MarshalBinary()
		return response{Record: record, Genesis: genesis}, err
	case "enroll":
		if f.authority == nil || len(q.Seed) != 32 {
			return response{}, errors.New("missing synthetic authority")
		}
		authorizer := f.members[q.Approver]
		if authorizer == nil {
			return response{}, errors.New("missing synthetic approver")
		}
		ed := ed25519.NewKeyFromSeed(q.Seed)
		x, xp, err := deviceauth.NewXKey()
		if err != nil {
			return response{}, err
		}
		m, err := deviceauth.NewMember(ed.Public().(ed25519.PublicKey), xp, deviceauth.Kind(q.Kind))
		if err != nil {
			return response{}, err
		}
		pair, err := f.authority.BeginPair(q.Pair, q.Approver, m)
		if err != nil {
			return response{}, err
		}
		approve, err := deviceauth.SignPair(pair, authorizer.ed, false)
		if err != nil {
			return response{}, err
		}
		accept, err := deviceauth.SignPair(pair, ed, true)
		if err != nil {
			return response{}, err
		}
		state, err := f.authority.ApprovePair(pair.ID, approve, accept)
		if err != nil {
			return response{}, err
		}
		id, err := f.install(ed, x, state)
		if err != nil {
			return response{}, err
		}
		record, err := f.record(id)
		raw, _ := state.MarshalBinary()
		return response{Record: record, State: raw, PairContext: &pair}, err
	case "record":
		b, err := f.record(q.Member)
		return response{Record: b}, err
	case "rpc":
		capsule, err := deviceauth.ParseCapsule(q.Capsule)
		if err != nil {
			return response{}, err
		}
		reply, err := f.authority.Handle(capsule)
		if err != nil {
			return response{}, err
		}
		raw, err := reply.MarshalBinary()
		return response{Capsule: raw}, err
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
		var wire deviceauth.Wire
		var err error
		if header.Purpose.Mutable() {
			wire, err = m.remote.SealMutable(ctx, header, q.Plain)
		} else {
			wire, err = m.remote.Seal(ctx, header, q.SessionSeed, q.Plain)
		}
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
		var plain []byte
		if header.Purpose.Control() {
			header, plain, err = m.remote.OpenCurrent(ctx, wire, q.SessionSeed)
		} else {
			header, plain, err = m.remote.OpenHistory(ctx, wire, q.SessionSeed)
		}
		if err != nil {
			return response{}, err
		}
		projection, err := proto.Marshal(header.Projection())
		return response{Header: projection, Plain: plain}, err
	}
	return response{}, errors.New("unknown fixture operation")
}
func main() {
	dir, err := os.MkdirTemp("", "ra-security-fixture-")
	if err != nil {
		panic("cannot initialize synthetic fixture")
	}
	defer os.RemoveAll(dir)
	f := &fixture{dir: dir, members: map[string]*member{}}
	stop := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				f.mu.Lock()
				if f.authority != nil {
					_, state := f.authority.Snapshot()
					for id, m := range f.members {
						member, ok := state.Core.Member(id)
						if ok && member.Kind == deviceauth.Machine {
							_ = m.remote.Sync(context.Background())
						}
					}
				}
				f.mu.Unlock()
			}
		}
	}()
	defer func() {
		close(stop)
		<-stopped
		for _, m := range f.members {
			m.remote.Endpoint.Close()
		}
		if f.authority != nil {
			f.authority.Close()
		}
	}()
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
