package tail

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.jsonl")
	r := NewReader(path, 0)
	if lines, _, err := r.Next(); err != nil || len(lines) != 0 {
		t.Fatalf("missing file: %v %v", lines, err)
	}
	f, _ := os.Create(path)
	defer f.Close()
	f.WriteString("one\ntw")
	lines, _, _ := r.Next()
	if len(lines) != 1 || string(lines[0].Data) != "one" || lines[0].End != 4 {
		t.Fatalf("first read: %+v", lines)
	}
	if r.Offset() != 4 {
		t.Fatalf("offset %d", r.Offset())
	}
	f.WriteString("o\n\nthree\n")
	lines, _, _ = r.Next()
	if len(lines) != 2 || string(lines[0].Data) != "two" || string(lines[1].Data) != "three" || lines[1].End != 15 {
		t.Fatalf("second read: %+v", lines)
	}

	// Resume from a saved offset.
	r2 := NewReader(path, 4)
	lines, _, _ = r2.Next()
	if len(lines) != 2 || string(lines[0].Data) != "two" {
		t.Fatalf("resume: %+v", lines)
	}

	// Replacing the file with a shorter one restarts from the top.
	os.WriteFile(path, []byte("new\n"), 0o600)
	lines, truncated, _ := r.Next()
	if !truncated || len(lines) != 1 || string(lines[0].Data) != "new" {
		t.Fatalf("after truncate: %+v %v", lines, truncated)
	}
}

func TestLongLinesAreCompacted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.jsonl")
	f, _ := os.Create(path)
	defer f.Close()
	r := NewReader(path, 0)
	b64 := strings.Repeat("QUJD", (MaxLine+1024)/4)
	text := strings.Repeat("é\\\"x", ElideAbove) // escapes and UTF-8
	long := `{"type":"message","content":[{"type":"input_text","text":"hi"},{"type":"input_image","image_url":"data:image/png;base64,` + b64 + `"},{"type":"input_text","text":"` + text + `"}]}`
	// An incomplete long line waits for its end.
	f.WriteString(`{"a":1}` + "\n" + long[:len(long)/2])
	lines, _, err := r.Next()
	if err != nil || len(lines) != 1 || r.Offset() != 8 {
		t.Fatalf("partial: %d lines, offset %d, %v", len(lines), r.Offset(), err)
	}
	f.WriteString(long[len(long)/2:] + "\n" + `{"b":2}` + "\n")
	lines, _, err = r.Next()
	if err != nil || len(lines) != 2 {
		t.Fatalf("got %d lines, %v", len(lines), err)
	}
	l := lines[0]
	if !l.Compacted || l.End != int64(8+len(long)+1) || len(l.Data) > 200<<10 {
		t.Fatalf("compacted line: %v end %d len %d", l.Compacted, l.End, len(l.Data))
	}
	var m struct {
		Content []struct {
			Text     string `json:"text"`
			ImageURL string `json:"image_url"`
		} `json:"content"`
	}
	if err := json.Unmarshal(l.Data, &m); err != nil {
		t.Fatal(err)
	}
	if m.Content[0].Text != "hi" || !IsBlob(m.Content[1].ImageURL) || !strings.HasSuffix(m.Content[2].Text, "…") {
		t.Fatalf("content: %q %q", m.Content[0].Text, m.Content[1].ImageURL[:20])
	}
	got, err := ReadBlob(path, m.Content[1].ImageURL, 64<<20)
	if err != nil || got != "data:image/png;base64,"+b64 {
		t.Fatalf("blob: %d bytes, %v", len(got), err)
	}
	if _, err := ReadBlob(path, m.Content[1].ImageURL, 1024); err == nil {
		t.Fatal("limit not enforced")
	}
	if string(lines[1].Data) != `{"b":2}` {
		t.Fatalf("next line %q", lines[1].Data)
	}
}
