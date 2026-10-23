package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type fileReplacedError struct{ error }

func (e *fileReplacedError) Unwrap() error { return e.error }

// fileReplaced means the new file is visible, even if crash durability is unknown.
func fileReplaced(err error) bool {
	var replaced *fileReplacedError
	return errors.As(err, &replaced)
}

// writeFileSync atomically replaces an owner-only file. Success includes
// syncing its directory; a fileReplacedError leaves the new file visible
// but its durability across a crash uncertain.
func writeFileSync(path string, data []byte) error {
	return writeFileSyncDir(path, data, syncDir)
}

func (d *Daemon) writeStateFile(path string, data []byte) error {
	sync := d.syncStateDir
	if sync == nil {
		sync = syncDir
	}
	return writeFileSyncDir(path, data, sync)
}

func writeFileSyncDir(path string, data []byte, sync func(string) error) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if err := sync(filepath.Dir(path)); err != nil {
		return &fileReplacedError{fmt.Errorf("sync directory after replacing file: %w", err)}
	}
	return nil
}

// syncDir makes a rename or new file in dir durable.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
