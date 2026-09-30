package claude

import (
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

func TestImages(t *testing.T) {
	p := NewParser()
	parse := func(line string) []*pb.Event {
		t.Helper()
		evs, err := p.Parse([]byte(line))
		if err != nil {
			t.Fatal(err)
		}
		return evs
	}
	evs := parse(`{"type":"user","promptId":"p1","origin":{"kind":"human"},"imagePasteIds":[3,4],"message":{"role":"user","content":[{"type":"text","text":"[Image #3] [Image #4]what"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}},{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"BBBB"}}]}}`)
	imgs := p.Images()
	m := evs[len(evs)-1].GetUserMessage()
	if m == nil || len(m.Images) != 2 || len(imgs) != 2 || m.Images[0].CliIndex != 3 || m.Images[1].CliIndex != 4 || imgs[1].Data != "BBBB" {
		t.Fatalf("user message %v %v", m, imgs)
	}
	// Queued while running: the prompt is a list of blocks.
	evs = parse(`{"type":"attachment","attachment":{"type":"queued_command","origin":{"kind":"human"},"imagePasteIds":[10],"prompt":[{"type":"text","text":"[Image #10]more"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"CCCC"}}]}}`)
	imgs = p.Images()
	if m := evs[0].GetUserMessage(); m == nil || len(m.Images) != 1 || m.Images[0].CliIndex != 10 || len(imgs) != 1 {
		t.Fatalf("queued %v", evs)
	}
	// Read returns the image; the file's size is kept when Read shrank it.
	parse(`{"type":"assistant","message":{"id":"m1","model":"claude-haiku-4-5","content":[{"type":"tool_use","id":"r1","name":"Read","input":{"file_path":"/tmp/big.png"}}]}}`)
	evs = parse(`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"r1","type":"tool_result","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"DDDD"}}]}]},"toolUseResult":{"type":"image","file":{"base64":"DDDD","type":"image/png","originalSize":9,"dimensions":{"originalWidth":4000,"originalHeight":3000,"displayWidth":2000,"displayHeight":1500}}}}`)
	imgs = p.Images()
	tc := evs[0].GetToolCall()
	if tc == nil || len(tc.Images) != 1 || len(imgs) != 1 {
		t.Fatalf("read %v", evs)
	}
	if r := tc.Images[0]; r.Path != "/tmp/big.png" || r.OriginalWidth != 4000 || r.OriginalHeight != 3000 || r.Source != pb.ImageSource_IMAGE_SOURCE_TOOL {
		t.Fatalf("ref %v", r)
	}
}

func TestBackgroundCalls(t *testing.T) {
	for _, c := range []struct {
		result string
		want   pb.ToolBackground
		id     string
	}{
		{`{"stdout":"","backgroundTaskId":"b1"}`, pb.ToolBackground_TOOL_BACKGROUND_REQUESTED, "b1"},
		{`{"stdout":"","backgroundTaskId":"b2","timedOutAfterMs":60000}`, pb.ToolBackground_TOOL_BACKGROUND_TIMED_OUT, "b2"},
		{`{"stdout":"","backgroundTaskId":"b3","backgroundedByUser":true}`, pb.ToolBackground_TOOL_BACKGROUND_BY_USER, "b3"},
		{`{"isAsync":true,"status":"async_launched","agentId":"a1"}`, pb.ToolBackground_TOOL_BACKGROUND_REQUESTED, "a1"},
		{`{"stdout":"done"}`, pb.ToolBackground_TOOL_BACKGROUND_UNSPECIFIED, ""},
	} {
		p := NewParser()
		p.Parse([]byte(`{"type":"assistant","message":{"id":"m","model":"claude-haiku-4-5","content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"sleep 99"}}]}}`))
		evs, _ := p.Parse([]byte(`{"type":"user","message":{"role":"user","content":[{"tool_use_id":"t1","type":"tool_result","content":"x"}]},"toolUseResult":` + c.result + `}`))
		tc := evs[0].GetToolCall()
		if tc.Background != c.want || tc.BackgroundId != c.id {
			t.Fatalf("%s: %v %q", c.result, tc.Background, tc.BackgroundId)
		}
	}
}
