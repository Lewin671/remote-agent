// One-time, offline operator tool. Never imported or invoked by the daemon.
package main

import (
	"bytes"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/i18n"
	_ "modernc.org/sqlite"
)

const summarySchema = `
DROP TABLE accepted;
CREATE TABLE accepted (
 key BLOB PRIMARY KEY CHECK(length(key)=32), payload_hash BLOB NOT NULL CHECK(length(payload_hash)=32),
 deadline INTEGER NOT NULL, retain_until INTEGER NOT NULL, state INTEGER NOT NULL, category INTEGER NOT NULL,
 body_hash BLOB NOT NULL CHECK(length(body_hash)=32), available INTEGER NOT NULL, checksum BLOB NOT NULL CHECK(length(checksum)=32)
);
CREATE TABLE accepted_bodies (key BLOB PRIMARY KEY, result BLOB NOT NULL);
UPDATE accepted_meta SET cutoff=0 WHERE id=1;
UPDATE security SET version=4 WHERE id=1;
`

var errMigration = errors.New("migration validation failed")
var identityTables = []string{"security", "states", "recovery", "content_keys", "lease"}

func snapshot(db *sql.DB, table string) ([][]any, error) {
	rows, err := db.Query(`SELECT * FROM ` + table + ` ORDER BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var result [][]any
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		for i, v := range values {
			if b, ok := v.([]byte); ok {
				values[i] = bytes.Clone(b)
			}
		}
		result = append(result, values)
	}
	return result, rows.Err()
}
func integrity(db *sql.DB) error {
	var result string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&result); err != nil || result != "ok" {
		return errMigration
	}
	return nil
}
func absent(path string) bool { _, err := os.Lstat(path); return os.IsNotExist(err) }
func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

type migrationOps struct {
	now     func() time.Time
	publish func(string, string) error
	syncDir func(string) error
	rename  func(string, string) error
}

func defaultMigrationOps() migrationOps {
	return migrationOps{time.Now, func(src, dst string) error { return publishFile(src, dst, os.Link) }, syncDir, os.Rename}
}
func migrate(home string, dry bool) error { return migrateWith(home, dry, defaultMigrationOps()) }
func migrateWith(home string, dry bool, ops migrationOps) (err error) {
	home, err = filepath.Abs(home)
	if err != nil {
		return err
	}
	source := filepath.Join(home, "security-v3.db")
	target := filepath.Join(home, "security-v4.db")
	backup := source + ".backup"
	// Reject typos before any lock/database/temp-file creation. Existing daemon
	// state has both a regular v3 store and its pre-existing lock inode.
	info, err := os.Stat(home)
	if err != nil || !info.IsDir() {
		return errMigration
	}
	for _, path := range []string{source, filepath.Join(home, "daemon.lock")} {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return errMigration
		}
	}
	if !absent(target) || !absent(backup) {
		return errMigration
	}
	// Hold exactly the same existing inode as daemon.Run through publication.
	unlock, err := lockDaemon(filepath.Join(home, "daemon.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	marker := filepath.Join(home, "state-generation")
	markerInfo, markerStatErr := os.Lstat(marker)
	if markerStatErr != nil && !os.IsNotExist(markerStatErr) {
		return markerStatErr
	}
	if markerStatErr == nil && !markerInfo.Mode().IsRegular() {
		return errMigration
	}
	originalMarker, markerErr := os.ReadFile(marker)
	if markerErr != nil && !os.IsNotExist(markerErr) {
		return markerErr
	}
	if markerErr == nil {
		info, err := os.Lstat(marker)
		if err != nil || !info.Mode().IsRegular() || strings.TrimSpace(string(originalMarker)) != "3" {
			return errMigration
		}
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: filepath.ToSlash(source)}).String()+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err = integrity(db); err != nil {
		return err
	}
	var version int
	if db.QueryRow(`SELECT version FROM security WHERE id=1`).Scan(&version) != nil || version != 3 {
		return errMigration
	}
	cutoff, err := migrationCutoff(db, ops.now())
	if err != nil {
		return err
	}
	// No old ID is copied. Preserve only replay barriers already required by
	// the source, rather than permanently pinning the operator's wall clock.
	before := map[string][][]any{}
	for _, table := range identityTables {
		before[table], err = snapshot(db, table)
		if err != nil {
			return err
		}
	}
	work, err := os.MkdirTemp(home, ".security-v4-upgrade-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	candidate := filepath.Join(work, "security.db")
	// VACUUM INTO includes committed WAL frames even before checkpoint.
	// Bind the path; never interpolate an operator path into SQL.
	if _, err = db.Exec(`VACUUM INTO ?`, candidate); err != nil {
		return err
	}
	if err = os.Chmod(candidate, 0600); err != nil {
		return err
	}
	// Keep an exact source backup before changing the private candidate.
	backupTemp := filepath.Join(work, "backup.db")
	if err = copyFile(candidate, backupTemp); err != nil {
		return err
	}
	next, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: filepath.ToSlash(candidate)}).String())
	if err != nil {
		return err
	}
	next.SetMaxOpenConns(1)
	defer next.Close()
	tx, err := next.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(summarySchema); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE accepted_meta SET cutoff=? WHERE id=1`, cutoff); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	if err = integrity(next); err != nil {
		return err
	}
	for _, table := range identityTables {
		actual, err := snapshot(next, table)
		if err != nil {
			return err
		}
		expected := before[table]
		if table == "security" {
			if len(expected) != 1 || len(expected[0]) != 12 {
				return errMigration
			}
			expected[0][1] = int64(4)

		}
		if !reflect.DeepEqual(expected, actual) {
			return errMigration
		}
	}
	if err = next.Close(); err != nil {
		return err
	}
	endpoint, err := deviceauth.OpenEndpoint(candidate)
	if err != nil {
		return err
	}
	if err = endpoint.Close(); err != nil {
		return err
	}
	// OpenEndpoint verifies the signed chain, keys, membership and lease, then
	// closes SQLite so all pages are in the single candidate file.
	if dry {
		return nil
	}
	f, err := os.Open(candidate)
	if err != nil {
		return err
	}
	err = f.Sync()
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	// Roll back only files published by this invocation. A pre-existing file
	// (including one that races publication) is never overwritten or removed.
	markerTemp := filepath.Join(work, "state-generation")
	if err = writeSynced(markerTemp, []byte("4\n")); err != nil {
		return err
	}
	markerBefore := filepath.Join(work, "state-generation-before")
	if markerErr == nil {
		if err = writeSynced(markerBefore, originalMarker); err != nil {
			return err
		}
	}
	backupPublished, targetPublished, markerPublished := false, false, false
	defer func() {
		if err == nil {
			return
		}
		if markerPublished {
			var restoreErr error
			if markerErr == nil {
				restoreErr = ops.rename(markerBefore, marker)
			} else {
				restoreErr = os.Remove(marker)
			}
			if restoreErr != nil {
				// Keep the validated v4 store if its generation marker cannot be restored.
				err = errors.Join(err, restoreErr, ops.syncDir(home))
				return
			}
		}
		if targetPublished {
			err = errors.Join(err, os.Remove(target))
		}
		if backupPublished {
			err = errors.Join(err, os.Remove(backup))
		}
		if backupPublished || targetPublished {
			err = errors.Join(err, ops.syncDir(home))
		}
	}()
	if err = ops.publish(backupTemp, backup); err != nil {
		return err
	}
	backupPublished = true
	if err = ops.syncDir(home); err != nil {
		return err
	}
	if err = ops.publish(candidate, target); err != nil {
		return err
	}
	targetPublished = true
	if err = ops.syncDir(home); err != nil {
		return err
	}
	if err = ops.rename(markerTemp, marker); err != nil {
		return err
	}
	markerPublished = true
	return ops.syncDir(home)
}

func writeSynced(path string, data []byte) (err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	if _, err = f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}

func migrationCutoff(db *sql.DB, now time.Time) (uint64, error) {
	if now.UnixMilli() <= 0 {
		return 0, errMigration
	}
	clock := uint64(now.UnixMilli())
	deadline, err := validatedV3Deadline(db)
	if err != nil {
		return 0, err
	}
	var cutoff, floor, saved uint64
	if deadline > clock {
		return 0, errMigration
	}
	if db.QueryRow(`SELECT cutoff FROM accepted_meta WHERE id=1`).Scan(&cutoff) != nil {
		return 0, errMigration
	}
	if db.QueryRow(`SELECT floor_ms,floor_saved_ms FROM security WHERE id=1`).Scan(&floor, &saved) != nil {
		return 0, errMigration
	}
	if max(cutoff, floor, saved) > clock+deviceauth.MaxClockSkew {
		return 0, errMigration
	}
	return max(cutoff, deadline), nil
}

// Hard links are preferred, but FAT/network filesystems may not support them.
// Exclusive copying preserves no-overwrite semantics while the daemon lock is
// held. Both paths publish only our private, verified and synced candidate.
func publishFile(source, target string, link func(string, string) error) error {
	err := link(source, target)
	if err == nil || os.IsExist(err) {
		return err
	}
	return copyFile(source, target)
}
func copyFile(source, target string) (err error) {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() {
		err = errors.Join(err, out.Close())
		if err != nil {
			err = errors.Join(err, os.Remove(target))
		}
	}()
	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("migrate-security-v4", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	home := flags.String("home", "", "")
	dry := flags.Bool("dry-run", false, "")
	locale := flags.String("locale", "en", "")
	if flags.Parse(args) != nil || *home == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, i18n.SecurityMigrationUsage(*locale))
		return 2
	}
	fmt.Fprintln(stdout, i18n.SecurityMigrationOldResults(*locale))
	if err := migrate(*home, *dry); err != nil {
		if errors.Is(err, errV3Acceptance) {
			fmt.Fprintln(stderr, i18n.SecurityMigrationAcceptanceCorrupt(*locale))
		} else {
			fmt.Fprintln(stderr, i18n.SecurityMigrationFailed(*locale))
		}
		return 1
	}
	if *dry {
		fmt.Fprintln(stdout, i18n.SecurityMigrationDryRun(*locale))
	} else {
		fmt.Fprintln(stdout, i18n.SecurityMigrationComplete(*locale))
	}
	return 0
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }
