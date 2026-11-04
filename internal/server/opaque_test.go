package server

import (
	"crypto/rand"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
)

// These storage/routing fixtures are opaque ciphertext, not an obsolete cipher
// implementation. Endpoint authentication is exercised by the deviceauth suites.
func opaqueEnvelope(h *pb.Header, plain []byte) (*pb.Envelope, error) {
	raw, err := proto.Marshal(h)
	if err != nil {
		return nil, err
	}
	nonce, cipher := make([]byte, 12), make([]byte, len(plain)+16)
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	if _, err = rand.Read(cipher); err != nil {
		return nil, err
	}
	return &pb.Envelope{Header: raw, Nonce: nonce, Ciphertext: cipher}, nil
}
func opaqueMessage(h *pb.Header, m proto.Message) (*pb.Envelope, error) {
	raw, err := proto.Marshal(m)
	if err != nil {
		return nil, err
	}
	return opaqueEnvelope(h, raw)
}
