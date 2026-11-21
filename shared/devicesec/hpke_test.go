package devicesec

import (
	"bytes"
	"crypto/ecdh"
	"crypto/hpke"
	"crypto/rand"
	"testing"
)

func TestHPKEBidirectionalStdlibInterop(t *testing.T) {
	curve := ecdh.X25519()
	for i := range 200 {
		privR, err := curve.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		privE, err := curve.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		info := make([]byte, (i%37)+1)
		aad := make([]byte, (i%41)+1)
		plain := make([]byte, (i%64)+1)
		_, _ = rand.Read(info)
		_, _ = rand.Read(aad)
		_, _ = rand.Read(plain)

		// 1) HPKESealWithDH -> stdlib crypto/hpke.Recipient.Open
		dh, err := privE.ECDH(privR.PublicKey())
		if err != nil {
			t.Fatal(err)
		}
		enc := privE.PublicKey().Bytes()
		ct, err := HPKESealWithDH(privR.PublicKey().Bytes(), enc, dh, info, aad, plain)
		if err != nil {
			t.Fatalf("case %d HPKESealWithDH: %v", i, err)
		}
		hkRecip, err := hpke.NewDHKEMPrivateKey(privR)
		if err != nil {
			t.Fatal(err)
		}
		recip, err := hpke.NewRecipient(enc, hkRecip, hpke.HKDFSHA256(), hpke.AES256GCM(), info)
		if err != nil {
			t.Fatalf("case %d hpke.NewRecipient: %v", i, err)
		}
		got, err := recip.Open(aad, ct)
		if err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("case %d stdlib recip open mismatch: got=%x want=%x err=%v", i, got, plain, err)
		}

		// 2) stdlib crypto/hpke.Sender.Seal -> HPKEOpenWithDH
		hkPub, err := hpke.NewDHKEMPublicKey(privR.PublicKey())
		if err != nil {
			t.Fatal(err)
		}
		enc2, sender, err := hpke.NewSender(hkPub, hpke.HKDFSHA256(), hpke.AES256GCM(), info)
		if err != nil {
			t.Fatalf("case %d hpke.NewSender: %v", i, err)
		}
		ct2, err := sender.Seal(aad, plain)
		if err != nil {
			t.Fatalf("case %d stdlib sender seal: %v", i, err)
		}
		encPub2, err := curve.NewPublicKey(enc2)
		if err != nil {
			t.Fatal(err)
		}
		dh2, err := privR.ECDH(encPub2)
		if err != nil {
			t.Fatal(err)
		}
		got2, err := HPKEOpenWithDH(privR.PublicKey().Bytes(), enc2, dh2, info, aad, ct2)
		if err != nil || !bytes.Equal(got2, plain) {
			t.Fatalf("case %d HPKEOpenWithDH mismatch: got=%x want=%x err=%v", i, got2, plain, err)
		}
	}
}

func TestHPKERejectsTamperAndZeroDH(t *testing.T) {
	curve := ecdh.X25519()
	privR, _ := curve.GenerateKey(rand.Reader)
	privE, _ := curve.GenerateKey(rand.Reader)
	dh, _ := privE.ECDH(privR.PublicKey())
	pubR := privR.PublicKey().Bytes()
	enc := privE.PublicKey().Bytes()

	ct, err := HPKESealWithDH(pubR, enc, dh, []byte("info"), []byte("aad"), []byte("secret32byteslong!secret32bytes!"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := HPKEOpenWithDH(pubR, enc, dh, []byte("info2"), []byte("aad"), ct); err == nil {
		t.Fatal("expected info mismatch failure")
	}
	if _, err := HPKEOpenWithDH(pubR, enc, dh, []byte("info"), []byte("aad2"), ct); err == nil {
		t.Fatal("expected aad mismatch failure")
	}
	badCT := bytes.Clone(ct)
	badCT[len(badCT)-1] ^= 1
	if _, err := HPKEOpenWithDH(pubR, enc, dh, []byte("info"), []byte("aad"), badCT); err == nil {
		t.Fatal("expected tag tamper failure")
	}
	zero := make([]byte, 32)
	if _, err := HPKESealWithDH(pubR, enc, zero, []byte("info"), []byte("aad"), []byte("x")); err == nil {
		t.Fatal("expected zero dh rejection on seal")
	}
	if _, err := HPKEOpenWithDH(pubR, enc, zero, []byte("info"), []byte("aad"), ct); err == nil {
		t.Fatal("expected zero dh rejection on open")
	}
}
