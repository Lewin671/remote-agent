package main

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Lewin671/remote-agent/internal/i18n"
)

func TestMigrationRejectsEveryCorruptV3AcceptanceAndReportsIt(t *testing.T) {
	mutations := map[string]string{
		"deadline": `UPDATE accepted SET deadline=1 WHERE key=X'01'`,
		"key":      `UPDATE accepted SET key=X'03' WHERE key=X'01'`,
		"done":     `UPDATE accepted SET done=0 WHERE key=X'01'`,
		"result":   `UPDATE accepted SET result=X'04' WHERE key=X'01'`,
		"failed":   `UPDATE accepted SET failed=1 WHERE key=X'01'`,
		"checksum": `UPDATE accepted SET checksum=zeroblob(32) WHERE key=X'01'`,
	}
	for name, mutation := range mutations {
		for _, dry := range []bool{false, true} {
			for _, locale := range []string{"en", "zh-CN"} {
				t.Run(fmt.Sprintf("%s/dry=%v/%s", name, dry, locale), func(t *testing.T) {
					home, _ := fixture(t)
					source := filepath.Join(home, "security-v3.db")
					marker := filepath.Join(home, "state-generation")
					if err := os.WriteFile(marker, []byte("3\n"), 0600); err != nil {
						t.Fatal(err)
					}
					db, err := sql.Open("sqlite", source)
					if err != nil {
						t.Fatal(err)
					}
					if name == "deadline" {
						future := uint64(time.Now().Add(time.Minute).UnixMilli())
						if _, err = db.Exec(`UPDATE accepted SET deadline=?,checksum=? WHERE key=X'01'`, future, v3AcceptedChecksum([]byte{1}, future, 1, []byte{2}, 0)); err != nil {
							t.Fatal(err)
						}
					}
					// A valid row owns MAX(deadline) after the corruption. Checking only
					// that row must not hide the smaller damaged row's live authorization.
					if _, err = db.Exec(`INSERT INTO accepted VALUES(?,100,1,?,0,?)`, []byte{2}, []byte{2}, v3AcceptedChecksum([]byte{2}, 100, 1, []byte{2}, 0)); err != nil {
						t.Fatal(err)
					}
					if _, err = db.Exec(mutation); err != nil {
						t.Fatal(err)
					}
					if err = integrity(db); err != nil {
						t.Fatal("corruption fixture must pass SQLite integrity", err)
					}
					db.Close()
					before, err := os.ReadFile(source)
					if err != nil {
						t.Fatal(err)
					}
					if err = migrate(home, dry); !errors.Is(err, errV3Acceptance) {
						t.Fatalf("damaged acceptance was trusted: %v", err)
					}
					var stdout, stderr bytes.Buffer
					args := []string{"--home", home, "--locale", locale}
					if dry {
						args = append(args, "--dry-run")
					}
					if code := run(args, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), i18n.SecurityMigrationAcceptanceCorrupt(locale)) {
						t.Fatalf("missing explicit checksum diagnostic: exit=%d stderr=%q", code, stderr.String())
					}
					after, _ := os.ReadFile(source)
					generation, _ := os.ReadFile(marker)
					if !bytes.Equal(before, after) || string(generation) != "3\n" || !absent(source+".backup") || !absent(filepath.Join(home, "security-v4.db")) {
						t.Fatal("corrupt source was changed or published")
					}
				})
			}
		}
	}
}
