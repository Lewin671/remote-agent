package daemon

import (
	"encoding/base64"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseErrorsKeepPendingImages(t *testing.T) {
	for _, bad := range []string{
		`{"type":"future_record"}`,
		`{"type":"event_msg","payload":{"type":"task_complete","duration_ms":"bad"}}`,
		`{"type":"event_msg","payload":{"type":"turn_aborted","duration_ms":"bad"}}`,
		`{broken`,
	} {
		t.Run(bad, func(t *testing.T) { checkPendingImagesAfterBadRecord(t, bad, false) })
	}
}

func TestDiagnosticRetryKeepsPendingImages(t *testing.T) {
	checkPendingImagesAfterBadRecord(t, `{"type":"future_record"}`, true)
}

func checkPendingImagesAfterBadRecord(t *testing.T, bad string, diagnosticFailure bool) {
	t.Helper()
	h := newHarness(t, Config{})
	b64 := base64.StdEncoding.EncodeToString(testPNG(t, 1, 1, color.RGBA{R: 255, A: 255}))
	prefix := []string{
		`{"type":"event_msg","payload":{"type":"task_started","turn_id":"t"}}`,
		`{"type":"event_msg","payload":{"type":"item_completed","turn_id":"t","item":{"type":"ImageView","id":"view","path":"/tmp/pending.png"}}}`,
		fmt.Sprintf(`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<image name=[Image #1] path=\"/tmp/pending.png\">"},{"type":"input_image","image_url":"data:image/png;base64,%s"}]}}`, b64),
	}
	h.writeRollout(prefix...)
	path := filepath.Join(h.dir, "rollout.jsonl")
	h.s.bindRollout("cli", path)
	diag := filepath.Join(h.dir, "parse-health.json")
	if diagnosticFailure {
		if err := os.Mkdir(diag, 0700); err != nil {
			t.Fatal(err)
		}
	}
	h.writeRollout(bad,
		`{"type":"event_msg","payload":{"type":"item_completed","turn_id":"t","item":{"type":"UserMessage","content":[{"type":"local_image","path":"/tmp/pending.png"}]}}}`,
		`{"type":"event_msg","payload":{"type":"task_complete","turn_id":"t"}}`)
	if diagnosticFailure {
		if h.s.readRollout() {
			t.Fatal("diagnostic write unexpectedly succeeded")
		}
		os.Remove(diag)
	}
	if !h.s.readRollout() {
		t.Fatal("read failed")
	}
	views, users, ends := 0, 0, 0
	for _, e := range h.events() {
		if c := e.GetToolCall(); c != nil && c.CallId == "view" {
			views++
		}
		if m := e.GetUserMessage(); m != nil {
			users++
			if len(m.Images) != 1 || m.Images[0].ImageId == "" {
				t.Fatalf("pending user image bytes lost: %v", m)
			}
		}
		if e.GetTurnEnded() != nil {
			ends++
		}
	}
	if views != 1 || users != 1 || ends != 1 {
		t.Fatalf("pending state lost: views=%d users=%d ends=%d bad=%s", views, users, ends, strings.Split(bad, "\n")[0])
	}
}
