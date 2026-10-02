package relay

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateWriteIgnoresPredictableTemporarySymlink(t *testing.T) {
	dir := t.TempDir()
	path, victim := filepath.Join(dir, "account.json"), filepath.Join(dir, "victim")
	if err := os.WriteFile(victim, []byte("unchanged"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, path+".tmp"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeFileSync(path, []byte("private-root-key")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(victim)
	if err != nil || string(got) != "unchanged" {
		t.Fatalf("victim changed: %q %v", got, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("private file mode=%o", info.Mode().Perm())
	}
}

func TestServerURLRejectsEmbeddedCredentialsAndFragments(t *testing.T) {
	for _, raw := range []string{"https://user:secret@example.com", "https://example.com#secret", "https://example.com?", "http://evil.example"} {
		if _, err := CheckServerURL(raw); err == nil {
			t.Errorf("accepted %q", raw)
		}
	}
	for _, raw := range []string{"https://example.com", "http://127.0.0.1:8080", "http://[::1]:8080"} {
		if _, err := CheckServerURL(raw); err != nil {
			t.Errorf("rejected %q: %v", raw, err)
		}
	}
}
