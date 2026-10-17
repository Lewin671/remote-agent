package tail

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReaderSignalsSamePathReplacement(t *testing.T) {
	for _, body := range []string{"new\n", "replacement\n"} {
		t.Run(body, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "rollout")
			if err := os.WriteFile(path, []byte("old\n"), 0600); err != nil {
				t.Fatal(err)
			}
			r := NewReader(path, 0)
			if _, reset, err := r.Next(); err != nil || reset {
				t.Fatalf("first read: reset=%v error=%v", reset, err)
			}
			if err := os.WriteFile(path+".new", []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(path+".new", path); err != nil {
				t.Fatal(err)
			}
			lines, reset, err := r.Next()
			if err != nil || !reset || len(lines) != 1 || string(lines[0].Data)+"\n" != body || lines[0].Start != 0 {
				t.Fatalf("replacement not read from start: %+v reset=%v error=%v", lines, reset, err)
			}
			if lines, reset, err := r.Next(); err != nil || reset || len(lines) != 0 {
				t.Fatalf("unchanged source reset again: %+v reset=%v error=%v", lines, reset, err)
			}
		})
	}
}
