package main

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/Lewin671/remote-agent/internal/deviceauth"
)

func TestMigrationRefusesNonStateHomeWithoutCreatingFiles(t *testing.T) {
	for _, kind := range []string{"missing", "empty", "file", "missing-lock", "fake-store"} {
		for _, dry := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/dry=%v", kind, dry), func(t *testing.T) {
				parent := t.TempDir()
				home := filepath.Join(parent, "home")
				switch kind {
				case "missing":
				case "empty":
					if err := os.Mkdir(home, 0700); err != nil {
						t.Fatal(err)
					}
				case "file":
					if err := os.WriteFile(home, []byte("file"), 0600); err != nil {
						t.Fatal(err)
					}
				case "missing-lock":
					home, _ = fixture(t)
					if err := os.Remove(filepath.Join(home, "daemon.lock")); err != nil {
						t.Fatal(err)
					}
				case "fake-store":
					if err := os.Mkdir(home, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(home, "security-v3.db"), []byte("not a database"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				listing := func() []string {
					var all []string
					_ = filepath.WalkDir(filepath.Dir(home), func(path string, d os.DirEntry, err error) error {
						if err == nil {
							all = append(all, path)
						}
						return nil
					})
					return all
				}
				before := listing()
				if err := migrate(home, dry); err == nil {
					t.Fatal("non-state home accepted")
				}
				if !reflect.DeepEqual(before, listing()) {
					t.Fatal("invalid --home created files")
				}
			})
		}
	}
}

func TestMigrationCutoffDoesNotPinFastClock(t *testing.T) {
	home, _ := fixture(t)
	source := filepath.Join(home, "security-v3.db")
	db, err := sql.Open("sqlite", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE accepted SET deadline=100; UPDATE accepted_meta SET cutoff=200`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	ops := defaultMigrationOps()
	ops.now = func() time.Time { return time.Now().Add(365 * 24 * time.Hour) }
	if err = migrateWith(home, false, ops); err != nil {
		t.Fatal(err)
	}
	next, err := sql.Open("sqlite", filepath.Join(home, "security-v4.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	var cutoff uint64
	if err = next.QueryRow(`SELECT cutoff FROM accepted_meta WHERE id=1`).Scan(&cutoff); err != nil || cutoff != 200 {
		t.Fatalf("migration clock poisoned floor: %d %v", cutoff, err)
	}
}

func TestMigrationRejectsClockBehindExistingWatermarks(t *testing.T) {
	for _, column := range []string{"cutoff", "floor_ms", "floor_saved_ms"} {
		t.Run(column, func(t *testing.T) {
			home, original := fixture(t)
			source := filepath.Join(home, "security-v3.db")
			db, err := sql.Open("sqlite", source)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			future := uint64(now.UnixMilli()) + deviceauth.MaxClockSkew + 1
			table := "security"
			if column == "cutoff" {
				table = "accepted_meta"
			}
			if _, err = db.Exec(`UPDATE `+table+` SET `+column+`=?`, future); err != nil {
				t.Fatal(err)
			}
			db.Close()
			original, err = os.ReadFile(source)
			if err != nil {
				t.Fatal(err)
			}
			ops := defaultMigrationOps()
			ops.now = func() time.Time { return now }
			if err = migrateWith(home, false, ops); err == nil {
				t.Fatal("clock rollback accepted")
			}
			after, _ := os.ReadFile(source)
			if !bytes.Equal(original, after) || !absent(source+".backup") || !absent(filepath.Join(home, "security-v4.db")) {
				t.Fatal("clock rejection changed state")
			}
		})
	}
}

func TestMigrationPublicationFailureCleansOwnFilesAndCanRetry(t *testing.T) {
	for _, phase := range []string{"target", "backup-sync", "target-sync", "target-race"} {
		t.Run(phase, func(t *testing.T) {
			home, original := fixture(t)
			source := filepath.Join(home, "security-v3.db")
			target := filepath.Join(home, "security-v4.db")
			ops := defaultMigrationOps()
			publish := ops.publish
			ops.publish = func(src, dst string) error {
				if dst == target {
					if phase == "target" {
						return os.ErrPermission
					}
					if phase == "target-race" {
						if err := os.WriteFile(dst, []byte("another publisher"), 0600); err != nil {
							return err
						}
					}
				}
				return publish(src, dst)
			}
			syncs := 0
			ops.syncDir = func(path string) error {
				syncs++
				if phase == "backup-sync" && syncs == 1 || phase == "target-sync" && syncs == 2 {
					return os.ErrPermission
				}
				return syncDir(path)
			}
			if err := migrateWith(home, false, ops); err == nil {
				t.Fatal("injected publication failure succeeded")
			}
			if !absent(source + ".backup") {
				t.Fatal("orphaned backup blocks retry")
			}
			after, _ := os.ReadFile(source)
			if !bytes.Equal(original, after) {
				t.Fatal("source changed")
			}
			if phase == "target-race" {
				raw, err := os.ReadFile(target)
				if err != nil || string(raw) != "another publisher" {
					t.Fatal("removed another publisher's target")
				}
				if err = os.Remove(target); err != nil {
					t.Fatal(err)
				}
			} else if !absent(target) {
				t.Fatal("partial target survived rollback")
			}
			if err := migrate(home, false); err != nil {
				t.Fatal("clean retry failed", err)
			}
		})
	}
}

func TestMigrationCopiesWhenHardLinksUnavailable(t *testing.T) {
	home, _ := fixture(t)
	ops := defaultMigrationOps()
	copies := 0
	unsupported := errors.New("test filesystem has no hard links")
	ops.publish = func(src, dst string) error {
		return publishFile(src, dst, func(string, string) error { copies++; return unsupported })
	}
	if err := migrateWith(home, false, ops); err != nil {
		t.Fatal(err)
	}
	if copies != 2 {
		t.Fatal("did not copy both published files")
	}
	ep, err := deviceauth.OpenEndpoint(filepath.Join(home, "security-v4.db"))
	if err != nil {
		t.Fatal(err)
	}
	ep.Close()
	src := filepath.Join(home, "security-v3.db")
	dst := src + ".backup"
	before, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if err = publishFile(src, dst, func(string, string) error { return unsupported }); !os.IsExist(err) {
		t.Fatal("copy fallback overwrote existing file", err)
	}
	after, _ := os.ReadFile(dst)
	if !bytes.Equal(before, after) {
		t.Fatal("existing backup changed")
	}
	partial := filepath.Join(home, "failed-copy")
	if err = copyFile(home, partial); err == nil || !absent(partial) {
		t.Fatal("failed copy left partial destination", err)
	}
}

func TestMigrationReadsUncheckpointedWALAfterKill(t *testing.T) {
	if source := os.Getenv("RA_MIGRATION_WAL_SOURCE"); source != "" {
		db, err := sql.Open("sqlite", source)
		if err != nil {
			panic(err)
		}
		db.SetMaxOpenConns(1)
		if _, err = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; PRAGMA synchronous=FULL; UPDATE security SET floor_ms=700,floor_saved_ms=700; UPDATE accepted SET deadline=500; UPDATE accepted_meta SET cutoff=600;`); err != nil {
			panic(err)
		}
		fmt.Fprintln(os.Stdout, "wal committed")
		select {} // parent kills the recorded child, without Close/checkpoint.
	}
	home, _ := fixture(t)
	source := filepath.Join(home, "security-v3.db")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMigrationReadsUncheckpointedWALAfterKill$")
	cmd.Env = append(os.Environ(), "RA_MIGRATION_WAL_SOURCE="+source)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	ready := make(chan bool, 1)
	go func() { scan := bufio.NewScanner(stdout); ready <- scan.Scan() && scan.Text() == "wal committed" }()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("WAL child failed")
		}
	case <-ctx.Done():
		t.Fatal("WAL child readiness timed out")
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	info, err := os.Stat(source + "-wal")
	if err != nil || info.Size() <= 32 {
		t.Fatal("source has no uncheckpointed WAL", err)
	}
	// Prove the update is absent from the main file: this standalone copy has
	// no WAL sidecar. Merely observing that a -wal filename exists is not enough.
	mainOnly := filepath.Join(t.TempDir(), "main-only.db")
	if err = copyFile(source, mainOnly); err != nil {
		t.Fatal(err)
	}
	single, err := sql.Open("sqlite", mainOnly)
	if err != nil {
		t.Fatal(err)
	}
	var floor uint64
	if err = single.QueryRow(`SELECT floor_ms FROM security`).Scan(&floor); err != nil || floor == 700 {
		t.Fatalf("test did checkpoint: %d %v", floor, err)
	}
	single.Close()
	if err = migrate(home, true); err != nil {
		t.Fatal(err)
	}
	if !absent(source + ".backup") {
		t.Fatal("dry run published backup")
	}
	if err = migrate(home, false); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		path              string
		version, accepted int
	}{{source + ".backup", 3, 1}, {filepath.Join(home, "security-v4.db"), 4, 0}} {
		db, err := sql.Open("sqlite", item.path)
		if err != nil {
			t.Fatal(err)
		}
		var version, count int
		var cutoff uint64
		if err = db.QueryRow(`SELECT version,floor_ms FROM security`).Scan(&version, &floor); err != nil {
			t.Fatal(err)
		}
		if err = db.QueryRow(`SELECT count(*) FROM accepted`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if err = db.QueryRow(`SELECT cutoff FROM accepted_meta`).Scan(&cutoff); err != nil {
			t.Fatal(err)
		}
		db.Close()
		if version != item.version || count != item.accepted || floor != 700 || cutoff != 600 {
			t.Fatalf("lost WAL state: version=%d count=%d floor=%d cutoff=%d", version, count, floor, cutoff)
		}
	}
}

func TestGenerationMarkerFailureRestoresPublication(t *testing.T) {
	for _, step := range []string{"rename", "sync"} {
		t.Run(step, func(t *testing.T) {
			home, original := fixture(t)
			marker := filepath.Join(home, "state-generation")
			if err := os.WriteFile(marker, []byte("3\n"), 0600); err != nil {
				t.Fatal(err)
			}
			ops := defaultMigrationOps()
			if step == "rename" {
				ops.rename = func(string, string) error { return os.ErrPermission }
			} else {
				calls := 0
				ops.syncDir = func(p string) error {
					calls++
					if calls == 3 {
						return os.ErrPermission
					}
					return syncDir(p)
				}
			}
			if migrateWith(home, false, ops) == nil {
				t.Fatal("failure ignored")
			}
			raw, _ := os.ReadFile(marker)
			if string(raw) != "3\n" {
				t.Fatal("marker not restored")
			}
			if !absent(filepath.Join(home, "security-v4.db")) || !absent(filepath.Join(home, "security-v3.db.backup")) {
				t.Fatal("owned publication leaked")
			}
			raw, _ = os.ReadFile(filepath.Join(home, "security-v3.db"))
			if !bytes.Equal(raw, original) {
				t.Fatal("source changed")
			}
			if err := migrate(home, false); err != nil {
				t.Fatal("cannot retry", err)
			}
		})
	}
}

func TestMarkerRollbackFailureRetainsValidatedV4(t *testing.T) {
	home, _ := fixture(t)
	marker := filepath.Join(home, "state-generation")
	os.WriteFile(marker, []byte("3\n"), 0600)
	ops := defaultMigrationOps()
	syncs, renames := 0, 0
	ops.syncDir = func(p string) error {
		syncs++
		if syncs == 3 {
			return os.ErrPermission
		}
		return syncDir(p)
	}
	ops.rename = func(a, b string) error {
		renames++
		if renames == 2 {
			return os.ErrPermission
		}
		return os.Rename(a, b)
	}
	if migrateWith(home, false, ops) == nil {
		t.Fatal("rollback failure hidden")
	}
	raw, _ := os.ReadFile(marker)
	if string(raw) != "4\n" {
		t.Fatal("unexpected marker")
	}
	ep, err := deviceauth.OpenEndpoint(filepath.Join(home, "security-v4.db"))
	if err != nil {
		t.Fatal("marker has no validated database", err)
	}
	ep.Close()
	if absent(filepath.Join(home, "security-v3.db.backup")) {
		t.Fatal("missing backup for manual recovery")
	}
}
