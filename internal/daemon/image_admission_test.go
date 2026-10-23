package daemon

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/imageproc"
	"github.com/Lewin671/remote-agent/internal/tail"
)

func TestTranscriptImageAdmissionBeforeMaterialization(t *testing.T) {
	for _, source := range []string{"blob", "base64"} {
		t.Run(source, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				first, second := imageproc.Acquire(), imageproc.Acquire()
				defer second.Release()
				st := openImageStore(t.TempDir())
				ref := &pb.ImageRef{}
				f := agent.FoundImage{Ref: ref, Data: "invalid base64"}
				path := filepath.Join(t.TempDir(), "transcript.jsonl")
				raw := testPNG(t, 20, 10, color.RGBA{R: 255, A: 255})
				blob, err := json.Marshal(base64.StdEncoding.EncodeToString(raw))
				if err != nil {
					t.Fatal(err)
				}
				if source == "blob" {
					f.Data = fmt.Sprintf("%s1:%d", tail.BlobPrefix, len(blob)-1)
				}
				done := make(chan struct{})
				go func() { st.process(f, path); close(done) }()
				synctest.Wait()
				select {
				case <-done:
					first.Release()
					t.Fatal("image source was read/decoded without admission")
				default:
				}
				// The source only becomes readable after the worker is waiting for admission.
				if source == "blob" {
					if err := os.WriteFile(path, blob, 0600); err != nil {
						first.Release()
						t.Fatal(err)
					}
				}
				first.Release()
				<-done
				if source == "blob" && (ref.ImageId == "" || ref.Width != 20 || ref.Height != 10) {
					t.Fatalf("blob was read too soon: %v", ref)
				}
				if source == "base64" && ref.Sync != pb.ImageSync_IMAGE_SYNC_NAME_ONLY {
					t.Fatal("malformed source unexpectedly accepted")
				}
				// Error and success paths must both return their admission.
				free := imageproc.Acquire()
				free.Release()
			})
		})
	}
}

func TestUploadAdmissionBeforeReadAndThroughValidation(t *testing.T) {
	for _, phase := range []string{"before-read", "after-decode"} {
		t.Run(phase, func(t *testing.T) {
			t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
			synctest.Test(t, func(t *testing.T) {
				u := newUploads()
				data := testPNG(t, 20, 10, color.RGBA{G: 255, A: 255})
				beginUpload(t, u, data, "phone")
				if _, err := u.put(&pb.PutImageChunkRequest{TransferId: tid, Data: data}, "phone"); err != nil {
					t.Fatal(err)
				}
				first := imageproc.Acquire()
				defer first.Release()
				done := make(chan error, 1)
				if phase == "before-read" {
					second := imageproc.Acquire()
					go func() { _, err := u.complete(tid, "phone"); done <- err }()
					synctest.Wait()
					// If completion read its bytes before acquiring admission it would
					// still succeed after this change, using the stale buffered source.
					if err := os.WriteFile(u.partial(tid)+".part", []byte("changed"), 0600); err != nil {
						second.Release()
						t.Fatal(err)
					}
					second.Release()
					if err := <-done; err == nil {
						t.Fatal("upload materialized bytes before admission")
					}
				} else {
					validating, resume := make(chan struct{}), make(chan struct{})
					// Quota validation runs after decode and metadata cleaning and before
					// the decoded image's dimensions are copied into the final record.
					u.limits = func() int64 { close(validating); <-resume; return maxUploadStorage }
					go func() { _, err := u.complete(tid, "phone"); done <- err }()
					<-validating
					admitted := make(chan struct{})
					go func() { p := imageproc.Acquire(); close(admitted); p.Release() }()
					synctest.Wait()
					select {
					case <-admitted:
						close(resume)
						<-done
						t.Fatal("upload released admission while decoded pixels were still in use")
					default:
					}
					close(resume)
					if err := <-done; err != nil {
						t.Fatal(err)
					}
					<-admitted
				}
				free := imageproc.Acquire()
				free.Release()
			})
		})
	}
}
