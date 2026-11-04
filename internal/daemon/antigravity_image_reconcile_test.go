package daemon

import (
	"image/color"
	"os"
	"path/filepath"
	"testing"

	"github.com/Lewin671/remote-agent/internal/antigravity"
)

func TestAgyCopiedUserMediaRetainsRequestAndUploadIdentity(t *testing.T) {
	h := agyHarness(t)
	root := t.TempDir()
	media := filepath.Join(root, "conv", ".system_generated", "logs", "media")
	if err := os.MkdirAll(media, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(media, "step_0_media_0.png")
	if err := os.WriteFile(path, testPNG(t, 2, 2, color.RGBA{R: 255, A: 255}), 0600); err != nil {
		t.Fatal(err)
	}
	p := antigravity.NewParser()
	p.SetTranscriptPath(filepath.Join(filepath.Dir(media), "transcript.jsonl"))
	events, err := p.Parse([]byte(`{"step_index":0,"type":"USER_INPUT","content":"<USER_REQUEST>look [Image #1]</USER_REQUEST>","media":[{"mime_type":"image/png","uri":"file://` + path + `"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	in := input{CLIConversation: "conv", Text: "look", RequestID: "image-request", Origin: storeOrigin(originLocal), Indexes: []int32{1}, Images: []queuedImage{{ID: "image-upload", Path: filepath.Join(root, "original-upload.png")}}}
	h.s.recent = []input{in}
	for _, e := range events {
		if msg := e.GetUserMessage(); msg != nil {
			if !h.s.inputMatchesEvent(in, e) {
				t.Fatal("the transcript copy did not confirm the submitted image")
			}
			h.s.mu.Lock()
			consume := h.s.annotateUserMessage(e, msg)
			if consume != nil {
				consume()
			}
			h.s.mu.Unlock()
			if msg.RequestId != "image-request" || len(msg.Images) != 1 || msg.Images[0].UploadId != "image-upload" || msg.Images[0].Path != "" || len(h.s.recent) != 0 {
				t.Fatalf("image identity was not reconciled: %v", msg)
			}
			if found := p.Images(); len(found) != 1 || found[0].Data == "" {
				t.Fatal("media bytes were lost while separating upload identity")
			}
			return
		}
	}
	t.Fatal("no parsed user message")
}
