package daemon

import (
	"fmt"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"os"
	"strings"
	"testing"
)

func historyFile(t *testing.T, lines []string) *terminalHistory {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "history")
	if err != nil {
		t.Fatal(err)
	}
	h := &terminalHistory{file: f, token: "token", offsets: []int64{0}}
	if _, err = h.Write([]byte(strings.Join(lines, "\n") + "\n")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return h
}
func TestHistoryPagingKeepsWholeUTF8LinesAndStableCursors(t *testing.T) {
	lines := make([]string, 1200)
	for i := range lines {
		lines[i] = fmt.Sprintf("%05d 中文 %s", i, strings.Repeat("宽", 500))
	}
	stream := &TermStream{history: historyFile(t, lines)}
	cursor := "token:1200"
	var got []string
	for cursor != "" {
		page, err := stream.History(&pb.TerminalHistoryRequest{RequestId: "test", Cursor: cursor})
		if err != nil || page.Error != "" {
			t.Fatalf("page failed: %v %v", page, err)
		}
		if len(page.Lines) > historyPageLines || len(strings.Join(page.Lines, "\n")) > historyPageBytes {
			t.Fatal("page exceeds budget")
		}
		got = append(append([]string{}, page.Lines...), got...)
		// Reading the same immutable cursor is idempotent (including after a timeout).
		again, _ := stream.History(&pb.TerminalHistoryRequest{RequestId: "retry", Cursor: cursor})
		if again.NextCursor != page.NextCursor || strings.Join(again.Lines, "\n") != strings.Join(page.Lines, "\n") {
			t.Fatal("retry changed page")
		}
		cursor = page.NextCursor
	}
	if strings.Join(got, "\n") != strings.Join(lines, "\n") {
		t.Fatal("pagination dropped, reordered or duplicated lines")
	}
	for _, bad := range []string{"other:1200", "token:-1", "token:1201", "token:0", "broken"} {
		page, _ := stream.History(&pb.TerminalHistoryRequest{RequestId: "bad", Cursor: bad})
		if page.Error == "" || len(page.Lines) != 0 {
			t.Errorf("accepted %q", bad)
		}
	}
}
func TestHistoryPageRejectsOversizedLineWithoutSkippingIt(t *testing.T) {
	stream := &TermStream{history: historyFile(t, []string{strings.Repeat("x", historyPageBytes+1)})}
	page, _ := stream.History(&pb.TerminalHistoryRequest{RequestId: "big", Cursor: "token:1"})
	if page.Error == "" || len(page.Lines) > 0 {
		t.Fatal("oversized line silently truncated")
	}
}
func TestHistoryCaptureWriterIsBounded(t *testing.T) {
	h := historyFile(t, []string{"small"})
	h.size = historyCaptureBytes
	if _, err := h.Write([]byte("x")); err == nil {
		t.Fatal("unbounded capture")
	}
}

func TestEmptyHistoryReturnsNoBlankPage(t *testing.T) {
	stream := &TermStream{history: historyFile(t, []string{"", " ", ""})}
	page, err := stream.History(&pb.TerminalHistoryRequest{RequestId: "empty", Cursor: "token:3"})
	if err != nil || page.Error != "" || len(page.Lines) != 0 || page.NextCursor != "" {
		t.Fatalf("empty page: %v %v", page, err)
	}
}
