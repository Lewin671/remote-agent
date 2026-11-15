package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"testing"
	"testing/synctest"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

func TestUploadLockWaitHonorsCallerDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		u := newUploads(t.TempDir())
		u.mu.Lock()
		defer u.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		done := make(chan error, 1)
		go func() {
			_, err := u.putContext(ctx, &pb.PutImageChunkRequest{TransferId: tid, Data: []byte("x")}, "fixture")
			done <- err
		}()
		synctest.Wait()
		time.Sleep(2 * time.Second)
		synctest.Wait()
		select {
		case err := <-done:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
		default:
			t.Fatal("canceled upload remained on global lock")
		}
	})
}
func TestCompleteFileHashChecksCancellationBetweenReads(t *testing.T) {
	u := newUploads(t.TempDir())
	data := bytes.Repeat([]byte("fixture"), 20000)
	sum := sha256.Sum256(data)
	if _, err := u.begin(&pb.BeginImageUploadRequest{TransferId: tid, FileName: "fixture.bin", TotalBytes: int64(len(data)), Sha256: sum[:]}, "fixture"); err != nil {
		t.Fatal(err)
	}
	if _, err := u.put(&pb.PutImageChunkRequest{TransferId: tid, Data: data}, "fixture"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reads := 0
	u.hashFile = func(dst io.Writer, src io.Reader) (int64, error) {
		b := make([]byte, 1<<20)
		n, err := src.Read(b)
		if err != nil {
			return 0, err
		}
		reads++
		if n > 64<<10 {
			t.Errorf("unbounded hash read: %d", n)
		}
		cancel()
		_, err = src.Read(b)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("hash ignored cancellation: %v", err)
		}
		return int64(n), err
	}
	if _, err := u.completeContext(ctx, tid, "fixture"); !errors.Is(err, context.Canceled) {
		t.Fatalf("completion=%v", err)
	}
	if reads != 1 {
		t.Fatal("unexpected hash execution")
	}
	st, err := u.status(context.Background(), tid, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if st.State != pb.ImageUploadState_IMAGE_UPLOAD_STATE_RECEIVING || st.PersistedBytes != int64(len(data)) {
		t.Fatal("canceled hashing changed durable upload ownership/offset")
	}
}
