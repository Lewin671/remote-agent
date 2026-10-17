package claude

import (
	"os"
	"path/filepath"
	"testing"
)

// catalogSample is a copy of the catalog Claude Code 2.1.300 fetched for
// this machine on 2026-10-17, with the account's selected model left out
// (testdata/catalog/example-org-1a2b3c4d5e6f-cc.json). The file name is
// Claude Code's: the organization, a hash of the account and the surface.
const catalogSample = "testdata/catalog/example-org-1a2b3c4d5e6f-cc.json"

// testCatalogHome is a Claude Code home whose cached catalog is the sample
// above. The other files in testdata are an older fetch for another account
// and a file of another surface, which must not be taken.
func testCatalogHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, catalogDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir("testdata/catalog")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join("testdata/catalog", e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, e.Name()), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

// testCatalog is the catalog of testCatalogHome.
func testCatalog(t *testing.T) *Catalog {
	t.Helper()
	cat, err := LoadCatalog(testCatalogHome(t))
	if err != nil {
		t.Fatal(err)
	}
	return cat
}

// testAdapter is an adapter whose Claude Code home holds the sample
// catalog, with that catalog.
func testAdapter(t *testing.T) (*Adapter, *Catalog) {
	t.Helper()
	home := testCatalogHome(t)
	cat, err := LoadCatalog(home)
	if err != nil {
		t.Fatal(err)
	}
	return New("claude", home), cat
}

// menuOf is the /model menu a catalog shows: "Default (recommended)" first,
// then the catalog's models in their order.
func menuOf(cat *Catalog) []string {
	out := []string{"Default (recommended)"}
	for _, m := range cat.Models {
		out = append(out, m.Name)
	}
	return out
}

// The catalog is read as Claude Code serves it: the newest fetch for this
// surface, its rows in the menu's order, each with its ID, name, efforts,
// context window and whether auto comes with it. Haiku 4.5 has no effort
// levels and no auto; a row of another surface or an unreadable file is
// not taken.
func TestLoadCatalog(t *testing.T) {
	cat := testCatalog(t)
	if len(cat.Models) != 12 {
		t.Fatalf("%d models, want 12", len(cat.Models))
	}
	want := []struct {
		id, name      string
		efforts       []string
		defaultEffort string
		window        int64
		noAuto        bool
	}{
		{"claude-opus-5-5", "Opus 5.5", efforts, "medium", 1_000_000, false},
		{"claude-fable-5-1", "Fable 5.1", efforts, "high", 1_000_000, false},
		{"claude-sonnet-5-5", "Sonnet 5.5", efforts, "medium", 1_000_000, false},
		{"claude-haiku-5-5", "Haiku 5.5", efforts, "medium", 1_000_000, false},
		{"claude-haiku-4-5-20251001", "Haiku 4.5", nil, "", 200_000, true},
		{"claude-sonnet-5", "Sonnet 5", efforts, "high", 1_000_000, false},
		{"claude-opus-5", "Opus 5", efforts, "high", 1_000_000, false},
		{"claude-fable-5", "Fable 5", efforts, "high", 1_000_000, false},
		{"claude-opus-4-8", "Opus 4.8", efforts, "high", 1_000_000, false},
		{"claude-opus-4-7", "Opus 4.7", efforts, "xhigh", 1_000_000, false},
		{"claude-opus-4-6", "Opus 4.6", []string{"low", "medium", "high", "max"}, "high", 1_000_000, false},
		{"claude-sonnet-4-6", "Sonnet 4.6", []string{"low", "medium", "high", "max"}, "high", 1_000_000, false},
	}
	for i, w := range want {
		m := cat.Models[i]
		if m.ID != w.id || m.Name != w.name || m.DefaultEffort != w.defaultEffort || m.ContextWindow != w.window || m.AutoUnavailable != w.noAuto {
			t.Errorf("row %d: %+v, want %+v", i, m, w)
		}
		if len(m.Efforts) != len(w.efforts) {
			t.Errorf("%s: efforts %v, want %v", m.ID, m.Efforts, w.efforts)
			continue
		}
		for j := range w.efforts {
			if m.Efforts[j] != w.efforts[j] {
				t.Errorf("%s: efforts %v, want %v", m.ID, m.Efforts, w.efforts)
				break
			}
		}
	}
	if cat.ByID("claude-sonnet-5-5[1m]") != cat.ByID("claude-sonnet-5-5") {
		t.Error("the [1m] suffix names the same model")
	}
	if cat.ByID("sonnet") != nil {
		t.Error("an alias is not a catalog ID")
	}
	if got := cat.ByName("Haiku 4.5"); got != cat.ByID("claude-haiku-4-5-20251001") {
		t.Errorf("ByName(Haiku 4.5) = %+v", got)
	}
	if got := cat.ByName("Opus 5.5 (default)"); got != cat.ByID("claude-opus-5-5") {
		t.Errorf("ByName with the menu's note = %+v", got)
	}
	if got := cat.ContextWindow("claude-haiku-4-5-20251001"); got != 200_000 {
		t.Errorf("window of Haiku 4.5 = %d", got)
	}
	if got := cat.ContextWindow("claude-9-9"); got != 0 {
		t.Errorf("window of an unknown model = %d", got)
	}
}

// A home without a catalog has no list: the IDs are Claude Code's own, and
// an empty catalog is an error the daemon reports rather than a list of
// nothing.
func TestLoadCatalogMissing(t *testing.T) {
	if _, err := LoadCatalog(t.TempDir()); err == nil {
		t.Fatal("a home without a catalog was accepted")
	}
	home := t.TempDir()
	dir := filepath.Join(home, catalogDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "x-cc.json"), []byte(`{"version":2,"catalog":{"surface":"cc","config":{"models":[]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCatalog(home); err == nil {
		t.Fatal("an empty catalog was accepted")
	}
}
