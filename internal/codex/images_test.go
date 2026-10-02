package codex

import (
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

func TestImages(t *testing.T) {
	p := NewParser()
	parse := func(line string) ([]*pb.Event, int) {
		t.Helper()
		evs, err := p.Parse([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
		return evs, len(p.Images())
	}
	// A pasted image: bytes in the response item, the path in the item.
	evs, n := parse(`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<image name=[Image #1] path=\"/tmp/a.png\">"},{"type":"input_image","image_url":"data:image/png;base64,AAAA"},{"type":"input_text","text":"</image>"},{"type":"input_text","text":"[Image #1] what"}]}}`)
	if len(evs) != 0 || n != 0 {
		t.Fatalf("response item: %d events, %d images", len(evs), n)
	}
	evs, err := p.Parse([]byte(`{"type":"event_msg","payload":{"type":"item_completed","turn_id":"t","item":{"type":"UserMessage","id":"u","content":[{"type":"local_image","path":"/tmp/a.png"},{"type":"text","text":"[Image #1] what"}]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	imgs := p.Images()
	m := evs[0].GetUserMessage()
	if m == nil || len(m.Images) != 1 || len(imgs) != 1 || imgs[0].Ref != m.Images[0] || imgs[0].Data != "data:image/png;base64,AAAA" {
		t.Fatalf("user message: %v %v", m, imgs)
	}
	if r := m.Images[0]; r.CliIndex != 1 || r.Path != "/tmp/a.png" || r.Source != pb.ImageSource_IMAGE_SOURCE_USER {
		t.Fatalf("ref %v", r)
	}

	// view_image: the call is reported with the output that has the image.
	evs, _ = parse(`{"type":"event_msg","payload":{"type":"item_completed","turn_id":"t","item":{"type":"ImageView","id":"v","path":"file:///tmp/b.png"}}}`)
	if len(evs) != 0 {
		t.Fatalf("image view reported early: %v", evs)
	}
	evs, n = parse(`{"type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"c","output":[{"type":"input_text","text":"done"},{"type":"input_image","image_url":"data:image/png;base64,BBBB"}]}}`)
	if len(evs) != 1 || n != 1 {
		t.Fatalf("view: %v %d", evs, n)
	}
	if tc := evs[0].GetToolCall(); tc.CallId != "v" || len(tc.Images) != 1 || tc.Images[0].Path != "/tmp/b.png" || tc.Images[0].Source != pb.ImageSource_IMAGE_SOURCE_TOOL {
		t.Fatalf("tool call %v", tc)
	}
	// An ImageView whose output has no image is reported at the turn's end.
	parse(`{"type":"event_msg","payload":{"type":"item_completed","turn_id":"t","item":{"type":"ImageView","id":"v2","path":"/tmp/c.png"}}}`)
	evs, _ = parse(`{"type":"event_msg","payload":{"type":"task_complete","turn_id":"t"}}`)
	if len(evs) != 2 || evs[0].GetToolCall().GetCallId() != "v2" || evs[1].GetTurnEnded() == nil {
		t.Fatalf("flush: %v", evs)
	}

	// A generated image counts once, even though "generating".
	evs, n = parse(`{"type":"event_msg","payload":{"type":"item_completed","turn_id":"t","item":{"type":"Extension","kind":"image_gen.generation","id":"ig","status":"generating","revisedPrompt":"a cat","result":"iVBO","savedPath":"/g/ig.png"}}}`)
	g := evs[0].GetImageGenerated()
	if g == nil || n != 1 || g.CallId != "ig" || g.RevisedPrompt != "a cat" || g.Images[0].Path != "/g/ig.png" || g.Images[0].Source != pb.ImageSource_IMAGE_SOURCE_GENERATED {
		t.Fatalf("generated: %v", evs)
	}
	if evs, n = parse(`{"type":"response_item","payload":{"type":"image_generation_call","id":"ig","status":"generating","result":"iVBO"}}`); len(evs) != 0 || n != 0 {
		t.Fatal("generated image reported twice")
	}
	// Older versions only have the response item.
	if evs, _ = parse(`{"type":"response_item","payload":{"type":"image_generation_call","id":"ig2","status":"completed","revised_prompt":"x","result":"iVBO"}}`); evs[0].GetImageGenerated() == nil {
		t.Fatal("old generation missed")
	}
	// A failed generation without a result stays a plain tool call.
	if evs, n = parse(`{"type":"event_msg","payload":{"type":"item_completed","turn_id":"t","item":{"type":"Extension","kind":"image_gen.generation","id":"ig3","status":"failed","result":""}}}`); evs[0].GetToolCall() == nil || n != 0 {
		t.Fatalf("failed generation: %v", evs)
	}
}

// G15: one exec that views two images: each image goes with its own view,
// and no image gets another's path.
func TestImagesTwoViewsInOneCall(t *testing.T) {
	p := NewParser()
	for _, l := range []string{
		`{"type":"event_msg","payload":{"type":"item_completed","turn_id":"t","item":{"type":"ImageView","id":"v1","path":"/a.png"}}}`,
		`{"type":"event_msg","payload":{"type":"item_completed","turn_id":"t","item":{"type":"ImageView","id":"v2","path":"/b.png"}}}`,
	} {
		if evs, _ := p.Parse([]byte(l)); len(evs) != 0 {
			t.Fatalf("reported early: %v", evs)
		}
	}
	evs, _ := p.Parse([]byte(`{"type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"c","output":[{"type":"input_image","image_url":"data:image/png;base64,AA"},{"type":"input_image","image_url":"data:image/png;base64,BB"}]}}`))
	imgs := p.Images()
	if len(evs) != 2 || len(imgs) != 2 {
		t.Fatalf("%d events, %d images", len(evs), len(imgs))
	}
	for i, want := range []string{"/a.png", "/b.png"} {
		tc := evs[i].GetToolCall()
		if len(tc.Images) != 1 || tc.Images[0].Path != want {
			t.Fatalf("call %d: %v", i, tc)
		}
	}
	if imgs[0].Data != "data:image/png;base64,AA" || imgs[0].Ref != evs[0].GetToolCall().Images[0] {
		t.Fatal("images paired wrongly")
	}
	// When the counts differ, which is which is unknown: no paths.
	p.Parse([]byte(`{"type":"event_msg","payload":{"type":"item_completed","turn_id":"t","item":{"type":"ImageView","id":"v3","path":"/c.png"}}}`))
	evs, _ = p.Parse([]byte(`{"type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"c2","output":[{"type":"input_image","image_url":"data:image/png;base64,AA"},{"type":"input_image","image_url":"data:image/png;base64,BB"}]}}`))
	for _, e := range evs {
		for _, r := range e.GetToolCall().GetImages() {
			if r.Path != "" {
				t.Fatalf("path %q given without knowing", r.Path)
			}
		}
	}
}
