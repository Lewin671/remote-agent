package protocol

import (
	"os"
	"strings"
	"testing"
)

// A clear routing type changes the documented trust boundary even when all
// message content stays sealed. Keep that disclosure in both authoritative docs.
func TestScheduledPushMetadataDisclosure(t *testing.T) {
	for _, name := range []string{"scope.md", "architecture.md"} {
		b, err := os.ReadFile("../../docs/" + name)
		if err != nil {
			t.Fatal(err)
		}
		disclosed := false
		for _, line := range strings.Split(string(b), "\n") {
			if strings.Contains(line, "NOTIFY_KIND_SCHEDULED") && strings.Contains(line, "TURN_ENDED") && strings.Contains(line, "server 可以据此推断") {
				disclosed = true
			}
		}
		if !disclosed {
			t.Fatalf("%s omits scheduled push metadata disclosure", name)
		}
	}
}
