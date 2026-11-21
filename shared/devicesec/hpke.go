package devicesec

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/sha256"
)

var (
	kemSuiteID  = []byte{'K', 'E', 'M', 0x00, 0x20}
	hpkeSuiteID = []byte{'H', 'P', 'K', 'E', 0x00, 0x20, 0x00, 0x01, 0x00, 0x02}
)

func labeledExtract(salt, suiteID, label, ikm []byte) ([]byte, error) {
	labeled := make([]byte, 0, len("HPKE-v1")+len(suiteID)+len(label)+len(ikm))
	labeled = append(labeled, "HPKE-v1"...)
	labeled = append(labeled, suiteID...)
	labeled = append(labeled, label...)
	labeled = append(labeled, ikm...)
	return hkdf.Extract(sha256.New, labeled, salt)
}

func labeledExpand(prk, suiteID, label, info []byte, length int) ([]byte, error) {
	labeledInfo := make([]byte, 0, 2+len("HPKE-v1")+len(suiteID)+len(label)+len(info))
	labeledInfo = append(labeledInfo, byte(length>>8), byte(length))
	labeledInfo = append(labeledInfo, "HPKE-v1"...)
	labeledInfo = append(labeledInfo, suiteID...)
	labeledInfo = append(labeledInfo, label...)
	labeledInfo = append(labeledInfo, info...)
	return hkdf.Expand(sha256.New, prk, string(labeledInfo), length)
}

func isZero32(b []byte) bool {
	if len(b) != 32 {
		return true
	}
	var acc byte
	for _, v := range b {
		acc |= v
	}
	return acc == 0
}

func hpkeBaseAEADKeyNonce(recipientPub, enc, dh, info []byte) (key, nonce []byte, err error) {
	if len(recipientPub) != 32 || len(enc) != 32 || len(dh) != 32 || isZero32(recipientPub) || isZero32(enc) || isZero32(dh) {
		return nil, nil, ErrInvalid
	}
	kemContext := make([]byte, 0, 64)
	kemContext = append(kemContext, enc...)
	kemContext = append(kemContext, recipientPub...)

	eaePRK, err := labeledExtract(nil, kemSuiteID, []byte("eae_prk"), dh)
	if err != nil {
		return nil, nil, err
	}
	sharedSecret, err := labeledExpand(eaePRK, kemSuiteID, []byte("shared_secret"), kemContext, 32)
	if err != nil {
		return nil, nil, err
	}

	pskIDHash, err := labeledExtract(nil, hpkeSuiteID, []byte("psk_id_hash"), nil)
	if err != nil {
		return nil, nil, err
	}
	infoHash, err := labeledExtract(nil, hpkeSuiteID, []byte("info_hash"), info)
	if err != nil {
		return nil, nil, err
	}
	ksContext := make([]byte, 0, 1+len(pskIDHash)+len(infoHash))
	ksContext = append(ksContext, 0x00)
	ksContext = append(ksContext, pskIDHash...)
	ksContext = append(ksContext, infoHash...)

	secret, err := labeledExtract(sharedSecret, hpkeSuiteID, []byte("secret"), nil)
	if err != nil {
		return nil, nil, err
	}
	key, err = labeledExpand(secret, hpkeSuiteID, []byte("key"), ksContext, 32)
	if err != nil {
		return nil, nil, err
	}
	nonce, err = labeledExpand(secret, hpkeSuiteID, []byte("base_nonce"), ksContext, 12)
	if err != nil {
		return nil, nil, err
	}
	return key, nonce, nil
}

// HPKESealWithDH performs single-shot RFC 9180 Base mode HPKE encryption using
// DHKEM(X25519, HKDF-SHA256) / HKDF-SHA256 / AES-256-GCM with an externalized KEM
// output (enc, dh = X25519(ephemeral_priv, recipientPub)).
func HPKESealWithDH(recipientPub, enc, dh, info, aad, plaintext []byte) ([]byte, error) {
	key, nonce, err := hpkeBaseAEADKeyNonce(recipientPub, enc, dh, info)
	if err != nil {
		return nil, err
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	return aead.Seal(nil, nonce, plaintext, aad), nil
}

// HPKEOpenWithDH performs single-shot RFC 9180 Base mode HPKE decryption using
// DHKEM(X25519, HKDF-SHA256) / HKDF-SHA256 / AES-256-GCM with an externalized KEM
// shared secret (dh = X25519(recipient_priv, enc)).
func HPKEOpenWithDH(recipientPub, enc, dh, info, aad, ciphertext []byte) ([]byte, error) {
	key, nonce, err := hpkeBaseAEADKeyNonce(recipientPub, enc, dh, info)
	if err != nil {
		return nil, err
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	out, err := aead.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, ErrUntrusted
	}
	return out, nil
}
