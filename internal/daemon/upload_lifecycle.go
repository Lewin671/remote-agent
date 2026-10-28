package daemon

import (
	"errors"
	"os"
	"path/filepath"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/paths"
)

// step gives fault tests the same boundaries used by every caller. A crash
// after a successful operation must be recoverable without the caller's undo.
func (u *uploads) step(name string, work func() error) error {
	if u.beforeUploadStep != nil {
		if err := u.beforeUploadStep(name); err != nil {
			return err
		}
	}
	if err := work(); err != nil {
		return err
	}
	if u.afterUploadStep != nil {
		u.afterUploadStep(name)
	}
	return nil
}

// All completed artifacts keep .part until the destination ancestry and the
// completion record are durable. Images use cleaned bytes, files a hard link.
func (u *uploads) commitUpload(t *transfer, write func(string) error) (*pb.ImageUploadStatus, error) {
	dst := u.pending(t.relativeName())
	if err := u.step("complete-directory", func() error { return u.mkdirUploadDir(filepath.Dir(dst)) }); err != nil {
		return nil, err
	}
	if err := u.step("complete-bytes", func() error { return write(dst) }); err != nil {
		return nil, err
	}
	if err := u.step("complete-destination", func() error { return u.syncUploadDirs(filepath.Dir(dst)) }); err != nil {
		u.removeCompletion(dst, t.FileID != "")
		return nil, err
	}
	if err := u.step("complete-record", func() error { return u.save(t) }); err != nil {
		if !fileReplaced(err) {
			u.removeCompletion(dst, t.FileID != "")
		}
		return nil, err
	}
	return u.finishCompletion(t)
}

func (u *uploads) removeCompletion(dst string, file bool) {
	if file {
		_ = os.RemoveAll(filepath.Dir(dst))
	} else {
		_ = os.Remove(dst)
	}
}

func (u *uploads) finishCompletion(t *transfer) (*pb.ImageUploadStatus, error) {
	if err := u.step("complete-unlink", func() error {
		err := os.Remove(u.partial(t.ID) + ".part")
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}); err != nil {
		return nil, err
	}
	if err := u.step("complete-source", func() error { return u.syncUploadDirs(filepath.Dir(u.partial(t.ID))) }); err != nil {
		return nil, err
	}
	return t.status(), nil
}

// The completion record may be visible after its last fsync failed. Re-sync
// both bytes' directory and record before discarding the retained original.
func (u *uploads) durableFileStatus(t *transfer) (*pb.ImageUploadStatus, error) {
	if err := u.step("complete-destination", func() error {
		path := u.pending(t.relativeName())
		if t.Session != "" {
			path = t.sessionPath(u)
		}
		return u.syncUploadDirs(filepath.Dir(path))
	}); err != nil {
		return nil, err
	}
	if err := u.step("complete-record", func() error { return u.save(t) }); err != nil {
		return nil, err
	}
	return u.finishCompletion(t)
}

// Session is the forward intent; Session+Returning is the reverse intent.
// The owner is never cleared until BOTH sides of rename have been synced.
// A retry handles bytes on either side, including a rename with failed fsync.
func (u *uploads) moveUpload(t *transfer) error {
	if err := u.step("move-intent", func() error { return u.save(t) }); err != nil {
		return err
	}
	src, dst := u.pending(t.relativeName()), t.sessionPath(u)
	if t.Returning {
		src, dst = dst, src
	}
	if _, err := os.Stat(src); err == nil {
		if err := u.step("move-directory", func() error { return u.mkdirUploadDir(filepath.Dir(dst)) }); err != nil {
			return err
		}
		if err := u.step("move-rename", func() error { return os.Rename(src, dst) }); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if _, err := os.Stat(dst); err == nil {
		if err := u.step("move-destination", func() error { return u.syncUploadDirs(filepath.Dir(dst)) }); err != nil {
			return err
		}
		if err := u.step("move-source", func() error { return u.syncExistingUploadDir(filepath.Dir(src)) }); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) || !t.Returning {
		return err
	}
	// Both missing on return means there are no bytes left to recover; let the
	// unowned record expire instead of pinning it to a vanished session forever.
	if t.Returning {
		owner, refs := t.Session, t.Refs
		t.Session, t.Returning, t.Refs = "", false, nil
		if err := u.step("move-clear", func() error { return u.save(t) }); err != nil {
			t.Session, t.Returning, t.Refs = owner, true, refs
			return err
		}
	}
	if t.FileID != "" {
		_ = os.Remove(filepath.Dir(src))
	} // empty only; not part of ownership
	return nil
}

func (u *uploads) startReturn(t *transfer) error {
	t.Returning = true
	t.Refs = nil
	return u.returnPending(t)
}
func (u *uploads) returnPending(t *transfer) error {
	if !t.Returning || t.Session == "" {
		return errors.New("upload has no return intent")
	}
	return u.moveUpload(t)
}

func (u *uploads) syncExistingUploadDir(dir string) error {
	for {
		if _, err := os.Stat(dir); err == nil {
			return u.syncUploadDirs(dir)
		} else if !os.IsNotExist(err) {
			return err
		}
		if dir == u.dir {
			return errors.New("missing upload root")
		}
		dir = filepath.Dir(dir)
	}
}

// Record deletion is last. A durable tombstone makes abort, expiry, image LRU
// and session exit the same retryable operation, even after bytes disappeared.
func (u *uploads) deleteUpload(t *transfer) error {
	t.Deleting = true
	if err := u.step("delete-intent", func() error { return u.save(t) }); err != nil {
		return err
	}
	if t.artifactID() != "" {
		path := u.pending(t.relativeName())
		if t.Session != "" {
			path = t.sessionPath(u)
		}
		if t.FileID != "" {
			path = filepath.Dir(path)
		}
		if err := u.step("delete-bytes", func() error { return os.RemoveAll(path) }); err != nil {
			return err
		}
		if err := u.step("delete-directory", func() error { return u.syncExistingUploadDir(filepath.Dir(path)) }); err != nil {
			return err
		}
	}
	if err := u.step("delete-part", func() error {
		err := os.Remove(u.partial(t.ID) + ".part")
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}); err != nil {
		return err
	}
	if err := u.step("delete-source", func() error { return u.syncUploadDirs(filepath.Dir(u.partial(t.ID))) }); err != nil {
		return err
	}
	if err := u.step("delete-record", func() error {
		err := os.Remove(u.partial(t.ID) + ".json")
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}); err != nil {
		return err
	}
	return u.step("delete-index", func() error { return u.syncUploadDirs(filepath.Dir(u.partial(t.ID))) })
}

// Startup and periodic GC use the same direction/priority. In particular a
// Returning upload cannot be moved forward or deleted as an exited session's.
func (u *uploads) recoverUpload(t *transfer) error {
	if t.Deleting {
		return u.deleteUpload(t)
	}
	if t.Returning {
		return u.returnPending(t)
	}
	if t.Session != "" {
		// Exited metadata remains a cleanup witness even if the entire upload
		// directory disappeared before its record could be unlinked.
		if m, err := loadMeta(paths.SessionDir(t.Session)); err == nil && m.Exited {
			return u.deleteUpload(t)
		}
		if err := u.moveUpload(t); err != nil {
			return err
		}
	}
	if t.artifactID() != "" {
		if _, err := os.Stat(u.partial(t.ID) + ".part"); err == nil {
			_, err = u.durableFileStatus(t)
			return err
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
