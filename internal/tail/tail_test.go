package tail

import (
	"bufio"
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

// G12: compacting a huge line that has nothing long to replace stops as
// soon as the output passes the limit, instead of building all of it.
func TestCompactionStopsAtTheLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.jsonl")
	line := "[" + strings.Repeat(`"ab",`, (3*MaxLine)/5) + `"ab"]`
	os.WriteFile(path, []byte(line+"\n"), 0o600)
	f, _ := os.Open(path)
	defer f.Close()
	c := &compactor{r: bufio.NewReader(f)}
	if err := c.run(); err == nil {
		t.Fatal("no error for a line that stays too long")
	}
	if len(c.out) > MaxLine+ElideAbove+16 {
		t.Fatalf("built %d bytes", len(c.out))
	}
}

func TestReadBlobRejectsHostileOffsets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	if err := os.WriteFile(path, []byte("unused"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, offsets := range []string{
		"-9223372036854775808:9223372036854775807", "-2:9223372036854775807", "-1:0",
		"0:9223372036854775807", "1:0",
	} {
		t.Run(offsets, func(t *testing.T) {
			if _, err := ReadBlob(path, BlobPrefix+offsets, 64<<20); err == nil {
				t.Fatal("hostile offset accepted")
			}
		})
	}
}

// A backlog is returned in batches of bounded size, each ending on a line,
// until none is left.
func TestBacklogIsReadInBatches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.jsonl")
	line := strings.Repeat("x", 3<<20)
	if err := os.WriteFile(path, []byte(strings.Repeat(line+"\n", 5)), 0o600); err != nil {
		t.Fatal(err)
	}
	r := NewReader(path, 0)
	var got []int
	var end int64
	for {
		lines, _, err := r.Next()
		if err != nil {
			t.Fatal(err)
		}
		if len(lines) == 0 {
			break
		}
		size := 0
		for _, l := range lines {
			if string(l.Data) != line || l.End <= end {
				t.Fatalf("line ending at %d after %d", l.End, end)
			}
			size, end = size+len(l.Data), l.End
		}
		if size >= MaxBatch+MaxLine {
			t.Fatalf("a batch of %d bytes", size)
		}
		got = append(got, len(lines))
	}
	if len(got) != 2 || got[0]+got[1] != 5 || r.Offset() != int64(5*(len(line)+1)) {
		t.Fatalf("batches %v, offset %d", got, r.Offset())
	}
}
