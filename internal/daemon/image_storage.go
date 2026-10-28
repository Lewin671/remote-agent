package daemon

import (
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/imageproc"
	"github.com/Lewin671/remote-agent/internal/paths"
)

const defaultImageCacheQuota = int64(150 << 20)

func (u *uploads) protectedSessions() map[string]bool {
	if u.pins != nil {
		return u.pins()
	}
	return nil
}

func (d *Daemon) protectedImageSessions() map[string]bool {
	d.mu.Lock()
	ss := make([]*session, 0, len(d.sessions))
	for _, s := range d.sessions {
		ss = append(ss, s)
	}
	d.mu.Unlock()
	pins := map[string]bool{}
	for _, s := range ss {
		s.mu.Lock()
		if s.running || s.meta.Starting || s.driving {
			pins[s.meta.ID] = true
		}
		s.mu.Unlock()
	}
	return pins
}

func (u *uploads) expose(imgs []queuedImage) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, im := range imgs {
		t := u.byImage(im.ID)
		if t == nil {
			// No record to mark; the CLI's own image check still applies.
			continue
		}
		t.Exposed = true
		t.AccessMs = time.Now().UnixMilli()
		if err := u.save(t); err != nil {
			return err
		}
	}
	return nil
}

func (u *uploads) embedded(id string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if t := u.byImage(id); t != nil {
		t.Embedded = true
		t.AccessMs = time.Now().UnixMilli()
		_ = u.save(t)
	}
}

// release drops a message's reference only after its queue change is durable.
// Images never passed to a CLI are removed once the final reference is gone.
func (u *uploads) releaseRefs(imgs []queuedImage, sessionID, request string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, im := range imgs {
		t := u.byImage(im.ID)
		if t == nil || t.Returning || t.Deleting || t.Session != sessionID {
			continue
		}
		delete(t.Refs, request)
		if err := u.save(t); err != nil {
			continue
		}
		if len(t.Refs) == 0 && !t.Exposed {
			if err := u.removeOriginal(t); err != nil {
				slog.Error("remove cancelled image", "err", errClass(err))
			}
		}
	}
}

func (u *uploads) removeOriginal(t *transfer) error {
	return u.deleteUpload(t)
}

// A minute also covers the gap between user-message and turn-start records.
func reclaimable(t *transfer, pins map[string]bool) bool {
	return !t.Returning && !t.Deleting && t.FileID == "" && t.Session != "" && len(t.Refs) == 0 && t.Embedded && !pins[t.Session] && time.Since(time.UnixMilli(t.AccessMs)) >= time.Minute
}

func (u *uploads) records() ([]*transfer, error) {
	entries, err := os.ReadDir(filepath.Join(u.dir, "partial"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*transfer
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		t, err := u.load(strings.TrimSuffix(e.Name(), ".json"))
		if err == nil {
			out = append(out, t)
		}
	}
	return out, nil
}

// makeRoom evicts only embedded originals whose current turn has ended.
// Unsent uploads expire by TTL; they are drafts, not disposable cache entries.
func (u *uploads) makeRoom(add int64, pins map[string]bool) error {
	if err := u.room(add); err == nil || connect.CodeOf(err) != connect.CodeResourceExhausted {
		return err
	}
	records, err := u.records()
	if err != nil {
		return err
	}
	sort.Slice(records, func(i, j int) bool { return records[i].AccessMs < records[j].AccessMs })
	for _, t := range records {
		if reclaimable(t, pins) {
			if err := u.removeOriginal(t); err != nil {
				return err
			}
		}
		if err := u.room(add); err == nil || connect.CodeOf(err) != connect.CodeResourceExhausted {
			return err
		}
	}
	return connect.NewError(connect.CodeResourceExhausted, errors.New("附件空间不足：可回收内容已清理；请在设备的附件存储中查看占用、结束不用的会话或提高限额"))
}

func (u *uploads) usage(clean bool, pins map[string]bool) (*pb.ImageStorageUsage, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.gcLocked(time.Now())
	if !clean {
		_ = u.makeRoom(0, pins)
	} // a lowered limit reclaims eligible originals now
	records, err := u.records()
	if err != nil {
		return nil, err
	}
	usage := &pb.ImageStorageUsage{}
	sessions := map[string]*pb.ImageSessionStorage{}
	known := map[string]bool{}
	for _, t := range records {
		if clean && reclaimable(t, pins) {
			if err := u.removeOriginal(t); err != nil {
				return nil, err
			}
		}
		if t.artifactID() == "" {
			usage.ReservedBytes += max(int64(0), t.Total-t.Persisted)
			continue
		}
		path := u.pending(t.relativeName())
		if t.Session != "" {
			path = t.sessionPath(u)
		}
		fi, err := os.Stat(path)
		if err != nil {
			continue
		}
		known[path] = true
		if t.Session != "" {
			row := sessions[t.Session]
			if row == nil {
				row = &pb.ImageSessionStorage{SessionId: t.Session}
				sessions[t.Session] = row
			}
			row.Bytes += fi.Size()
			if reclaimable(t, pins) {
				usage.ReclaimableBytes += fi.Size()
			} else {
				usage.ProtectedBytes += fi.Size()
				row.ProtectedBytes += fi.Size()
			}
		} else {
			usage.ProtectedBytes += fi.Size()
		}
	}
	// Actual disk bytes include orphan files and all receiving chunks.
	err = filepath.WalkDir(u.dir, func(path string, e fs.DirEntry, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if e.Type().IsRegular() {
			fi, err := e.Info()
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			usage.UploadBytes += fi.Size()
			// Files without a record (a crash between the two writes) count
			// as protected, rather than disappearing from the breakdown.
			ext := filepath.Ext(path)
			if !known[path] && uploadDataPath(u.dir, path, ext) {
				usage.ProtectedBytes += fi.Size()
				rel, _ := filepath.Rel(u.dir, path)
				owner := strings.Split(rel, string(filepath.Separator))[0]
				if owner != "partial" && owner != "pending" {
					row := sessions[owner]
					if row == nil {
						row = &pb.ImageSessionStorage{SessionId: owner}
						sessions[owner] = row
					}
					row.Bytes += fi.Size()
					row.ProtectedBytes += fi.Size()
				}
			}
		}
		return nil
	})
	for _, row := range sessions {
		usage.Sessions = append(usage.Sessions, row)
	}
	sort.Slice(usage.Sessions, func(i, j int) bool { return usage.Sessions[i].Bytes > usage.Sessions[j].Bytes })
	return usage, err
}

type imageCacheFile struct {
	path    string
	size    int64
	at      time.Time
	store   *imageStore
	id      string
	display bool
}

// Cache blobs are replaceable; events already retain their previews and the
// CLI transcript retains the source. Pending displays evicted while offline
// explicitly become preview-only instead of pretending a large copy exists.
func (d *Daemon) trimImageCache(limit int64) (int64, error) {
	d.cacheMu.Lock()
	defer d.cacheMu.Unlock()
	d.mu.Lock()
	stores := map[string]*imageStore{}
	for id, s := range d.sessions {
		stores[id] = s.images
	}
	d.mu.Unlock()
	var files []imageCacheFile
	var total int64
	entries, err := os.ReadDir(paths.SessionsDir())
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		st := stores[e.Name()]
		if st == nil {
			st = openImageStore(paths.SessionDir(e.Name()))
		}
		st.mu.Lock()
		es, err := os.ReadDir(st.dir)
		if err != nil && !os.IsNotExist(err) {
			st.mu.Unlock()
			return 0, err
		}
		for _, f := range es {
			display := strings.HasSuffix(f.Name(), ".display")
			if !display && !strings.HasSuffix(f.Name(), ".preview") {
				continue
			}
			fi, err := f.Info()
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				st.mu.Unlock()
				return 0, err
			}
			ext := ".preview"
			if display {
				ext = ".display"
			}
			files = append(files, imageCacheFile{filepath.Join(st.dir, f.Name()), fi.Size(), fi.ModTime(), st, strings.TrimSuffix(f.Name(), ext), display})
			total += fi.Size()
		}
		st.mu.Unlock()
	}
	if total <= limit {
		return total, nil
	}
	sort.Slice(files, func(i, j int) bool { return files[i].at.Before(files[j].at) })
	for _, f := range files {
		if total <= limit {
			break
		}
		f.store.mu.Lock()
		if fi, err := os.Stat(f.path); err == nil && !fi.ModTime().Equal(f.at) {
			f.store.mu.Unlock()
			continue // touched after this LRU snapshot; reconsider next pass
		}
		if f.display {
			if im := f.store.byID[f.id]; im != nil && im.State == imagePending {
				im.State = imageFailed
				if err := f.store.saveLocked(); err != nil {
					f.store.mu.Unlock()
					return total, err
				}
			}
		}
		err := os.Remove(f.path)
		f.store.mu.Unlock()
		if err == nil || os.IsNotExist(err) {
			total -= f.size
		} else {
			return total, err
		}
	}
	return total, nil
}

func (d *Daemon) imageStorage(clean bool) (*pb.ImageStorageUsage, error) {
	usage, err := d.uploads.usage(clean, d.protectedImageSessions())
	if err != nil {
		return nil, err
	}
	limit := defaultImageCacheQuota
	if clean {
		limit = 0
	}
	usage.CacheBytes, err = d.trimImageCache(limit)
	return usage, err
}

func (d *Daemon) imageGCLoop(stop <-chan struct{}) {
	timer := time.NewTicker(10 * time.Minute)
	defer timer.Stop()
	for {
		if _, err := d.imageStorage(false); err != nil {
			slog.Error("clean image storage", "err", errClass(err))
		}
		select {
		case <-stop:
			return
		case <-timer.C:
		}
	}
}

// referenceRecords loads the transfer index once before session restoration.
func (u *uploads) referenceRecords() (map[string][]*transfer, error) {
	records, err := u.records()
	if err != nil {
		return nil, err
	}
	bySession := map[string][]*transfer{}
	for _, t := range records {
		if t.Session != "" {
			bySession[t.Session] = append(bySession[t.Session], t)
		}
	}
	return bySession, nil
}

// On startup durable queue/history acceptance wins over provisional claims.
func (u *uploads) restoreRefs(s *session, records []*transfer) {
	if len(records) == 0 || s.queueLost {
		return
	}
	wanted := map[string]bool{}
	for _, t := range records {
		if t.artifactID() != "" && !t.Returning && !t.Deleting {
			wanted[t.artifactID()] = true
		}
	}
	if len(wanted) == 0 {
		return
	}
	accepted := map[string]bool{}
	refsByID := map[string]map[string]bool{}
	add := func(id, request string, ref bool) {
		if !wanted[id] {
			return
		}
		accepted[id] = true
		if ref {
			if refsByID[id] == nil {
				refsByID[id] = map[string]bool{}
			}
			refsByID[id][request] = true
		}
	}
	for _, id := range s.queue.PendingUploads {
		add(id, "", false)
	}
	for kind, list := range [][]input{s.queue.Queue, s.queue.Held} {
		for _, in := range list {
			for _, f := range in.Files {
				add(f.ID, in.RequestID, true)
			}
			for _, im := range in.Images {
				add(im.ID, in.RequestID, kind == 0)
			}
		}
	}
	witness := map[string]bool{}
	// History binds acceptance to artifact IDs, never just a request ID:
	// the dedup window may have expired and that request ID may be reused.
	if err := s.log.Each(func(e *pb.Event) {
		if in := e.GetInputState(); in != nil && (in.Status == pb.InputStatus_INPUT_STATUS_QUEUED || in.Status == pb.InputStatus_INPUT_STATUS_HELD_BY_CLI) {
			for _, f := range in.Files {
				add(f.FileId, "", false)
			}
			for _, id := range in.ImageIds {
				add(id, "", false)
			}
		}
		if m := e.GetUserMessage(); m != nil {
			for _, f := range m.Files {
				add(f.FileId, "", false)
			}
			for _, im := range m.Images {
				if im.UploadId != "" && im.ImageId != "" {
					add(im.UploadId, "", false)
					witness[im.UploadId] = true
				}
			}
		}
	}); err != nil {
		slog.Error("restore upload acceptance", "session", s.meta.ID, "err", errClass(err))
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, snapshot := range records {
		t, err := u.load(snapshot.ID)
		if err != nil {
			if connect.CodeOf(err) != connect.CodeNotFound {
				slog.Error("reload upload references", "err", errClass(err))
			}
			continue
		}
		if t.Session != s.meta.ID || t.artifactID() != snapshot.artifactID() || t.Returning || t.Deleting {
			continue
		}
		id := t.artifactID()
		if !t.Accepted && !accepted[id] && !t.Exposed && !t.Embedded {
			if err := u.startReturn(t); err != nil {
				slog.Error("return unaccepted upload", "err", errClass(err))
			}
			continue
		}
		t.Accepted = true
		t.Refs = refsByID[id]
		if witness[t.ImageID] {
			t.Embedded = true
		}
		if err := u.save(t); err != nil {
			continue
		}
		if t.FileID == "" && len(t.Refs) == 0 && !t.Exposed {
			_ = u.removeOriginal(t)
		}
	}
}

func (u *uploads) touchPath(path string) {
	id := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	if !validHexID(id) {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if t := u.byImage(id); t != nil && t.Session != "" && filepath.Clean(path) == filepath.Join(u.sessionDir(t.Session), t.ImageID+"."+imageproc.Ext(t.Format)) {
		t.AccessMs = time.Now().UnixMilli()
		t.Exposed, t.Embedded = true, true
		_ = u.save(t)
	}
}
