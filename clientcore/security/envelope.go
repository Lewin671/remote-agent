// Package security implements the sans-IO client security gate: membership
// chain verification, grant opening via externalized KEM, envelope opening/sealing,
// strict protobuf body validation, lease checking, and WebCrypto offload routing.
package security

import (
	"bytes"

	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/shared/devicesec"
)

// Projection builds the unauthenticated routing header projection from h.
func Projection(h devicesec.Header) *v1.Header {
	return &v1.Header{
		Version:     devicesec.Version,
		Kind:        v1.PayloadKind(h.Kind),
		MachineId:   h.Machine,
		SessionId:   h.Session,
		RequestId:   h.Request,
		Seq:         h.Seq,
		DeadlineMs:  int64(h.Deadline),
		Sender:      h.Sender,
		Compression: v1.Compression(h.Compression),
	}
}

func headersEqual(a, b *v1.Header) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Version == b.Version &&
		a.Kind == b.Kind &&
		a.MachineId == b.MachineId &&
		a.SessionId == b.SessionId &&
		a.RequestId == b.RequestId &&
		a.Seq == b.Seq &&
		a.DeadlineMs == b.DeadlineMs &&
		a.Sender == b.Sender &&
		a.Compression == b.Compression
}

// ToProtoEnvelope converts a canonical signed devicesec.Ciphertext to a wire v1.Envelope.
func ToProtoEnvelope(w devicesec.Ciphertext) (*v1.Envelope, error) {
	h, err := devicesec.ParseHeader(w.Header)
	if err != nil {
		return nil, err
	}
	if _, err := w.MarshalBinary(); err != nil {
		return nil, err
	}
	raw, err := Projection(h).Marshal()
	if err != nil {
		return nil, err
	}
	return &v1.Envelope{
		Header:         raw,
		Nonce:          bytes.Clone(w.Nonce),
		Ciphertext:     bytes.Clone(w.Cipher),
		Signature:      bytes.Clone(w.Origin),
		SecurityHeader: bytes.Clone(w.Header),
	}, nil
}

// FromProtoEnvelope validates unknown fields, header projection consistency, and
// wire constraints before returning a devicesec.Ciphertext.
func FromProtoEnvelope(env *v1.Envelope) (devicesec.Ciphertext, error) {
	if env == nil || len(env.Header) > devicesec.MaxHeaderBytes || env.HasUnknown() {
		return devicesec.Ciphertext{}, devicesec.ErrInvalid
	}
	w := devicesec.Ciphertext{
		Header: bytes.Clone(env.SecurityHeader),
		Nonce:  bytes.Clone(env.Nonce),
		Cipher: bytes.Clone(env.Ciphertext),
		Origin: bytes.Clone(env.Signature),
	}
	h, err := devicesec.ParseHeader(w.Header)
	if err != nil {
		return devicesec.Ciphertext{}, err
	}
	var projection v1.Header
	if err := projection.Unmarshal(env.Header); err != nil || projection.HasUnknown() {
		return devicesec.Ciphertext{}, devicesec.ErrUntrusted
	}
	if !headersEqual(&projection, Projection(h)) {
		return devicesec.Ciphertext{}, devicesec.ErrUntrusted
	}
	if _, err := w.MarshalBinary(); err != nil {
		return devicesec.Ciphertext{}, err
	}
	return w, nil
}
