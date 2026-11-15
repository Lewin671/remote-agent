package relay

import (
	"bytes"
	"errors"
	"github.com/Lewin671/remote-agent/internal/transport/peerwire"
	"os"
	"path/filepath"
	"testing"
)

func policyRequest(scope, revision, enabled uint64, sender string) peerwire.Opened {
	peer := []byte(nil)
	if scope == 2 {
		peer = []byte(sender)
	}
	return peerwire.Opened{Context: peerwire.Context{Class: 2, Seq: 7, From: []byte(sender), FromRole: 1, ToRole: 2}, Type: 9, Hash: bytes.Repeat([]byte{8}, 32), Payload: peerwire.PB(peerwire.B(1, peer), peerwire.U(2, revision), peerwire.U(3, enabled), peerwire.U(4, scope))}
}
func TestDirectPolicyScopePersistenceAndReceipt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	p, err := openDirectPolicy(path)
	if err != nil {
		t.Fatal(err)
	}
	owner := "11111111111111111111111111111111"
	client := "22222222222222222222222222222222"
	if !p.enabled(client) {
		t.Fatal("default not auto")
	}
	if _, err = p.apply(policyRequest(1, 1, 0, client), owner); err == nil {
		t.Fatal("ordinary client changed machine policy")
	}
	if _, err = p.apply(policyRequest(2, 1, 1, client), owner); err == nil {
		t.Fatal("ordinary client enabled policy")
	}
	request := policyRequest(2, 1, 0, client)
	reply, err := p.apply(request, owner)
	if err != nil {
		t.Fatal(err)
	}
	f, err := peerwire.ParseMessage(reply, "PolicyReply")
	if err != nil {
		t.Fatal(err)
	}
	if f[4].U != request.Context.Seq || !bytes.Equal(f[5].B, request.Hash) || f[6].U != 2 || string(f[7].B) != client {
		t.Fatal("reply not bound to original request/scope")
	}
	if p.enabled(client) || !p.enabled(owner) {
		t.Fatal("peer close crossed scope")
	}
	p, err = openDirectPolicy(path)
	if err != nil || p.enabled(client) {
		t.Fatal("restart re-enabled peer", err)
	}
	if _, err = p.apply(policyRequest(1, 3, 0, owner), owner); err != nil {
		t.Fatal(err)
	}
	if p.enabled(owner) {
		t.Fatal("machine policy not applied")
	}
	if _, err = p.apply(policyRequest(1, 2, 1, owner), owner); err != nil || p.enabled(owner) {
		t.Fatal("stale policy re-enabled machine")
	}
	if err = p.setLocal(true); err != nil || !p.enabled(owner) {
		t.Fatal("local enable", err)
	}
	p, err = openDirectPolicy(path)
	if err != nil || !p.enabled(owner) || p.enabled(client) {
		t.Fatal("scoped state lost across restart", err)
	}
}
func TestDirectPolicyFailedPersistenceNeverClaimsApplied(t *testing.T) {
	p, err := openDirectPolicy(filepath.Join(t.TempDir(), "policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	owner := "11111111111111111111111111111111"
	p.save = func(string, []byte) error { return errors.New("test storage failure") }
	if reply, err := p.apply(policyRequest(1, 1, 0, owner), owner); err == nil || reply != nil {
		t.Fatal("failed write acknowledged")
	}
	if !p.enabled(owner) {
		t.Fatal("failed write installed volatile policy")
	}
}
func TestDirectPolicyCorruptionFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, []byte(`{"version":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := openDirectPolicy(path); err == nil {
		t.Fatal("missing policy fields silently enabled")
	}
}

func TestDirectPolicyOffThenOnCannotReviveOldPCGrant(t *testing.T) {
	p, err := openDirectPolicy(filepath.Join(t.TempDir(), "policy.json"))
	if err != nil {
		t.Fatal(err)
	}
	peer := "11111111111111111111111111111111"
	grant := p.version(peer)
	if !p.matches(peer, grant) {
		t.Fatal("initial grant")
	}
	if err = p.setLocal(false); err != nil {
		t.Fatal(err)
	}
	if err = p.setLocal(true); err != nil {
		t.Fatal(err)
	}
	if p.matches(peer, grant) || !p.matches(peer, p.version(peer)) {
		t.Fatal("rapid enable revived the closed generation")
	}
}
