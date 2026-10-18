package deviceauth

import (
	"bytes"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
)

// Projection is routing metadata only. The canonical Header is the AEAD AAD.
func (h Header) Projection() *pb.Header {
	return &pb.Header{Version: Version, Kind: pb.PayloadKind(h.Kind), MachineId: h.Machine, SessionId: h.Session, RequestId: h.Request, Seq: h.Seq, DeadlineMs: int64(h.Deadline), Sender: h.Sender, Compression: pb.Compression(h.Compression)}
}
func (w Wire) Envelope() (*pb.Envelope, error) {
	h, err := ParseHeader(w.Header)
	if err != nil {
		return nil, err
	}
	if _, err = w.MarshalBinary(); err != nil {
		return nil, err
	}
	raw, err := proto.Marshal(h.Projection())
	if err != nil {
		return nil, err
	}
	return &pb.Envelope{Header: raw, Nonce: bytes.Clone(w.Nonce), Ciphertext: bytes.Clone(w.Cipher), Signature: bytes.Clone(w.Origin), SecurityHeader: bytes.Clone(w.Header)}, nil
}
func FromEnvelope(env *pb.Envelope) (Wire, error) {
	if env == nil || len(env.Header) > MaxHeaderBytes || len(env.ProtoReflect().GetUnknown()) != 0 {
		return Wire{}, ErrInvalid
	}
	w := Wire{Header: bytes.Clone(env.SecurityHeader), Nonce: bytes.Clone(env.Nonce), Cipher: bytes.Clone(env.Ciphertext), Origin: bytes.Clone(env.Signature)}
	h, err := ParseHeader(w.Header)
	if err != nil {
		return Wire{}, err
	}
	var projection pb.Header
	if proto.Unmarshal(env.Header, &projection) != nil || !proto.Equal(&projection, h.Projection()) {
		return Wire{}, ErrUntrusted
	}
	if _, err := w.MarshalBinary(); err != nil {
		return Wire{}, err
	}
	return w, nil
}
