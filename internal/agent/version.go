package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

// BinVersion remembers a CLI's version until its executable changes, so
// that a CLI updated while the daemon runs is noticed without running the
// CLI on every question.
type BinVersion struct {
	mu      sync.Mutex
	read    bool
	id      binID
	version string
}

// binID identifies an installed executable: where its name leads, the
// link itself, and the file it ends at. An update replaces at least one.
type binID struct {
	path         string
	link, target os.FileInfo
}

func identify(bin string) binID {
	var id binID
	p, err := exec.LookPath(bin)
	if err != nil {
		return id
	}
	id.link, _ = os.Lstat(p)
	if id.path, err = filepath.EvalSymlinks(p); err == nil {
		id.target, _ = os.Stat(id.path)
	}
	return id
}

func sameInfo(a, b os.FileInfo) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

func (id binID) equal(o binID) bool {
	return id.path == o.path && sameInfo(id.link, o.link) && sameInfo(id.target, o.target)
}

// Get is the version of the executable bin: what read returned the last
// time, unless bin has changed since, in which case read runs again.
func (c *BinVersion) Get(bin string, read func() string) string {
	if bin == "" {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if id := identify(bin); !c.read || !id.equal(c.id) {
		c.version, c.id, c.read = read(), id, true
	}
	return c.version
}
