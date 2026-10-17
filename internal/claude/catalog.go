package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Claude Code keeps a model catalog of its own: the models /model offers
// with their effort levels, context window and capabilities. It fetches the
// catalog for the logged-in account about once an hour and caches one file
// per account and surface under cache/model-catalog
// (docs/claude-code-notes.md §9). remote-agent reads these files and never
// writes them; the list it hands to clients is this catalog, so a model is
// named by the ID the CLI itself uses rather than by an alias whose meaning
// changes between releases.

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
	Version   int   `json:"version"`
	FetchedAt int64 `json:"fetchedAt"`
	StaleAt   int64 `json:"staleAt"`
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
	AutoPermissionMode string `json:"auto_permission_mode"`
	MinVersion         string `json:"min_claude_code_version"`
}

// parseCatalog reads one cached file: nil for anything that is not this
// CLI's own catalog (another surface, an undecodable file), the rows
// otherwise.
func parseCatalog(b []byte) *Catalog {
	var f catalogFile
	if json.Unmarshal(b, &f) != nil || f.Catalog == nil || f.Catalog.Surface != catalogSurface || f.Catalog.Config == nil {
		return nil
	}
	c := &Catalog{fetchedAt: f.FetchedAt, byID: map[string]*Model{}, byName: map[string]*Model{}}
	for _, r := range f.Catalog.Config.Models {
		if r.ID == "" || r.Disabled {
			continue
		}
		m := &Model{ID: r.ID, Name: r.Name, Description: r.Description}
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
		c.Models = append(c.Models, m)
		c.byID[m.ID] = m
		if m.Name != "" {
			c.byName[m.Name] = m
		}
	}
	return c
}

// ForVersion is the catalog as Claude Code version v lists it in /model: a
// row needing a newer release is left out, so the rows and their positions
// match the menu. An unknown version keeps every row.
func (c *Catalog) ForVersion(v string) *Catalog {
	if c == nil || v == "" {
		return c
	}
	out := &Catalog{fetchedAt: c.fetchedAt, byID: map[string]*Model{}, byName: map[string]*Model{}}
	for _, m := range c.Models {
		if m.MinVersion != "" && versionLess(v, m.MinVersion) {
			continue
		}
		out.Models = append(out.Models, m)
		out.byID[m.ID] = m
		if m.Name != "" {
			out.byName[m.Name] = m
		}
	}
	return out
}

// versionLess reports whether dotted version a is older than b. Parts that
// are not numbers compare as 0.
func versionLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
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
