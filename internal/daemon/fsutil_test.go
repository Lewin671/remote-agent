package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateWritesDoNotFollowSymlinks(t *testing.T) {
	for _, write := range []struct {
		name string
		fn   func(string, []byte) error
	}{
		{"durable", writeFileSync}, {"atomic", writeFileAtomic},
		{"environment", func(path string, _ []byte) error {
			return writeEnvFile(path, map[string]string{"API_KEY": "test-secret"})
		}},
	} {
		t.Run(write.name, func(t *testing.T) {
			dir := t.TempDir()
			victim := filepath.Join(dir, "victim")
			path := filepath.Join(dir, "private.json")
			if err := os.WriteFile(victim, []byte("untouched"), 0o644); err != nil {
				t.Fatal(err)
			}
			for _, link := range []string{path, path + ".tmp"} {
				if err := os.Symlink(victim, link); err != nil {
					t.Fatal(err)
				}
			}
			if err := write.fn(path, []byte("private")); err != nil {
				t.Fatal(err)
			}
			b, err := os.ReadFile(victim)
			if err != nil || string(b) != "untouched" {
				t.Fatalf("symlink target modified: %q, %v", b, err)
			}
			fi, err := os.Lstat(path)
			if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 {
				t.Fatalf("private file: %v, %v", fi, err)
			}
			// Overwriting an older permissive file also restores private permissions.
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := write.fn(path, []byte("replacement")); err != nil {
				t.Fatal(err)
			}
			fi, err = os.Stat(path)
			if err != nil || fi.Mode().Perm() != 0o600 {
				t.Fatalf("replacement permissions: %v, %v", fi, err)
			}
		})
	}
}
