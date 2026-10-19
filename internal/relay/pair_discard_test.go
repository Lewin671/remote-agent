package relay

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscardRegistrationKeepsReceiptInItsStateDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pair-admission-home")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REMOTE_AGENT_HOME", dir)
	pending := pairAdmission{Account: &Account{Server: "https://relay.example.com"}, PairingID: "synthetic-pair", Request: []byte("synthetic-private-receipt")}
	if err := savePairAdmission(pending); err != nil {
		t.Fatal(err)
	}
	if err := DiscardRegistration(); err != nil {
		t.Fatal(err)
	}
	active, _ := filepath.Glob(filepath.Join(dir, "pair-admission-*.json"))
	if len(active) != 0 {
		t.Fatal("retry slot not cleared")
	}
	owned, _ := filepath.Glob(filepath.Join(dir, "pair-owned-*.json"))
	if len(owned) != 1 {
		t.Fatal("receipt escaped its private directory")
	}
}
