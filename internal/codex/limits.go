package codex

import (
	"bytes"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

const (
	// limitsTail is how much of a rollout's end is searched for its last
	// token_count; one turn's records rarely exceed it.
	limitsTail = 1 << 20
	// limitsMaxFiles bounds one scan when many rollouts changed at once.
	limitsMaxFiles = 16
)

// AccountLimits reads the newest account limits that any Codex run with this
// state directory wrote into its rollout: `codex exec` started by another
// agent or a plain terminal session counts as well as sessions remote-agent
// manages, since they all use the same login. Only rollouts modified after
// since are read, and only their tails. seen is the newest modification time
// found, for the caller's next since; limits is nil when none had any.
func (a *Adapter) AccountLimits(since time.Time) (limits *pb.TokenUsage, at, seen time.Time) {
	if a.home == "" {
		return nil, time.Time{}, time.Time{}
	}
	type rollout struct {
		path string
		mod  time.Time
	}
	var changed []rollout
	_ = filepath.WalkDir(filepath.Join(a.home, "sessions"), func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		info, err := d.Info()
		if err == nil && info.ModTime().After(since) {
			changed = append(changed, rollout{path, info.ModTime()})
		}
		return nil
	})
	sort.Slice(changed, func(i, j int) bool { return changed[i].mod.After(changed[j].mod) })
	if len(changed) > 0 {
		seen = changed[0].mod
	}
	for i, r := range changed {
		if i == limitsMaxFiles {
			break
		}
		if u, t := lastLimits(r.path); u != nil && t.After(at) {
			limits, at = u, t
		}
	}
	return limits, at, seen
}

// lastLimits returns the last account limits in a rollout's tail and when
// Codex recorded them.
func lastLimits(path string) (*pb.TokenUsage, time.Time) {
	f, err := os.Open(path)
	if err != nil {
		return nil, time.Time{}
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, time.Time{}
	}
	offset := max(0, info.Size()-limitsTail)
	b, err := io.ReadAll(io.NewSectionReader(f, offset, info.Size()-offset))
	if err != nil {
		return nil, time.Time{}
	}
	lines := bytes.Split(b, []byte{'\n'})
	if offset > 0 {
		lines = lines[1:] // starts mid-line
	}
	for i := len(lines) - 1; i >= 0; i-- {
		raw := lines[i]
		if !bytes.Contains(raw, []byte(`"token_count"`)) {
			continue
		}
		var l line
		var t typed
		if json.Unmarshal(raw, &l) != nil || l.Type != "event_msg" || l.Timestamp.IsZero() ||
			json.Unmarshal(l.Payload, &t) != nil || t.Type != "token_count" {
			continue
		}
		evs, err := tokenCount(l.Payload, nil)
		if err != nil || len(evs) == 0 {
			continue
		}
		if u := evs[0].GetTokenUsage(); u.GetPrimaryLimit() != nil || u.GetSecondaryLimit() != nil {
			return u, l.Timestamp
		}
	}
	return nil, time.Time{}
}
