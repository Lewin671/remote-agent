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
	for _, tc := range []struct {
		cursor string
		want   pb.TerminalHistoryErrorCode
	}{
		{"other:1200", pb.TerminalHistoryErrorCode_TERMINAL_HISTORY_ERROR_CODE_EXPIRED},
		{"broken", pb.TerminalHistoryErrorCode_TERMINAL_HISTORY_ERROR_CODE_EXPIRED},
		{"token:-1", pb.TerminalHistoryErrorCode_TERMINAL_HISTORY_ERROR_CODE_FAILED},
		{"token:1201", pb.TerminalHistoryErrorCode_TERMINAL_HISTORY_ERROR_CODE_FAILED},
		{"token:0", pb.TerminalHistoryErrorCode_TERMINAL_HISTORY_ERROR_CODE_FAILED},
	} {
		page, _ := stream.History(&pb.TerminalHistoryRequest{RequestId: "bad", Cursor: tc.cursor})
		if page.Error == "" || page.ErrorCode != tc.want || len(page.Lines) != 0 {
			t.Errorf("cursor %q: got code=%v err=%q lines=%d, want code=%v", tc.cursor, page.ErrorCode, page.Error, len(page.Lines), tc.want)
		}
	}
}
func TestHistoryPageRejectsOversizedLineWithoutSkippingIt(t *testing.T) {
	stream := &TermStream{history: historyFile(t, []string{strings.Repeat("x", historyPageBytes+1)})}
	page, _ := stream.History(&pb.TerminalHistoryRequest{RequestId: "big", Cursor: "token:1"})
	if page.Error == "" || page.ErrorCode != pb.TerminalHistoryErrorCode_TERMINAL_HISTORY_ERROR_CODE_FAILED || len(page.Lines) > 0 {
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
