package daemon

import (
	"os"
	"testing"
)

// Fault matrices keep real files and fsyncs. Bound their concurrent disk work
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

// Build the same completed state for claim/return recovery, using the existing
// image and file helpers (including the file begin/complete idempotency checks).
func completedUpload(t *testing.T, u *uploads, image bool, name string, data []byte) completedUploadFixture {
	t.Helper()
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
	return f
}
