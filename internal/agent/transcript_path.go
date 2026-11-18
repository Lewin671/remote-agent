package agent

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ValidTranscriptFile reports whether path lies inside home/relRoot, satisfies
// match on the path relative to that root, and is a regular file owned by the
// current user. When allowMissing is true (such as Claude Code reporting its
// future transcript on SessionStart before the first message), an absent leaf
// file is accepted provided its deepest existing ancestor directory lies under
// home/relRoot and belongs to the current user.
func ValidTranscriptFile(home, relRoot, path string, allowMissing bool, match func(rel string) bool) bool {
	if home == "" || relRoot == "" || path == "" || !filepath.IsAbs(path) {
		return false
	}
	homeInfo, err := os.Stat(home)
	if err != nil || !homeInfo.IsDir() || !ownedByCurrentUser(homeInfo) {
		return false
	}
	canonHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		return false
	}
	root := filepath.Join(home, relRoot)
	lstat, lerr := os.Lstat(path)
	if lerr == nil {
		if lstat.Mode()&os.ModeSymlink != 0 && !ownedByCurrentUser(lstat) {
			return false
		}
		canonRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			return false
		}
		if _, ok := withinDir(canonHome, canonRoot); !ok {
			return false
		}
		rootInfo, err := os.Stat(canonRoot)
		if err != nil || !rootInfo.IsDir() || !ownedByCurrentUser(rootInfo) {
			return false
		}
		canonPath, err := filepath.EvalSymlinks(path)
		if err != nil {
			return false
		}
		st, err := os.Stat(canonPath)
		if err != nil || !st.Mode().IsRegular() || !ownedByCurrentUser(st) {
			return false
		}
		rel, ok := withinDir(canonRoot, canonPath)
		if !ok {
			return false
		}
		return match(rel)
	}
	if !errors.Is(lerr, os.ErrNotExist) || !allowMissing {
		return false
	}
	clean := filepath.Clean(path)
	cur := filepath.Dir(clean)
	missing := []string{filepath.Base(clean)}
	for {
		_, err := os.Lstat(cur)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return false
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return false
		}
		missing = append(missing, filepath.Base(cur))
		cur = parent
	}
	canonAnc, err := filepath.EvalSymlinks(cur)
	if err != nil {
		return false
	}
	ancInfo, err := os.Stat(canonAnc)
	if err != nil || !ancInfo.IsDir() || !ownedByCurrentUser(ancInfo) {
		return false
	}
	parts := make([]string, 0, 1+len(missing))
	parts = append(parts, canonAnc)
	for i := len(missing) - 1; i >= 0; i-- {
		parts = append(parts, missing[i])
	}
	resolved := filepath.Join(parts...)
	expectedRoot := filepath.Join(canonHome, relRoot)
	if canonRoot, err := filepath.EvalSymlinks(root); err == nil {
		if _, ok := withinDir(canonHome, canonRoot); !ok {
			return false
		}
		rootInfo, err := os.Stat(canonRoot)
		if err != nil || !rootInfo.IsDir() || !ownedByCurrentUser(rootInfo) {
			return false
		}
		expectedRoot = canonRoot
	} else if !errors.Is(err, os.ErrNotExist) {
		return false
	}
	rel, ok := withinDir(expectedRoot, resolved)
	if !ok {
		return false
	}
	return match(rel)
}

func withinDir(base, target string) (string, bool) {
	rel, err := filepath.Rel(base, target)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}

func ownedByCurrentUser(info os.FileInfo) bool {
	sys, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	return sys.Uid == uint32(os.Getuid())
}
