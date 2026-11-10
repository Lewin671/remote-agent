package claude

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Claude Code keeps a model catalog of its own: the models /model offers
// with their effort levels, context window and capabilities. It fetches the
// catalog for the logged-in account about once an hour and caches one file
// per account and surface under cache/model-catalog
// (docs/claude-code-notes.md §13). remote-agent reads these files and never
// writes them; the list it hands to clients is this catalog, so a model is
// named by the ID the CLI itself uses rather than by an alias whose meaning
// changes between releases.
//
// Claude Code stops fetching when it is told to keep to essential traffic
// (CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC), and the cached file then
// stays as it was, missing every model released since. The daemon
// therefore also carries a trimmed copy of the catalog Claude Code
// publishes (builtin-catalog.json, refreshed by scripts/model_catalog.py
// before each release) and uses whichever of the two is newer. It never
// fetches one itself.

//go:embed builtin-catalog.json
var builtinCatalogJSON []byte

// builtinCatalog is the catalog built into the daemon; tests replace it.
var builtinCatalog = parseBuiltin(builtinCatalogJSON)

// parseBuiltin reads the built-in catalog: its rows have the cached
// catalog's shape, and issued_at stands for the time it was fetched.
func parseBuiltin(b []byte) *Catalog {
	var f struct {
		IssuedAt time.Time    `json:"issued_at"`
		Models   []catalogRow `json:"models"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		panic(fmt.Sprintf("built-in model catalog: %v", err))
	}
	return newCatalog(f.IssuedAt.UnixMilli(), f.Models)
}

// CurrentCatalog is the catalog to go by: the newer of the one Claude Code
// has cached under home and the built-in one. Only a whole catalog is
// taken, never rows from both: a row only the older one has may be a model
// since withdrawn.
func CurrentCatalog(home string) (*Catalog, error) {
	cached, err := LoadCatalog(home)
	b := builtinCatalog
	switch {
	case b == nil || len(b.Models) == 0:
		return cached, err
	case cached == nil || b.fetchedAt > cached.fetchedAt:
		return b, nil
	}
	return cached, nil
}

// catalogDir is where the catalogs are cached under a Claude Code home.
const catalogDir = "cache/model-catalog"

// catalogSurface is the surface this CLI serves itself ("cc"; "ccr" and
// "ccd" are Claude Code's cloud sessions, whose rows it must not take).
const catalogSurface = "cc"

// Model is one model of Claude Code's catalog.
type Model struct {
	// ID is the model's ID: what --model takes, what the transcript
	// records (claude-code-notes §2) and what clients send back.
	ID string
	// Name is the model's row in /model, e.g. "Sonnet 5.5". A session
	// running a model without one (an ID older than the catalog) has no
	// row to be switched to by.
	Name string
	// ShortName is the family label used by Claude Code's short-label menu.
	ShortName string
	// Description is the line /model shows under the name.
	Description string
	// Efforts are the effort levels the model takes, from low to max;
	// empty for a model without any (Haiku 4.5). The /model effort slider
	// steps through them.
	Efforts []string
	// DefaultEffort is the level a session starts at; empty without any.
	DefaultEffort string
	// ContextWindow is the model's largest context window in tokens.
	ContextWindow int64
	// AutoUnavailable: the auto approval mode is not offered with this
	// model (its rows say so up to Claude 4.5).
	AutoUnavailable bool
	// MinVersion is the oldest Claude Code release that lists the model;
	// empty when the row does not say.
	MinVersion string
	// OfferedOn are the providers the model is served by ("first_party",
	// "bedrock", "vertex", ...); empty when the row does not say.
	OfferedOn []string
}

// Catalog is the model list Claude Code has cached, in the order /model
// shows it.
type Catalog struct {
	Models    []*Model
	fetchedAt int64

	byID   map[string]*Model
	byName map[string]*Model
}

// ByID returns the model with this ID, or nil. A "[1m]" suffix, which names
// the 1M window on the command line, is not part of the ID.
func (c *Catalog) ByID(id string) *Model {
	if c == nil {
		return nil
	}
	id = strings.TrimSuffix(id, "[1m]")
	return c.byID[id]
}

// ByName returns the model whose /model row is this name, or nil. Notes
// the menu adds to a name ("Opus 5.5 (default)", "Opus 5.5 (1M context)")
// are not part of it.
func (c *Catalog) ByName(name string) *Model {
	if c == nil {
		return nil
	}
	for {
		if m := c.byName[name]; m != nil {
			return m
		}
		i := strings.LastIndex(name, " (")
		if i <= 0 {
			return nil
		}
		name = strings.TrimSpace(name[:i])
	}
}

// ContextWindow is the context window of a model ID, or 0 when the catalog
// does not know it. Claude Code does not write the window to the
// transcript (claude-code-notes §2), so it comes from the catalog.
func (c *Catalog) ContextWindow(id string) int64 {
	if m := c.ByID(id); m != nil {
		return m.ContextWindow
	}
	return 0
}

// LoadCatalog reads the most recently fetched catalog Claude Code has
// cached for this CLI's surface under home. Each account has its own file;
// the newest is taken, which is the logged-in account's unless another one
// fetched later. A catalog for another surface is ignored, a file that
// cannot be read is passed over, and an error is returned only when no
// usable catalog is there at all.
func LoadCatalog(home string) (*Catalog, error) {
	dir := filepath.Join(home, catalogDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read Claude Code's model catalog: %w", err)
	}
	var best *Catalog
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		c := parseCatalog(b)
		if c == nil {
			continue
		}
		if best == nil || c.fetchedAt > best.fetchedAt {
			best = c
		}
	}
	if best == nil || len(best.Models) == 0 {
		return nil, errors.New("Claude Code has no model catalog cached; start it once to fetch one")
	}
	return best, nil
}

// catalogFile is the file Claude Code caches: the document it fetched, when
// it fetched it, and when it turns stale. The document is null for a
// surface the account is not served.
type catalogFile struct {
	FetchedAt int64 `json:"fetchedAt"`
	Catalog   *struct {
		Surface string         `json:"surface"`
		Config  *catalogConfig `json:"config"`
	} `json:"catalog"`
}

type catalogConfig struct {
	Models []catalogRow `json:"models"`
}

// catalogRow is one row of the document. Only the fields remote-agent uses
// are read; the rest (capabilities, badges, notices, image limits) is
// Claude Code's own.
type catalogRow struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ShortName   string `json:"short_name"`
	Description string `json:"description"`
	Disabled    bool   `json:"disabled"`
	Thinking    *struct {
		EffortOptions []struct {
			ID string `json:"id"`
		} `json:"effort_options"`
	} `json:"thinking"`
	Runtime *struct {
		EffortLevels   []string `json:"effort_levels"`
		DefaultEffort  string   `json:"default_effort"`
		MaxInputTokens int64    `json:"max_input_tokens"`
	} `json:"runtime"`
	AutoPermissionMode string   `json:"auto_permission_mode"`
	MinVersion         string   `json:"min_claude_code_version"`
	OfferedOn          []string `json:"offered_on"`
}

// parseCatalog reads one cached file: nil for anything that is not this
// CLI's own catalog (another surface, an undecodable file), the rows
// otherwise.
func parseCatalog(b []byte) *Catalog {
	var f catalogFile
	if json.Unmarshal(b, &f) != nil || f.Catalog == nil || f.Catalog.Surface != catalogSurface || f.Catalog.Config == nil {
		return nil
	}
	return newCatalog(f.FetchedAt, f.Catalog.Config.Models)
}

// newCatalog is the catalog of these rows, fetched at this time (Unix
// milliseconds).
func newCatalog(fetchedAt int64, rows []catalogRow) *Catalog {
	c := &Catalog{fetchedAt: fetchedAt, byID: map[string]*Model{}, byName: map[string]*Model{}}
	for _, r := range rows {
		if r.ID == "" || r.Disabled {
			continue
		}
		m := &Model{ID: r.ID, Name: r.Name, ShortName: r.ShortName, Description: r.Description}
		if r.Runtime != nil {
			m.Efforts = append([]string{}, r.Runtime.EffortLevels...)
			m.DefaultEffort = r.Runtime.DefaultEffort
			m.ContextWindow = r.Runtime.MaxInputTokens
		}
		if len(m.Efforts) == 0 && r.Thinking != nil {
			// A row may give the slider's levels without the API's.
			for _, o := range r.Thinking.EffortOptions {
				m.Efforts = append(m.Efforts, o.ID)
			}
			m.DefaultEffort = ""
		}
		m.AutoUnavailable = r.AutoPermissionMode == "unavailable"
		m.MinVersion = r.MinVersion
		m.OfferedOn = r.OfferedOn
		c.add(m)
	}
	return c
}

func (c *Catalog) add(m *Model) {
	c.Models = append(c.Models, m)
	c.byID[m.ID] = m
	if m.Name != "" {
		c.byName[m.Name] = m
	}
}

// filter is the catalog with only the models keep accepts.
func (c *Catalog) filter(keep func(*Model) bool) *Catalog {
	out := &Catalog{fetchedAt: c.fetchedAt, byID: map[string]*Model{}, byName: map[string]*Model{}}
	for _, m := range c.Models {
		if keep(m) {
			out.add(m)
		}
	}
	return out
}

// ForVersion is the catalog as Claude Code version v lists it in /model: a
// row needing a newer release is left out, so the rows and their positions
// match the menu. An unknown version keeps every row.
func (c *Catalog) ForVersion(v string) *Catalog {
	if c == nil || v == "" {
		return c
	}
	return c.filter(func(m *Model) bool { return m.MinVersion == "" || !versionLess(v, m.MinVersion) })
}

// ForProvider is the catalog without the models provider p does not
// serve. A row that does not name its providers is kept.
func (c *Catalog) ForProvider(p string) *Catalog {
	if c == nil || p == "" {
		return c
	}
	return c.filter(func(m *Model) bool { return len(m.OfferedOn) == 0 || slices.Contains(m.OfferedOn, p) })
}

// providerEnv are the variables that point Claude Code at a provider other
// than Anthropic's own API, with the catalog's names for them.
var providerEnv = []struct{ env, provider string }{
	{"CLAUDE_CODE_USE_BEDROCK", "bedrock"},
	{"CLAUDE_CODE_USE_VERTEX", "vertex"},
	{"CLAUDE_CODE_USE_FOUNDRY", "foundry"},
	{"CLAUDE_CODE_USE_ANTHROPIC_AWS", "anthropic_aws"},
	{"CLAUDE_CODE_USE_MANTLE", "mantle"},
	{"CLAUDE_CODE_USE_GATEWAY", "gateway"},
}

// provider is the catalog's name for the provider Claude Code uses, by
// the daemon's environment: Anthropic's own API unless one of providerEnv
// is set.
func provider(getenv func(string) string) string {
	for _, p := range providerEnv {
		if v := getenv(p.env); v != "" && v != "0" && !strings.EqualFold(v, "false") {
			return p.provider
		}
	}
	return "first_party"
}

// versionLess reports whether dotted version a is older than b. A suffix
// after the numbers ("2.1.300-beta") is ignored.
func versionLess(a, b string) bool {
	num := func(v string) string {
		if i := strings.IndexFunc(v, func(r rune) bool { return r != '.' && (r < '0' || r > '9') }); i >= 0 {
			v = v[:i]
		}
		return v
	}
	pa, pb := strings.Split(num(a), "."), strings.Split(num(b), ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x < y
		}
	}
	return false
}
