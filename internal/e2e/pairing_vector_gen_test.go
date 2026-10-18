package e2e

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/encoding/protowire"
)

// TestGenerateKotlinPairingAccountKey is a one-off helper for the protocol-10
// pairing payload: the checked-in Kotlin vector was made before the account
// public key was added. It is skipped unless explicitly requested.
func TestGenerateKotlinPairingAccountKey(t *testing.T) {
	if os.Getenv("GEN_KOTLIN_PAIRING") == "" {
		t.Skip("set GEN_KOTLIN_PAIRING=1 to regenerate")
	}
	path := "testdata/kotlin-vectors.json"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	pairing := doc["pairing"].(map[string]any)
	payload := pairing["payload"].(map[string]any)
	key := unhex(t, pairing["key"].(string))
	env := &pb.Envelope{
		Header:     unhex(t, payload["header"].(string)),
		Nonce:      unhex(t, payload["nonce"].(string)),
		Ciphertext: unhex(t, payload["ciphertext"].(string)),
	}
	h, plain, err := Open(key, env)
	if err != nil {
		t.Fatal(err)
	}
	var server string
	var approval, approvalSig []byte
	for len(plain) > 0 {
		num, typ, n := protowire.ConsumeTag(plain)
		if n < 0 {
			t.Fatal("bad legacy payload")
		}
		plain = plain[n:]
		if typ == protowire.BytesType {
			v, n := protowire.ConsumeBytes(plain)
			if n < 0 {
				t.Fatal("bad legacy payload field")
			}
			switch num {
			case 2:
				server = string(v)
			case 3:
				approval = v
			case 4:
				approvalSig = v
			}
			plain = plain[n:]
			continue
		}
		n = protowire.ConsumeFieldValue(num, typ, plain)
		if n < 0 {
			t.Fatal("bad legacy payload value")
		}
		plain = plain[n:]
	}
	next := &pb.PairingPayload{
		Server: server, Approval: approval, ApprovalSignature: approvalSig,
		AccountPublicKey: unhex(t, doc["account_public_key"].(string)),
	}
	sealed, err := SealMessage(key, &pb.Header{
		Kind: pb.PayloadKind_PAYLOAD_KIND_PAIRING, RequestId: pairing["pairing_id"].(string), Sender: h.Sender,
	}, next)
	if err != nil {
		t.Fatal(err)
	}
	payload["header"] = hex.EncodeToString(sealed.Header)
	payload["nonce"] = hex.EncodeToString(sealed.Nonce)
	payload["ciphertext"] = hex.EncodeToString(sealed.Ciphertext)
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}
