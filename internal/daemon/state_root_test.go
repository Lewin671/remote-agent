package daemon

import (
	"os"
	"path/filepath"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

func TestDaemonStateRootsAreIndependentOfProcessEnvironment(t *testing.T) {
	other := t.TempDir()
	t.Setenv("REMOTE_AGENT_HOME", other)
	roots := []string{t.TempDir(), t.TempDir()}
	for i, root := range roots {
		d := New(root, Config{})
		quota := int64(i+2) << 30
		if _, err := d.updateSettings(&pb.DaemonSettings{ImageUploadQuotaBytes: quota}); err != nil {
			t.Fatal(err)
		}
		if got := New(root, Config{}).settings().ImageUploadQuotaBytes; got != quota {
			t.Fatalf("root %d quota = %d", i, got)
		}
		id := uploadFile(t, d.uploads, "local.txt", []byte{byte(i)})
		recovered := newUploads(root)
		tr := recovered.byFile(id)
		if tr == nil {
			t.Fatal("upload was not persisted in its owner's root")
		}
		got, err := os.ReadFile(recovered.pending(tr.relativeName()))
		if err != nil || len(got) != 1 || got[0] != byte(i) {
			t.Fatalf("root %d upload = %v, %v", i, got, err)
		}
		if d.sessionDir("s") != filepath.Join(root, "sessions", "s") || recovered.sessionStateDir("s") != d.sessionDir("s") {
			t.Fatal("daemon and uploads disagree about the state root")
		}
	}
	if entries, err := os.ReadDir(other); err != nil || len(entries) != 0 {
		t.Fatalf("explicit roots wrote to the environment's directory: %v %v", entries, err)
	}
}
