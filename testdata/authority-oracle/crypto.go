// Test-only deterministic encryption and signing helpers shared by both fixture profiles.
// RFC 9180 sections 4, 5.1 and 5.2 supply fixed sender inputs without changing
// production randomness. Every grant is opened by production Pin.OpenGrant.
package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	da "github.com/Lewin671/remote-agent/internal/deviceauth"
)

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
func check(err error) {
	if err != nil {
		panic(err)
	}
}
func fill(n, b int) []byte         { return bytes.Repeat([]byte{byte(b)}, n) }
func key(b int) ed25519.PrivateKey { return ed25519.NewKeyFromSeed(fill(32, b)) }
func public(b int) [32]byte        { return [32]byte(key(b).Public().(ed25519.PublicKey)) }
func member(b, x int, kind da.Kind) da.Member {
	return must(da.NewMember(key(b).Public().(ed25519.PublicKey), must(da.XPublic(fill(32, x))), kind))
}
func u16(n int) []byte            { return binary.BigEndian.AppendUint16(nil, uint16(n)) }
func u64(n uint64) []byte         { return binary.BigEndian.AppendUint64(nil, n) }
func lp(b []byte) []byte          { return append(binary.BigEndian.AppendUint32(nil, uint32(len(b))), b...) }
func join(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
func hash(parts ...[]byte) []byte { sum := sha256.Sum256(join(parts...)); return sum[:] }
func extract(suite, salt []byte, label string, ikm []byte) []byte {
	return must(hkdf.Extract(sha256.New, join([]byte("HPKE-v1"), suite, []byte(label), ikm), salt))
}
func expand(suite, prk []byte, label string, info []byte, n int) []byte {
	return must(hkdf.Expand(sha256.New, prk, string(join(u16(n), []byte("HPKE-v1"), suite, []byte(label), info)), n))
}
func gcm(secret []byte) cipher.AEAD { return must(cipher.NewGCM(must(aes.NewCipher(secret)))) }
func previousSecret(newSecret, oldSecret, nonce []byte) []byte {
	return append(nonce, gcm(newSecret).Seal(nil, nonce, oldSecret, []byte("ra3/prev-secret\x00"))...)
}

func grant(c da.Core, ch []byte, m da.Member, secret, ephemeral []byte) da.Grant {
	priv := must(ecdh.X25519().NewPrivateKey(ephemeral))
	enc := priv.PublicKey().Bytes()
	dh := must(priv.ECDH(must(ecdh.X25519().NewPublicKey(m.XPublic[:]))))
	kemSuite := join([]byte("KEM"), u16(0x20))
	shared := expand(kemSuite, extract(kemSuite, nil, "eae_prk", dh), "shared_secret", join(enc, m.XPublic[:]), 32)
	suite := join([]byte("HPKE"), u16(0x20), u16(1), u16(2))
	ctx := join(lp([]byte(c.Account)), c.Domain[:], u64(c.ContentEpoch), ch, lp([]byte(m.ID)))
	info := join([]byte("ra3/grant-info\x00"), ctx)
	ks := join([]byte{0}, extract(suite, nil, "psk_id_hash", nil), extract(suite, nil, "info_hash", info))
	root := extract(suite, shared, "secret", nil)
	k := expand(suite, root, "key", ks, 32)
	nonce := expand(suite, root, "base_nonce", ks, 12)
	return da.Grant{Recipient: m.ID, Enc: [32]byte(enc), Cipher: gcm(k).Seal(nil, nonce, secret, join([]byte("ra3/grant-aad\x00"), ctx))}
}
func fixedState(s da.State, secret, previous []byte, signer ed25519.PrivateKey, ephemeral []byte) da.State {
	s.PrevSecret = previous
	ch := hash([]byte("ra3/core\x00"), must(s.Core.MarshalBinary()))
	s.Grants = nil
	for _, m := range s.Core.Members {
		s.Grants = append(s.Grants, grant(s.Core, ch, m, secret, ephemeral))
	}
	h := must(s.Hash())
	c := s.Core
	signed := join([]byte("ra3/state-sig\x00"), lp([]byte(c.Account)), c.Domain[:], c.Parent[:], u64(c.Epoch), u64(c.ContentEpoch), u64(c.IssuedAt), lp([]byte(c.AuthorityID)), c.RecoveryEd[:], h[:])
	s.Signature = ed25519.Sign(signer, signed)
	return s
}

func code(seed, genesis []byte) string {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	payload := join(genesis[:10], seed)
	out := ""
	bits := 0
	value := uint32(0)
	for _, b := range payload {
		value = value<<8 | uint32(b)
		bits += 8
		for bits >= 5 {
			bits -= 5
			out += string(alphabet[value>>bits&31])
		}
	}
	if bits > 0 {
		out += string(alphabet[value<<(5-bits)&31])
	}
	groups := []string{}
	for len(out) > 4 {
		groups = append(groups, out[:4])
		out = out[4:]
	}
	groups = append(groups, out)
	return string(bytes.Join(func() [][]byte {
		a := [][]byte{}
		for _, g := range groups {
			a = append(a, []byte(g))
		}
		return a
	}(), []byte("-")))
}
