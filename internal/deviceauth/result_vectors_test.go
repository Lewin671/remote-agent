package deviceauth

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/outcome"
	"google.golang.org/protobuf/proto"
	"os"
	"testing"
)

type proofVector struct{ Name, Envelope, OuterOutcome, OuterReason, Expected string }
type proofVectors struct {
	State, Secret, Command, Plain, Key, Payload string
	Vectors                                     []proofVector
}

func TestSharedProofVectors(t *testing.T) {
	const path = "../../testdata/transport/proofs.json"
	raw := func(s string) []byte {
		b, e := base64.StdEncoding.DecodeString(s)
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	hexraw := func(s string) []byte {
		b, e := hex.DecodeString(s)
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	b64 := func(b []byte) string { return base64.StdEncoding.EncodeToString(b) }
	var f proofVectors
	if os.Getenv("GEN_OUTCOME_VECTORS") == "1" {
		base := buildVectors(t)
		state, _ := ParseState(raw(base.Added.State))
		sh, _ := state.Hash()
		phone := ed25519.NewKeyFromSeed(hexraw(base.PhoneSeed))
		machine := ed25519.NewKeyFromSeed(hexraw(base.MachineSeed))
		secret := hexraw(base.ContentSecret2)
		h := Header{Account: state.Core.Account, Domain: state.Core.Domain, Epoch: state.Core.Epoch, State: sh, ContentEpoch: state.Core.ContentEpoch, Sender: ID(phone.Public().(ed25519.PublicKey)), Kind: 5, Purpose: Command, Machine: ID(machine.Public().(ed25519.PublicKey)), Session: "fixture-session", Request: "0123456789abcdef0123456789abcdef", Deadline: 1700000600000}
		plain, _ := proto.Marshal(&pb.Command{ProtocolVersion: 15, Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: h.Session, Text: "fixture", RequestId: h.Request}}})
		w, _ := Seal(secret, nil, h, plain, phone)
		env, _ := w.Envelope()
		encoded, _ := proto.Marshal(env)
		key, payload, _ := RequestIdentity(h, plain)
		f = proofVectors{State: base.Added.State, Secret: base.ContentSecret2, Command: b64(encoded), Plain: b64(plain), Key: hex.EncodeToString(key[:]), Payload: hex.EncodeToString(payload[:])}
		for _, kind := range []string{"not_sent", "rejected", "executed", "unknown", "business", "identity", "missing_body", "bad_signature", "bad_key", "bad_payload", "outer_category", "outer_reason", "missing_category", "bad_conflict", "body_without_availability"} {
			r := &pb.CommandResult{Outcome: kind, Reason: "completed", RequestKeyHash: key[:], PayloadHash: payload[:]}
			expected := kind
			switch kind {
			case "not_sent":
				r.Reason = "busy"
			case "rejected":
				r.Reason = "invalid_body"
			case "unknown":
				r.Reason = "outcome_unknown"
			case "business", "identity":
				r.Outcome = "conflict"
				r.ConflictKind = kind
				expected = "conflict"
			default:
				r.Outcome = "executed"
				r.ResultAvailable = true
				expected = "executed"
			}
			switch kind {
			case "missing_body":
				r.ResultAvailable = false
			case "bad_key":
				r.RequestKeyHash = make([]byte, 32)
				expected = "unknown"
			case "bad_payload":
				r.PayloadHash = make([]byte, 32)
				expected = "unknown"
			case "missing_category":
				r.Outcome = ""
				expected = "unknown"
			case "bad_conflict":
				r.Outcome = "conflict"
				r.ConflictKind = "future"
				expected = "unknown"
			case "body_without_availability":
				r.ResultAvailable = false
				r.Kind = &pb.CommandResult_Send{Send: &pb.SendResponse{}}
				expected = "unknown"
			}
			resultBody, _ := proto.Marshal(r)
			rh := h
			rh.Kind = 6
			rh.Purpose = Result
			rh.Sender = h.Machine
			rh.Deadline = 0
			resultWire, e := Seal(secret, nil, rh, resultBody, machine)
			if e != nil {
				t.Fatal(e)
			}
			resultEnv, _ := resultWire.Envelope()
			if kind == "bad_signature" {
				resultEnv.Signature[0] ^= 1
				expected = "unknown"
			}
			encoded, _ := proto.Marshal(resultEnv)
			v := proofVector{Name: kind, Envelope: b64(encoded), OuterOutcome: r.Outcome, OuterReason: r.Reason, Expected: expected}
			if kind == "outer_category" {
				v.OuterOutcome = "rejected"
				v.Expected = "unknown"
			}
			if kind == "outer_reason" {
				v.OuterReason = "other"
				v.Expected = "unknown"
			}
			f.Vectors = append(f.Vectors, v)
		}
		b, _ := json.MarshalIndent(f, "", "  ")
		if e := os.WriteFile(path, append(b, '\n'), 0644); e != nil {
			t.Fatal(e)
		}
	}
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(b, &f); e != nil {
		t.Fatal(e)
	}
	state, _ := ParseState(raw(f.State))
	secret := hexraw(f.Secret)
	command := &pb.Envelope{}
	proto.Unmarshal(raw(f.Command), command)
	wire, _ := FromEnvelope(command)
	h, p, e := wire.Open(secret, nil, state, state, false, 0)
	if e != nil {
		t.Fatal(e)
	}
	key, payload, e := RequestIdentity(h, p)
	if e != nil || hex.EncodeToString(key[:]) != f.Key || hex.EncodeToString(payload[:]) != f.Payload {
		t.Fatal("request binding drift", e)
	}
	for _, v := range f.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			env := &pb.Envelope{}
			proto.Unmarshal(raw(v.Envelope), env)
			w, e := FromEnvelope(env)
			got := outcome.Unknown
			if e == nil {
				_, plain, e := w.Open(secret, nil, state, state, false, 0)
				if e == nil {
					r := &pb.CommandResult{}
					if proto.Unmarshal(plain, r) == nil {
						got = outcome.Verify(r, key[:], payload[:], v.OuterOutcome, v.OuterReason, false)
					}
				}
			}
			if string(got) != v.Expected {
				t.Fatalf("got %s want %s", got, v.Expected)
			}
		})
	}
}
