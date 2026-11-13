package daemon

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

// Claude Code 2.1.322 at 44 columns wraps the fourth image label between
// "Image" and "#11". It has accepted the image; the injector must still
// recognize it, paste the fifth path, and pass its final ownership check.
func TestClaudeInjectWrappedImages(t *testing.T) {
	t.Parallel()
	for _, count := range []int{1, 5} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			h := claudeHarness(t, Config{})
			var names []string
			for i := range count {
				names = append(names, fmt.Sprintf("%d.jpg", i))
			}
			images := imagesFor(t, h.dir, names...)
			var labels []string
			h.term.onWrite = func(f *fakeTerm, b string) {
				if len(labels) == count {
					return // the final text and Enter
				}
				if b != string(agent.PastePath(images[len(labels)].Path)) {
					t.Fatalf("unexpected write before all images recognized: %q", b)
				}
				labels = append(labels, fmt.Sprintf("[Image #%d]", 8+len(labels)))
				box := strings.ReplaceAll(strings.Join(labels, " "), "[Image #11]", "[Image\n  #11]")
				f.screen = ccBox(box)
			}
			if _, err := h.s.enqueue("read the numbers", "images", images, originLocal); err != nil {
				t.Fatal(err)
			}
			h.s.status = pb.SessionStatus_SESSION_STATUS_IDLE
			outcome, err := h.s.inject(h.s.queue.Queue[0])
			if err != nil || outcome != written {
				t.Fatalf("inject = %v, %v; states %v", outcome, err, inputStates(h, "images"))
			}
			writes := h.term.written()
			if len(writes) != count+1 || writes[count] != string(agent.PasteBytes("read the numbers")) {
				t.Fatalf("did not submit after all images: %q", writes)
			}
			var want []int32
			for i := range count {
				want = append(want, int32(8+i))
			}
			in := h.s.queue.Queue[0]
			if !slices.Equal(in.Indexes, want) || in.Inflight == nil {
				t.Fatalf("wrong confirmation evidence: %+v", in)
			}
			// A local draft appearing after recognition still prevents
			// the final submission/cleanup ownership checks from passing.
			h.term.setScreen(ccBox(strings.Join(labels, " ") + " foreign draft"))
			if h.s.boxHolds(want, "") {
				t.Fatal("foreign text treated as owned images")
			}
		})
	}
}
