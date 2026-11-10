package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
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
	withBuiltin(t, nil)
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

// withBuiltin replaces the built-in catalog for the test. The tests on the
// sample catalog run without one: the built-in catalog is newer than the
// sample, and would be taken in its place.
func withBuiltin(t *testing.T, c *Catalog) {
	t.Helper()
	old := builtinCatalog
	builtinCatalog = c
	t.Cleanup(func() { builtinCatalog = old })
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

// A row needing a newer Claude Code than the installed one is not in
// /model, so it is neither offered nor counted when finding a row.
func TestCatalogForVersion(t *testing.T) {
	cat := testCatalog(t)
	var need *Model
	for _, m := range cat.Models {
		if m.MinVersion != "" {
			need = m
			break
		}
	}
	if need == nil {
		t.Fatal("the sample has no row with a minimum version")
	}
	if got := cat.ForVersion(need.MinVersion).ByID(need.ID); got == nil {
		t.Fatalf("%s missing at its own minimum %s", need.ID, need.MinVersion)
	}
	older := cat.ForVersion("2.0.0")
	if older.ByID(need.ID) != nil || older.ByName(need.Name) != nil {
		t.Fatalf("%s (needs %s) listed for 2.0.0", need.ID, need.MinVersion)
	}
	if len(older.Models) >= len(cat.Models) {
		t.Fatalf("2.0.0 keeps %d of %d rows", len(older.Models), len(cat.Models))
	}
	if got := cat.ForVersion(""); len(got.Models) != len(cat.Models) {
		t.Fatalf("unknown version keeps %d of %d rows", len(got.Models), len(cat.Models))
	}
	for _, c := range []struct {
		a, b string
		less bool
	}{{"2.1.284", "2.1.300", true}, {"2.1.300", "2.1.280", false}, {"2.1.300", "2.1.300", false}, {"2.1", "2.1.1", true}, {"2.1.300-beta", "2.1.280", false}} {
		if got := versionLess(c.a, c.b); got != c.less {
			t.Errorf("versionLess(%s, %s) = %v", c.a, c.b, got)
		}
	}
}

// A session made before Claude Code had fetched a catalog reads it again
// once a /model name or a model ID turns up that it does not know.
func TestParserReadsCatalogLater(t *testing.T) {
	p := NewParser()
	var cat *Catalog
	p.loadCatalog = func() *Catalog { return cat }
	if w := p.contextWindow("claude-sonnet-5-5"); w != 0 {
		t.Fatalf("window without a catalog: %d", w)
	}
	cat = testCatalog(t)
	p.reloadedAt = time.Time{} // a minute has passed
	if m := p.modelNamed("Sonnet 5.5"); m == nil || m.ID != "claude-sonnet-5-5" {
		t.Fatalf("Sonnet 5.5 after the catalog appeared: %v", m)
	}
	if w := p.contextWindow("claude-sonnet-5-5"); w == 0 {
		t.Fatal("no window after the catalog appeared")
	}
}

func TestCatalogIgnoresUnusedExternalMetadata(t *testing.T) {
	data, err := os.ReadFile(catalogSample)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	delete(doc, "version")
	delete(doc, "staleAt")
	plain, _ := json.Marshal(doc)
	want := parseCatalog(plain)
	if want == nil {
		t.Fatal("fixture catalog is missing")
	}
	for _, metadata := range []string{`42`, `"external metadata"`, `null`, `{"new":"shape"}`} {
		doc["version"], doc["staleAt"] = json.RawMessage(metadata), json.RawMessage(metadata)
		data, _ := json.Marshal(doc)
		if got := parseCatalog(data); !reflect.DeepEqual(got, want) {
			t.Fatalf("unused metadata %s changed catalog", metadata)
		}
	}
}

// The built-in catalog reads: every row has an ID and a name, no ID twice,
// and it carries the time it was issued, which decides against the cache.
func TestBuiltinCatalog(t *testing.T) {
	c := parseBuiltin(builtinCatalogJSON)
	if len(c.Models) == 0 || c.fetchedAt <= 0 {
		t.Fatalf("built-in catalog: %d models, issued %d", len(c.Models), c.fetchedAt)
	}
	seen := map[string]bool{}
	for _, m := range c.Models {
		if m.ID == "" || m.Name == "" || seen[m.ID] {
			t.Errorf("row %+v: no ID, no name or a repeated ID", m)
		}
		seen[m.ID] = true
	}
}

// The newer of the cached and the built-in catalog is taken, whole: the
// cache Claude Code stopped refreshing gives way to a newer built-in one,
// a cache fetched since wins over it, and without a cache the built-in
// one is there from the start.
func TestCurrentCatalogTakesTheNewer(t *testing.T) {
	home := testCatalogHome(t)
	cached, err := LoadCatalog(home)
	if err != nil {
		t.Fatal(err)
	}
	newer := newCatalog(cached.fetchedAt+1, []catalogRow{{ID: "claude-new-1", Name: "New 1"}})
	older := newCatalog(cached.fetchedAt-1, []catalogRow{{ID: "claude-old-1", Name: "Old 1"}})

	withBuiltin(t, newer)
	if c, err := CurrentCatalog(home); err != nil || c != newer {
		t.Fatalf("newer built-in catalog not taken: %v", err)
	}
	if c, err := CurrentCatalog(t.TempDir()); err != nil || c != newer {
		t.Fatalf("built-in catalog not taken without a cache: %v", err)
	}
	withBuiltin(t, older)
	c, err := CurrentCatalog(home)
	if err != nil || c.ByID("claude-old-1") != nil || len(c.Models) != len(cached.Models) {
		t.Fatalf("newer cache not taken whole: %v", err)
	}
	withBuiltin(t, nil)
	if _, err := CurrentCatalog(t.TempDir()); err == nil {
		t.Fatal("a catalog from nowhere")
	}
}

// A model not served by the provider in use is left out; a row that does
// not name its providers stays. The provider comes from the variables
// Claude Code reads, Anthropic's own API without any.
func TestCatalogForProvider(t *testing.T) {
	c := newCatalog(1, []catalogRow{
		{ID: "a", Name: "A", OfferedOn: []string{"first_party", "bedrock"}},
		{ID: "b", Name: "B", OfferedOn: []string{"bedrock"}},
		{ID: "c", Name: "C"},
	})
	ids := func(c *Catalog) (out []string) {
		for _, m := range c.Models {
			out = append(out, m.ID)
		}
		return out
	}
	if got := ids(c.ForProvider("first_party")); !reflect.DeepEqual(got, []string{"a", "c"}) {
		t.Errorf("first_party: %v", got)
	}
	if got := ids(c.ForProvider("bedrock")); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("bedrock: %v", got)
	}
	if c.ForProvider("first_party").ByID("b") != nil {
		t.Error("left-out model still found by ID")
	}
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	for want, e := range map[string]map[string]string{
		"first_party": {},
		"bedrock":     {"CLAUDE_CODE_USE_BEDROCK": "1"},
		"vertex":      {"CLAUDE_CODE_USE_BEDROCK": "0", "CLAUDE_CODE_USE_VERTEX": "true"},
	} {
		if got := provider(env(e)); got != want {
			t.Errorf("provider(%v) = %s, want %s", e, got, want)
		}
	}
}

// With the built-in catalog, a CLI release lists the models it offers on
// Anthropic's API: Fable 5.2 from 2.1.290 on and not before, and never
// Opus 4.1, which only Bedrock serves.
func TestBuiltinCatalogForTheInstalledCLI(t *testing.T) {
	c := parseBuiltin(builtinCatalogJSON).ForProvider("first_party")
	if c.ByID("claude-opus-4-1-20250805") != nil {
		t.Error("Opus 4.1 offered on Anthropic's API")
	}
	m := c.ByID("claude-fable-5-2")
	if m == nil || m.MinVersion == "" {
		t.Skip("the built-in catalog has no Fable 5.2 with a minimum version")
	}
	if c.ForVersion(m.MinVersion).ByID(m.ID) == nil {
		t.Errorf("%s missing at %s", m.ID, m.MinVersion)
	}
	if c.ForVersion("2.1.1").ByID(m.ID) != nil {
		t.Errorf("%s offered to 2.1.1", m.ID)
	}
}
