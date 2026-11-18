package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidTranscriptFile(t *testing.T) {
	home := t.TempDir()
	projects := filepath.Join(home, "projects", "work")
	if err := os.MkdirAll(projects, 0o700); err != nil {
		t.Fatal(err)
	}
	validFile := filepath.Join(projects, "s1.jsonl")
	if err := os.WriteFile(validFile, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "outside.jsonl")
	if err := os.WriteFile(outsideFile, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlinkInside := filepath.Join(projects, "link.jsonl")
	if err := os.Symlink(outsideFile, symlinkInside); err != nil {
		t.Fatal(err)
	}
	escapedHome := t.TempDir()
	escapedRoot := filepath.Join(escapedHome, "projects")
	if err := os.Symlink(outsideDir, escapedRoot); err != nil {
		t.Fatal(err)
	}
	escapedFile := filepath.Join(escapedRoot, "outside.jsonl")

	isJSONL := func(rel string) bool { return filepath.Ext(rel) == ".jsonl" }

	cases := []struct {
		name         string
		home         string
		path         string
		allowMissing bool
		want         bool
	}{
		{"valid existing", home, validFile, false, true},
		{"missing rejected when disallowed", home, filepath.Join(projects, "missing.jsonl"), false, false},
		{"missing allowed under owned home", home, filepath.Join(projects, "new-dir", "s2.jsonl"), true, true},
		{"missing outside root rejected", home, filepath.Join(home, "other", "s2.jsonl"), true, false},
		{"outside file rejected", home, outsideFile, true, false},
		{"symlink escaping root rejected", home, symlinkInside, true, false},
		{"root symlink escaping home rejected", escapedHome, escapedFile, true, false},
		{"directory rejected", home, projects, true, false},
		{"non-jsonl rejected", home, filepath.Join(projects, "notes.txt"), true, false},
		{"relative path rejected", home, "projects/work/s1.jsonl", true, false},
		{"empty home rejected", "", validFile, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidTranscriptFile(tc.home, "projects", tc.path, tc.allowMissing, isJSONL); got != tc.want {
				t.Fatalf("ValidTranscriptFile(%q, %q, allowMissing=%v) = %v, want %v", tc.home, tc.path, tc.allowMissing, got, tc.want)
			}
		})
	}
}
