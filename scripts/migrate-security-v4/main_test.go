package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Lewin671/remote-agent/internal/deviceauth"
)

func fixture(t *testing.T) (string, []byte) {
	t.Helper()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "daemon.lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	_, ed, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	x, xpub, err := deviceauth.NewXKey()
	if err != nil {
		t.Fatal(err)
	}
	m, err := deviceauth.NewMember(ed.Public().(ed25519.PublicKey), xpub, deviceauth.Client)
	if err != nil {
		t.Fatal(err)
	}
	var recovery [32]byte
	recovery[0] = 1
	genesis, err := deviceauth.BuildGenesis("0123456789abcdef0123456789abcdef", deviceauth.Hash{1}, m, recovery, bytes.Repeat([]byte{2}, 32), ed, uint64(time.Now().UnixMilli())-1000)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := genesis.Pin()
	if err != nil {
		t.Fatal(err)
	}
	lease, err := deviceauth.BuildLease(genesis, ed, genesis.Core.IssuedAt)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, "security-v3.db")
	ep, err := deviceauth.InitEndpoint(path, pin, []deviceauth.State{genesis}, []*deviceauth.RecoveryStatement{nil}, m.ID, ed.Seed(), x, lease)
	if err != nil {
		t.Fatal(err)
	}
	ep.Close()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`DROP TABLE accepted; DROP TABLE accepted_bodies; CREATE TABLE accepted(key BLOB PRIMARY KEY,deadline INTEGER NOT NULL,done INTEGER NOT NULL,result BLOB,failed INTEGER NOT NULL,checksum BLOB NOT NULL); UPDATE security SET version=3;INSERT INTO accepted VALUES(X'01',1,1,X'02',0,zeroblob(32));`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(home, "account.json"), []byte("account-sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(filepath.Join(home, "sessions"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(home, "sessions", "session-sentinel"), []byte("session"), 0600); err != nil {
		t.Fatal(err)
	}
	return home, original
}
func TestDryRunAndMigrationPreserveIdentity(t *testing.T) {
	home, original := fixture(t)
	if err := os.WriteFile(filepath.Join(home, "state-generation"), []byte("3\n"), 0600); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(home, "security-v3.db")
	if err := migrate(home, true); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(source)
	if !bytes.Equal(after, original) || !absent(filepath.Join(home, "security-v4.db")) || !absent(source+".backup") {
		t.Fatal("dry run published or changed source")
	}
	if b, _ := os.ReadFile(filepath.Join(home, "state-generation")); string(b) != "3\n" {
		t.Fatal("dry run changed marker")
	}
	if err := migrate(home, false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(home, "state-generation")); string(b) != "4\n" {
		t.Fatal("migration did not publish state generation")
	}
	ep, err := deviceauth.OpenEndpoint(filepath.Join(home, "security-v4.db"))
	if err != nil {
		t.Fatal(err)
	}
	ep.Close()
	after, _ = os.ReadFile(source)
	backup, _ := os.ReadFile(source + ".backup")
	if !bytes.Equal(after, original) || len(backup) == 0 {
		t.Fatal("source changed or missing backup")
	}
	for name, want := range map[string]string{"account.json": "account-sentinel", "sessions/session-sentinel": "session"} {
		raw, _ := os.ReadFile(filepath.Join(home, name))
		if string(raw) != want {
			t.Fatal("changed non-security state")
		}
	}
	db, _ := sql.Open("sqlite", filepath.Join(home, "security-v4.db"))
	defer db.Close()
	var count int
	if db.QueryRow(`SELECT count(*) FROM accepted`).Scan(&count) != nil || count != 0 {
		t.Fatal("migrated old acceptance")
	}
	if err = migrate(home, false); err == nil {
		t.Fatal("overwrote existing migration")
	}
}
func TestMigrationRefusesActiveDaemonAndUnexpiredIDs(t *testing.T) {
	home, _ := fixture(t)
	unlock, err := lockDaemon(filepath.Join(home, "daemon.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if migrate(home, true) == nil {
		unlock()
		t.Fatal("migrated running daemon")
	}
	unlock()
	db, _ := sql.Open("sqlite", filepath.Join(home, "security-v3.db"))
	_, err = db.Exec(`UPDATE accepted SET deadline=?`, time.Now().Add(time.Minute).UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if migrate(home, false) == nil {
		t.Fatal("lost protection for unexpired v3 requests")
	}
	if !absent(filepath.Join(home, "security-v4.db")) {
		t.Fatal("published failed migration")
	}
}
func TestMigrationFailureLeavesSourceRecoverable(t *testing.T) {
	home, original := fixture(t)
	db, _ := sql.Open("sqlite", filepath.Join(home, "security-v3.db"))
	_, err := db.Exec(`UPDATE states SET hash=zeroblob(32)`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	// The signed state itself is still valid; corrupt it instead.
	db, _ = sql.Open("sqlite", filepath.Join(home, "security-v3.db"))
	_, err = db.Exec(`UPDATE states SET state=X'00'`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	if migrate(home, false) == nil {
		t.Fatal("published invalid identity")
	}
	if !absent(filepath.Join(home, "security-v4.db")) || !absent(filepath.Join(home, "security-v3.db.backup")) {
		t.Fatal("failed verification published files")
	}
	// A backup restore followed by retry works without new identity material.
	if err = os.WriteFile(filepath.Join(home, "security-v3.db"), original, 0600); err != nil {
		t.Fatal(err)
	}
	if err = migrate(home, false); err != nil {
		t.Fatal(err)
	}
}
