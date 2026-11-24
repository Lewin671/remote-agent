package termhistory

import (
	"fmt"
	"testing"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
)

func seqIDs() func() string {
	n := 0
	return func() string {
		n++
		return fmt.Sprintf("req-%d", n)
	}
}

func TestMachineOpenLoadEarlierCloseAndStaleReplies(t *testing.T) {
	var m Machine
	nextID := seqIDs()

	tr := m.Open(nextID)
	if !tr.Changed || !tr.SendRequest || !tr.StartTimer || !tr.ResetPages || tr.RequestID != "req-1" || tr.Cursor != "" {
		t.Fatalf("unexpected open transition: %#v", tr)
	}
	if m.Phase != corev1.TerminalHistoryPhase_TERMINAL_HISTORY_PHASE_OPENING || m.Status() != corev1.TerminalHistoryStatus_TERMINAL_HISTORY_STATUS_OPENING {
		t.Fatalf("unexpected state after open: phase=%v status=%v", m.Phase, m.Status())
	}

	// Duplicate open/load while in flight is ignored.
	if m.Open(nextID).Changed || m.LoadEarlier(nextID).Changed {
		t.Fatal("expected in-flight requests to be ignored")
	}

	// First page arrives with next_cursor.
	tr = m.ReceivePage(&v1.TerminalHistoryPage{
		RequestId:  "req-1",
		Lines:      []string{"new \x1b[31m中文\x1b[0m"},
		NextCursor: "cap:200",
	})
	if !tr.Changed || !tr.CancelTimer || tr.AddedPage == nil || tr.AddedPage.MaxCells != 8 {
		t.Fatalf("unexpected first page transition: %#v", tr)
	}
	if m.Phase != corev1.TerminalHistoryPhase_TERMINAL_HISTORY_PHASE_OPEN || !m.CanLoadEarlier() || m.Status() != corev1.TerminalHistoryStatus_TERMINAL_HISTORY_STATUS_NONE {
		t.Fatalf("unexpected open state: phase=%v canLoad=%v status=%v", m.Phase, m.CanLoadEarlier(), m.Status())
	}

	// Load earlier uses cursor.
	tr = m.LoadEarlier(nextID)
	if !tr.Changed || !tr.SendRequest || tr.Cursor != "cap:200" || tr.RequestID != "req-2" {
		t.Fatalf("unexpected loadEarlier transition: %#v", tr)
	}
	if m.Status() != corev1.TerminalHistoryStatus_TERMINAL_HISTORY_STATUS_LOADING_EARLIER || m.CanLoadEarlier() {
		t.Fatalf("unexpected loading status: %v", m.Status())
	}

	// Second (oldest) page arrives without cursor -> START status.
	tr = m.ReceivePage(&v1.TerminalHistoryPage{
		RequestId: "req-2",
		Lines:     []string{"1234567890"},
	})
	if !tr.Changed || !tr.CancelTimer || tr.AddedPage == nil || m.MaxCells != 10 {
		t.Fatalf("unexpected second page transition: %#v maxCells=%d", tr, m.MaxCells)
	}
	if m.Status() != corev1.TerminalHistoryStatus_TERMINAL_HISTORY_STATUS_START || m.CanLoadEarlier() {
		t.Fatalf("expected START status, got %v", m.Status())
	}

	// Load earlier at start is ignored.
	if m.LoadEarlier(nextID).Changed {
		t.Fatal("expected loadEarlier at start to be ignored")
	}

	// Close resets everything and subsequent stale reply is ignored.
	tr = m.Close()
	if !tr.Changed || !tr.ResetPages || m.Phase != corev1.TerminalHistoryPhase_TERMINAL_HISTORY_PHASE_IDLE {
		t.Fatalf("unexpected close transition: %#v", tr)
	}
	_ = m.Open(nextID) // req-3
	if m.ReceivePage(&v1.TerminalHistoryPage{RequestId: "req-2", Lines: []string{"stale"}}).Changed {
		t.Fatal("stale reply must be ignored")
	}
}

func TestMachineEmptyCaptureRules(t *testing.T) {
	t.Run("empty lines and empty cursor returns to idle", func(t *testing.T) {
		var m Machine
		nextID := seqIDs()
		m.Open(nextID)
		tr := m.ReceivePage(&v1.TerminalHistoryPage{RequestId: "req-1"})
		if !tr.Changed || !tr.CancelTimer || !tr.ResetPages || tr.AddedPage != nil {
			t.Fatalf("unexpected empty capture transition: %#v", tr)
		}
		if m.Phase != corev1.TerminalHistoryPhase_TERMINAL_HISTORY_PHASE_IDLE {
			t.Fatalf("expected IDLE, got %v", m.Phase)
		}
	})

	t.Run("empty lines with non-empty cursor still opens and keeps cursor", func(t *testing.T) {
		var m Machine
		nextID := seqIDs()
		m.Open(nextID)
		tr := m.ReceivePage(&v1.TerminalHistoryPage{RequestId: "req-1", NextCursor: "cap:100"})
		if !tr.Changed || !tr.CancelTimer || tr.AddedPage != nil {
			t.Fatalf("unexpected transition: %#v", tr)
		}
		if m.Phase != corev1.TerminalHistoryPhase_TERMINAL_HISTORY_PHASE_OPEN || !m.CanLoadEarlier() || m.Cursor != "cap:100" {
			t.Fatalf("expected OPEN with cursor cap:100, got phase=%v cursor=%q canLoad=%v", m.Phase, m.Cursor, m.CanLoadEarlier())
		}
	})
}

func TestMachineFailuresTimeoutsExpiredAndDisconnect(t *testing.T) {
	var m Machine
	nextID := seqIDs()

	// Opening error (even with EXPIRED code) stays OPENING_FAILED (never EXPIRED).
	m.Open(nextID) // req-1
	tr := m.ReceivePage(&v1.TerminalHistoryPage{
		RequestId: "req-1",
		Error:     "history expired; reopen history",
		ErrorCode: v1.TerminalHistoryErrorCode_TERMINAL_HISTORY_ERROR_CODE_EXPIRED,
	})
	if !tr.Changed || !tr.CancelTimer {
		t.Fatalf("unexpected transition: %#v", tr)
	}
	if m.Phase != corev1.TerminalHistoryPhase_TERMINAL_HISTORY_PHASE_OPENING_FAILED || !m.CanRetry() {
		t.Fatalf("expected OPENING_FAILED retryable, got phase=%v status=%v canRetry=%v", m.Phase, m.Status(), m.CanRetry())
	}

	// Retry from OPENING_FAILED starts fresh capture with empty cursor.
	tr = m.Retry(nextID) // req-2
	if !tr.Changed || !tr.SendRequest || tr.Cursor != "" || tr.RequestID != "req-2" {
		t.Fatalf("unexpected retry transition: %#v", tr)
	}

	// Timeout during opening -> OPENING_FAILED.
	tr = m.Timeout()
	if !tr.Changed || m.Phase != corev1.TerminalHistoryPhase_TERMINAL_HISTORY_PHASE_OPENING_FAILED {
		t.Fatalf("expected OPENING_FAILED on timeout, got %v", m.Phase)
	}

	// Disconnect during OPENING_FAILED resets to IDLE.
	tr = m.Disconnected()
	if !tr.Changed || !tr.ResetPages || m.Phase != corev1.TerminalHistoryPhase_TERMINAL_HISTORY_PHASE_IDLE {
		t.Fatalf("expected IDLE after disconnect during opening failure: %#v", tr)
	}

	// Open succeeds, then older page times out, late reply ignored, retry succeeds, then expired error (case-insensitive).
	m.Open(nextID) // req-3
	m.ReceivePage(&v1.TerminalHistoryPage{RequestId: "req-3", Lines: []string{"latest"}, NextCursor: "cap:50"})
	m.LoadEarlier(nextID) // req-4
	tr = m.Timeout()
	if !tr.Changed || m.Status() != corev1.TerminalHistoryStatus_TERMINAL_HISTORY_STATUS_FAILED || !m.CanRetry() {
		t.Fatalf("expected FAILED after timeout, got %v", m.Status())
	}
	if m.ReceivePage(&v1.TerminalHistoryPage{RequestId: "req-4", Lines: []string{"late"}}).Changed {
		t.Fatal("late reply after timeout must be ignored")
	}

	tr = m.Retry(nextID) // req-5
	if !tr.Changed || tr.Cursor != "cap:50" || tr.RequestID != "req-5" {
		t.Fatalf("expected retry to reuse cursor cap:50, got %#v", tr)
	}
	// Unspecified error_code (e.g. older daemon) or FAILED code with text mentioning "expired"
	// stays retryable FAILED (no string matching).
	tr = m.ReceivePage(&v1.TerminalHistoryPage{
		RequestId: "req-5",
		Error:     "history expired; reopen history",
		ErrorCode: v1.TerminalHistoryErrorCode_TERMINAL_HISTORY_ERROR_CODE_UNSPECIFIED,
	})
	if !tr.Changed || m.Status() != corev1.TerminalHistoryStatus_TERMINAL_HISTORY_STATUS_FAILED || !m.CanRetry() {
		t.Fatalf("expected FAILED when error_code == UNSPECIFIED, got status=%v canRetry=%v", m.Status(), m.CanRetry())
	}

	tr = m.Retry(nextID) // req-6
	if !tr.Changed || tr.Cursor != "cap:50" || tr.RequestID != "req-6" {
		t.Fatalf("expected retry to reuse cursor cap:50, got %#v", tr)
	}
	tr = m.ReceivePage(&v1.TerminalHistoryPage{
		RequestId: "req-6",
		Error:     "History EXPIRED; reopen",
		ErrorCode: v1.TerminalHistoryErrorCode_TERMINAL_HISTORY_ERROR_CODE_FAILED,
	})
	if !tr.Changed || m.Status() != corev1.TerminalHistoryStatus_TERMINAL_HISTORY_STATUS_FAILED || !m.CanRetry() {
		t.Fatalf("expected FAILED when error_code == FAILED, got status=%v canRetry=%v", m.Status(), m.CanRetry())
	}

	// Structured EXPIRED error_code (even with empty error message) transitions to non-retryable EXPIRED.
	tr = m.Retry(nextID) // req-7
	if !tr.Changed || tr.Cursor != "cap:50" || tr.RequestID != "req-7" {
		t.Fatalf("expected retry to reuse cursor cap:50, got %#v", tr)
	}
	tr = m.ReceivePage(&v1.TerminalHistoryPage{
		RequestId: "req-7",
		ErrorCode: v1.TerminalHistoryErrorCode_TERMINAL_HISTORY_ERROR_CODE_EXPIRED,
	})
	if !tr.Changed || m.Status() != corev1.TerminalHistoryStatus_TERMINAL_HISTORY_STATUS_EXPIRED || m.CanRetry() || m.CanLoadEarlier() {
		t.Fatalf("expected EXPIRED non-retryable, got status=%v canRetry=%v", m.Status(), m.CanRetry())
	}
	if m.Retry(nextID).Changed {
		t.Fatal("EXPIRED must not retry")
	}

	// Disconnect while OPEN keeps reader open with DISCONNECTED status.
	tr = m.Disconnected()
	if !tr.Changed || tr.ResetPages || m.Phase != corev1.TerminalHistoryPhase_TERMINAL_HISTORY_PHASE_OPEN ||
		m.Status() != corev1.TerminalHistoryStatus_TERMINAL_HISTORY_STATUS_DISCONNECTED {
		t.Fatalf("expected OPEN+DISCONNECTED without resetting pages, got tr=%#v phase=%v status=%v", tr, m.Phase, m.Status())
	}
}
