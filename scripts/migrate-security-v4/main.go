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

func migrate(home string, dry bool) error {
	home, err := filepath.Abs(home)
	if err != nil {
		return err
	}
	// Hold exactly the same exclusive lock as daemon.Run through publication.
	unlock, err := lockDaemon(filepath.Join(home, "daemon.lock"))
	if err != nil {
		return err
	}
	defer unlock()
	source := filepath.Join(home, "security-v3.db")
	target := filepath.Join(home, "security-v4.db")
	backup := source + ".backup"
	if !absent(target) || !absent(backup) {
		return errMigration
	}
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() {
		return errMigration
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
	var deadline, cutoff uint64
	if db.QueryRow(`SELECT version FROM security WHERE id=1`).Scan(&version) != nil || version != 3 {
		return errMigration
	}
	if db.QueryRow(`SELECT COALESCE(MAX(deadline),0) FROM accepted`).Scan(&deadline) != nil || deadline > uint64(time.Now().UnixMilli()) {
		return errMigration
	}
	if db.QueryRow(`SELECT cutoff FROM accepted_meta WHERE id=1`).Scan(&cutoff) != nil {
		return errMigration
	}
	cutoff = max(cutoff, deadline, uint64(time.Now().UnixMilli()))
	// No ID without a hash is copied. The retained pruning floor prevents
	// expired missing requests resurrecting after a clock rollback.
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
	// VACUUM INTO is a consistent SQLite backup, including any checkpointed WAL.
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
	// Hard links publish without overwriting a concurrently created file.
	if err = os.Link(backupTemp, backup); err != nil {
		return err
	}
	if err = syncDir(home); err != nil {
		return err
	}
	if err = os.Link(candidate, target); err != nil {
		return err
	}
	if err = syncDir(home); err != nil {
		os.Remove(target)
		return err
	}
	return nil
}
func copyFile(source, target string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func main() {
	flags := flag.NewFlagSet("migrate-security-v4", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	home := flags.String("home", "", "")
	dry := flags.Bool("dry-run", false, "")
	locale := flags.String("locale", "en", "")
	if flags.Parse(os.Args[1:]) != nil || *home == "" || flags.NArg() != 0 {
		fmt.Fprintln(os.Stderr, i18n.SecurityMigrationUsage(*locale))
		os.Exit(2)
	}
	fmt.Println(i18n.SecurityMigrationOldResults(*locale))
	if err := migrate(*home, *dry); err != nil {
		fmt.Fprintln(os.Stderr, i18n.SecurityMigrationFailed(*locale))
		os.Exit(1)
	}
	if *dry {
		fmt.Println(i18n.SecurityMigrationDryRun(*locale))
	} else {
		fmt.Println(i18n.SecurityMigrationComplete(*locale))
	}
}
