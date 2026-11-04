package daemon

import (
	"bytes"
	"fmt"
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

func TestAgy150UploadedMediaConfirmsWithoutTextPlaceholders(t *testing.T) {
	h := agyHarness(t)
	root := t.TempDir()
	conversation := filepath.Join(root, "conv")
	media := filepath.Join(conversation, ".user_uploaded")
	if err := os.MkdirAll(media, 0700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := os.WriteFile(filepath.Join(media, fmt.Sprintf("red-%d.png", i)), testPNG(t, 2, 2, color.RGBA{R: 255, A: 255}), 0600); err != nil {
			t.Fatal(err)
		}
	}
	fixture, err := os.ReadFile("../antigravity/testdata/transcript/user-images-1.5.0.json")
	if err != nil {
		t.Fatal(err)
	}
	fixture = bytes.ReplaceAll(fixture, []byte("/workspace/conversations/example"), []byte(conversation))
	p := antigravity.NewParser()
	p.SetTranscriptPath(filepath.Join(conversation, ".system_generated", "logs", "transcript.jsonl"))
	events, err := p.Parse(fixture)
	if err != nil {
		t.Fatal(err)
	}
	text := "What is the dominant color in both attached images? Reply with RED only, without tools."
	in := input{CLIConversation: "conv", Text: text, RequestID: "image-request", Origin: storeOrigin(originLocal), Indexes: []int32{1, 2}, Images: []queuedImage{{ID: "one"}, {ID: "two"}}}
	h.s.recent = []input{in}
	for _, e := range events {
		if message := e.GetUserMessage(); message != nil {
			if message.Text != text || !h.s.inputMatchesEvent(in, e) {
				t.Fatalf("native media did not confirm input: %v", message)
			}
			if h.s.inputMatchesText(in, text) {
				t.Fatal("image-less history confirmed image send")
			}
			h.s.mu.Lock()
			consume := h.s.annotateUserMessage(e, message)
			if consume != nil {
				consume()
			}
			h.s.mu.Unlock()
			if message.RequestId != in.RequestID || len(message.Images) != 2 || message.Images[0].UploadId != "one" || message.Images[1].UploadId != "two" {
				t.Fatalf("upload identity lost: %v", message)
			}
			return
		}
	}
	t.Fatal("missing user message")
}
