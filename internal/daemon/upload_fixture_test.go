package daemon

import (
	"os"
	"testing"
)

// Instance-root fixtures can overlap at the top level as well as within each
// matrix. Tests that change process environment or cwd stay sequential.
// Storage matrices keep real files and fsyncs. Bound their concurrent disk work
// even when the test runner exposes more than four CPUs.
var storageTestSlots = make(chan struct{}, 4)

func parallelStorageTest(t *testing.T) {
	t.Helper()
	t.Parallel()
	storageTestSlots <- struct{}{}
	t.Cleanup(func() { <-storageTestSlots })
}

type completedUploadFixture struct {
	images, files []string
	record        *transfer
	bytes         []byte
}

// Prepare one completed upload; each fault case gets an independent disk copy.
func completedUpload(t *testing.T, image bool, name string, data []byte) func(*testing.T, *uploads) completedUploadFixture {
	t.Helper()
	u := newUploads(t.TempDir())
	var f completedUploadFixture
	if image {
		f.images = []string{storageUpload(t, u)}
		f.record = u.byImage(f.images[0])
	} else {
		f.files = []string{uploadFile(t, u, name, data)}
		f.record = u.byFile(f.files[0])
	}
	var err error
	f.bytes, err = os.ReadFile(u.pending(f.record.relativeName()))
	if err != nil {
		t.Fatal(err)
	}
	return func(t *testing.T, target *uploads) completedUploadFixture {
		t.Helper()
		if err := os.CopyFS(target.dir, os.DirFS(u.dir)); err != nil {
			t.Fatal(err)
		}
		result := f
		record, err := target.load(f.record.ID)
		result.record = record
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
}

// Copy prepared disk state into each case's private directory. Setup runs the
// real upload path once; fault injection and recovery still use real I/O and
// fsyncs in every case. Copies must not share writable files or upload records.
func copyUploadState(t *testing.T, source string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS(source)); err != nil {
		t.Fatal(err)
	}
	return root
}
