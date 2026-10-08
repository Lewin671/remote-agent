package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBinVersionRereadsWhenTheExecutableChanges(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(name), 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	bin := filepath.Join(dir, "cli")
	link := func(target string) {
		os.Remove(bin)
		if err := os.Symlink(target, bin); err != nil {
			t.Fatal(err)
		}
	}
	reads, installed := 0, "1.0"
	var c BinVersion
	get := func() string {
		return c.Get(bin, func() string { reads++; return installed })
	}

	link(write("1.0"))
	if get() != "1.0" || get() != "1.0" || reads != 1 {
		t.Fatalf("unchanged executable: reads = %d, want 1", reads)
	}

	// An update points the link at a new file, as Claude Code's does.
	installed = "2.0"
	link(write("2.0"))
	if v := get(); v != "2.0" || reads != 2 {
		t.Fatalf("after relinking: version %q, reads %d", v, reads)
	}

	// Or replaces the file the link ends at, as npm and bun do.
	installed = "3.0"
	os.Remove(filepath.Join(dir, "2.0"))
	write("2.0")
	if v := get(); v != "3.0" || get() != "3.0" || reads != 3 {
		t.Fatalf("after replacing the target: version %q, reads %d", v, reads)
	}

	if (&BinVersion{}).Get("", func() string { t.Fatal("read without an executable"); return "" }) != "" {
		t.Fatal("no executable should have no version")
	}
}
