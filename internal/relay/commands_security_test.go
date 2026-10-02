package relay

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

func TestCorruptCommandLogFailsClosed(t *testing.T) {
	for _, data := range []string{`{"request":`, `null`, `{"request":null}`, `{"request":{}}`} {
		t.Run(data, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "commands.json")
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			log := openCommandLog(path)
			called := false
			result := log.do("request", time.Now().Add(time.Minute).UnixMilli(), nil, func() *pb.CommandResult { called = true; return &pb.CommandResult{} })
			if called || result.ErrorCode != "failed_precondition" {
				t.Fatalf("unsafe execution: called=%v result=%v", called, result)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != data {
				t.Fatalf("corrupt state overwritten: %q %v", got, err)
			}
		})
	}
}

func TestUnreadableCommandLogFailsClosed(t *testing.T) {
	log := openCommandLog(t.TempDir()) // directories cannot be read as log files
	called := false
	result := log.do("request", time.Now().Add(time.Minute).UnixMilli(), nil, func() *pb.CommandResult { called = true; return &pb.CommandResult{} })
	if called || result.ErrorCode != "failed_precondition" {
		t.Fatalf("unsafe execution: called=%v result=%v", called, result)
	}
}
