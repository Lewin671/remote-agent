package peerwire

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"fmt"
)

func fixture(id string, role uint64) Identity {
	seed := Hash([]byte("lab/sign/" + id))
	box, e := ecdh.X25519().NewPrivateKey(Hash([]byte("lab/box/" + id)))
	if e != nil {
		panic(e)
	}
	sign := ed25519.NewKeyFromSeed(seed)
	deviceID := fmt.Sprintf("%x", Hash(sign.Public().(ed25519.PublicKey))[:16])
	return Identity{[]byte(deviceID), Hash([]byte("lab/instance/" + id))[:16], Hash([]byte("lab/boot/" + id)), role, sign, box}
}

func fixtureCodec() *Codec {
	return &Codec{[]byte("0123456789abcdef0123456789abcdef"), Hash([]byte("lab/account-domain")), Hash([]byte("lab/member-head/1")), 1}
}
