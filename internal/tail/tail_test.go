package tail

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.jsonl")
	r := NewReader(path, 0)
	if lines, _, err := r.Next(); err != nil || len(lines) != 0 {
		t.Fatalf("missing file: %v %v", lines, err)
	}
	f, _ := os.Create(path)
	defer f.Close()
	f.WriteString("one\ntw")
	lines, _, _ := r.Next()
	if len(lines) != 1 || string(lines[0].Data) != "one" || lines[0].End != 4 {
		t.Fatalf("first read: %+v", lines)
	}
	if r.Offset() != 4 {
		t.Fatalf("offset %d", r.Offset())
	}
	f.WriteString("o\n\nthree\n")
	lines, _, _ = r.Next()
	if len(lines) != 2 || string(lines[0].Data) != "two" || string(lines[1].Data) != "three" || lines[1].End != 15 {
		t.Fatalf("second read: %+v", lines)
	}

	// Resume from a saved offset.
	r2 := NewReader(path, 4)
	lines, _, _ = r2.Next()
	if len(lines) != 2 || string(lines[0].Data) != "two" {
		t.Fatalf("resume: %+v", lines)
	}

	// Replacing the file with a shorter one restarts from the top.
	os.WriteFile(path, []byte("new\n"), 0o600)
	lines, truncated, _ := r.Next()
	if !truncated || len(lines) != 1 || string(lines[0].Data) != "new" {
		t.Fatalf("after truncate: %+v %v", lines, truncated)
	}
}
