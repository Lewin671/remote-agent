package daemon

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
)

func TestFileHashCannotCompleteAnotherBeginGeneration(t *testing.T) {
	for _, completed := range []bool{false, true} {
		name := "receiving-with-identical-inode"
		if completed {
			name = "already-complete"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
			u := newUploads()
			original, replacement := []byte("old!"), []byte("new!")
			sum := sha256.Sum256(original)
			req := &pb.BeginImageUploadRequest{TransferId: tid, FileName: "race.bin", TotalBytes: int64(len(original)), Sha256: sum[:]}
			if _, err := u.begin(req, "phone"); err != nil {
				t.Fatal(err)
			}
			if _, err := u.put(&pb.PutImageChunkRequest{TransferId: tid, Data: original}, "phone"); err != nil {
				t.Fatal(err)
			}
			part := u.partial(tid) + ".part"
			before, err := os.Stat(part)
			if err != nil {
				t.Fatal(err)
			}
			// Deterministically give the replacement the exact original identity,
			// rather than relying on this filesystem actually recycling an inode.
			alias := filepath.Join(t.TempDir(), "original-inode")
			if !completed {
				if err := os.Link(part, alias); err != nil {
					t.Fatal(err)
				}
			}
			hashed, resume := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			u.hashFile = func(w io.Writer, r io.Reader) (int64, error) {
				n, err := io.Copy(w, r)
				if calls.Add(1) == 1 {
					close(hashed)
					<-resume
				}
				return n, err
			}
			type result struct {
				status *pb.ImageUploadStatus
				err    error
			}
			done := make(chan result, 1)
			go func() { st, err := u.complete(tid, "phone"); done <- result{st, err} }()
			<-hashed
			joined := false
			defer func() {
				if !joined {
					close(resume)
					<-done
				}
			}()
			if err := u.abort(tid, "phone"); err != nil {
				t.Fatal(err)
			}
			next := proto.Clone(req).(*pb.BeginImageUploadRequest)
			if completed {
				nextSum := sha256.Sum256(replacement)
				next.Sha256 = nextSum[:]
			}
			if _, err := u.begin(next, "phone"); err != nil {
				t.Fatal(err)
			}
			if !completed {
				if err := os.Remove(part); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(alias, part); err != nil {
					t.Fatal(err)
				}
				now, err := os.Stat(part)
				if err != nil || !os.SameFile(before, now) {
					t.Fatal("fixture did not preserve original inode identity", err)
				}
			}
			if _, err := u.put(&pb.PutImageChunkRequest{TransferId: tid, Data: replacement}, "phone"); err != nil {
				t.Fatal(err)
			}
			var newStatus *pb.ImageUploadStatus
			if completed {
				newStatus, err = u.complete(tid, "phone")
				if err != nil {
					t.Fatal(err)
				}
			}
			close(resume)
			got := <-done
			joined = true
			if got.status != nil || connect.CodeOf(got.err) != connect.CodeAborted {
				t.Fatalf("old hash completed replacement generation: status=%v err=%v", got.status, got.err)
			}
			current, err := u.load(tid)
			if err != nil {
				t.Fatal(err)
			}
			path := part
			if completed {
				if current.FileID != newStatus.FileId {
					t.Fatal("stale completion changed the new generation's file ID")
				}
				path = u.pending(current.relativeName())
			} else if current.FileID != "" {
				t.Fatal("unverified replacement gained a completed file ID")
			}
			data, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(data, replacement) {
				t.Fatal("replacement bytes changed", err)
			}
			if !completed {
				u.hashFile = nil
				if _, err := u.complete(tid, "phone"); connect.CodeOf(err) != connect.CodeInvalidArgument {
					t.Fatal("replacement was not checked against its own hash", err)
				}
			}
		})
	}
}

func TestRepeatedBeginKeepsActiveHashAndDescriptorThroughCompletion(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	u := newUploads()
	data := []byte("same")
	sum := sha256.Sum256(data)
	req := &pb.BeginImageUploadRequest{TransferId: tid, FileName: "retry.bin", TotalBytes: int64(len(data)), Sha256: sum[:]}
	if _, err := u.begin(req, "phone"); err != nil {
		t.Fatal(err)
	}
	if _, err := u.put(&pb.PutImageChunkRequest{TransferId: tid, Data: data}, "phone"); err != nil {
		t.Fatal(err)
	}
	hashed, resume := make(chan struct{}), make(chan struct{})
	var descriptor *os.File
	u.hashFile = func(w io.Writer, r io.Reader) (int64, error) {
		descriptor = r.(*os.File)
		n, err := io.Copy(w, r)
		close(hashed)
		<-resume
		return n, err
	}
	checked := false
	u.beforeUploadStep = func(stage string) error {
		if stage == "complete-record" {
			checked = true
			_, err := descriptor.Stat()
			return err
		}
		return nil
	}
	done := make(chan error, 1)
	go func() { _, err := u.complete(tid, "phone"); done <- err }()
	<-hashed
	_, beginErr := u.begin(proto.Clone(req).(*pb.BeginImageUploadRequest), "phone")
	close(resume)
	if err := <-done; err != nil {
		t.Fatal("idempotent Begin invalidated active completion or descriptor closed early", err)
	}
	if beginErr != nil {
		t.Fatal(beginErr)
	}
	if !checked {
		t.Fatal("completion did not reach record commit")
	}
	if _, err := descriptor.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatal("descriptor leaked after completion")
	}
}
