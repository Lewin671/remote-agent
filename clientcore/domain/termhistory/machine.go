package termhistory

import (
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
)

// TimeoutMs is how long a single history page request waits before failing.
const TimeoutMs int64 = 10_000

// Machine tracks one terminal stream's history paging state.
type Machine struct {
	Phase    corev1.TerminalHistoryPhase
	Cursor   string
	Pending  string
	Trouble  corev1.TerminalHistoryStatus
	HasPages bool
	MaxCells int32
}

// Transition describes the side effects and incremental page delta produced by
// one state machine step.
type Transition struct {
	Changed     bool
	SendRequest bool
	RequestID   string
	Cursor      string
	StartTimer  bool
	CancelTimer bool
	ResetPages  bool
	AddedPage   *corev1.TerminalHistoryParsedPage
}

// Open begins a fresh capture from IDLE or retries a failed opening capture.
// Ignored while a request is already in flight or the reader is already open.
func (m *Machine) Open(newID func() string) Transition {
	if m.Pending != "" {
		return Transition{}
	}
	if m.Phase != corev1.TerminalHistoryPhase_TERMINAL_HISTORY_PHASE_IDLE &&
		m.Phase != corev1.TerminalHistoryPhase_TERMINAL_HISTORY_PHASE_OPENING_FAILED {
		return Transition{}
	}
	id := newID()
	m.Phase = corev1.TerminalHistoryPhase_TERMINAL_HISTORY_PHASE_OPENING
	m.Cursor = ""
	m.Pending = id
	m.Trouble = corev1.TerminalHistoryStatus_TERMINAL_HISTORY_STATUS_NONE
	m.HasPages = false
	m.MaxCells = 0
	return Transition{
		Changed:     true,
		SendRequest: true,
		RequestID:   id,
		Cursor:      "",
		StartTimer:  true,
		ResetPages:  true,
	}
}

// LoadEarlier requests the page preceding the current cursor while the reader
// is open, idle, healthy, and has more history to load.
func (m *Machine) LoadEarlier(newID func() string) Transition {
	if m.Phase != corev1.TerminalHistoryPhase_TERMINAL_HISTORY_PHASE_OPEN ||
		m.Pending != "" ||
		m.Trouble != corev1.TerminalHistoryStatus_TERMINAL_HISTORY_STATUS_NONE ||
		m.Cursor == "" {
		return Transition{}
	}
	id := newID()
	m.Pending = id
	return Transition{
		Changed:     true,
		SendRequest: true,
		RequestID:   id,
		Cursor:      m.Cursor,
		StartTimer:  true,
	}
}

// Retry retries a failed opening capture from scratch, or retries a failed
// older-page fetch using the preserved cursor.
func (m *Machine) Retry(newID func() string) Transition {
	if m.Pending != "" {
		return Transition{}
	}
	if m.Phase == corev1.TerminalHistoryPhase_TERMINAL_HISTORY_PHASE_OPENING_FAILED {
		return m.Open(newID)
	}
	if m.Phase == corev1.TerminalHistoryPhase_TERMINAL_HISTORY_PHASE_OPEN &&
		m.Trouble == corev1.TerminalHistoryStatus_TERMINAL_HISTORY_STATUS_FAILED {
		id := newID()
		m.Trouble = corev1.TerminalHistoryStatus_TERMINAL_HISTORY_STATUS_NONE
		m.Pending = id
		return Transition{
			Changed:     true,
			SendRequest: true,
			RequestID:   id,
			Cursor:      m.Cursor,
			StartTimer:  true,
		}
	}
	return Transition{}
}

// ReceivePage processes a daemon TerminalHistoryPage reply. Late replies or
// replies whose request_id does not match Pending are ignored.
func (m *Machine) ReceivePage(page *v1.TerminalHistoryPage) Transition {
	if page == nil ||
		m.Phase == corev1.TerminalHistoryPhase_TERMINAL_HISTORY_PHASE_IDLE ||
		m.Pending == "" ||
		page.RequestId != m.Pending {
		return Transition{}
	}
	m.Pending = ""

	hasError := page.ErrorCode != v1.TerminalHistoryErrorCode_TERMINAL_HISTORY_ERROR_CODE_UNSPECIFIED || page.Error != ""

	if m.Phase == corev1.TerminalHistoryPhase_TERMINAL_HISTORY_PHASE_OPENING {
		if hasError {
			// Before the reader opens, there is no capture to expire yet: any
			// error is a retryable opening failure.
			m.Phase = corev1.TerminalHistoryPhase_TERMINAL_HISTORY_PHASE_OPENING_FAILED
			m.Trouble = corev1.TerminalHistoryStatus_TERMINAL_HISTORY_STATUS_NONE
			return Transition{Changed: true, CancelTimer: true}
		}
		var parsed *corev1.TerminalHistoryParsedPage
		if len(page.Lines) > 0 {
			parsed = ParsePage(page.RequestId, page.Lines)
			m.HasPages = true
			if parsed.MaxCells > m.MaxCells {
				m.MaxCells = parsed.MaxCells
			}
		}
		m.Cursor = page.NextCursor
		if !m.HasPages && m.Cursor == "" {
			// Empty capture: do not open a blank reader.
			m.reset()
			return Transition{Changed: true, CancelTimer: true, ResetPages: true}
		}
		m.Phase = corev1.TerminalHistoryPhase_TERMINAL_HISTORY_PHASE_OPEN
		m.Trouble = corev1.TerminalHistoryStatus_TERMINAL_HISTORY_STATUS_NONE
		return Transition{Changed: true, CancelTimer: true, AddedPage: parsed}
	}

	if m.Phase == corev1.TerminalHistoryPhase_TERMINAL_HISTORY_PHASE_OPEN {
		if hasError {
			if page.ErrorCode == v1.TerminalHistoryErrorCode_TERMINAL_HISTORY_ERROR_CODE_EXPIRED {
				m.Trouble = corev1.TerminalHistoryStatus_TERMINAL_HISTORY_STATUS_EXPIRED
			} else {
				m.Trouble = corev1.TerminalHistoryStatus_TERMINAL_HISTORY_STATUS_FAILED
			}
			return Transition{Changed: true, CancelTimer: true}
		}
		m.Trouble = corev1.TerminalHistoryStatus_TERMINAL_HISTORY_STATUS_NONE
		m.Cursor = page.NextCursor
		var parsed *corev1.TerminalHistoryParsedPage
		if len(page.Lines) > 0 {
			parsed = ParsePage(page.RequestId, page.Lines)
			m.HasPages = true
			if parsed.MaxCells > m.MaxCells {
				m.MaxCells = parsed.MaxCells
			}
		}
		return Transition{Changed: true, CancelTimer: true, AddedPage: parsed}
	}

	return Transition{}
}

// Timeout marks the currently in-flight request as failed.
func (m *Machine) Timeout() Transition {
	if m.Pending == "" || m.Phase == corev1.TerminalHistoryPhase_TERMINAL_HISTORY_PHASE_IDLE {
		return Transition{}
	}
	m.Pending = ""
	if m.Phase == corev1.TerminalHistoryPhase_TERMINAL_HISTORY_PHASE_OPENING {
		m.Phase = corev1.TerminalHistoryPhase_TERMINAL_HISTORY_PHASE_OPENING_FAILED
		m.Trouble = corev1.TerminalHistoryStatus_TERMINAL_HISTORY_STATUS_NONE
	} else if m.Phase == corev1.TerminalHistoryPhase_TERMINAL_HISTORY_PHASE_OPEN {
		m.Trouble = corev1.TerminalHistoryStatus_TERMINAL_HISTORY_STATUS_FAILED
	}
	return Transition{Changed: true}
}

// Disconnected handles a dropped terminal stream. If the reader is open, loaded
// pages stay visible and no more pages can be fetched; before opening, reading
// ends cleanly.
func (m *Machine) Disconnected() Transition {
	if m.Phase == corev1.TerminalHistoryPhase_TERMINAL_HISTORY_PHASE_IDLE {
		return Transition{}
	}
	hadPending := m.Pending != ""
	m.Pending = ""
	if m.Phase == corev1.TerminalHistoryPhase_TERMINAL_HISTORY_PHASE_OPEN {
		m.Trouble = corev1.TerminalHistoryStatus_TERMINAL_HISTORY_STATUS_DISCONNECTED
		return Transition{Changed: true, CancelTimer: hadPending}
	}
	m.reset()
	return Transition{Changed: true, CancelTimer: hadPending, ResetPages: true}
}

// Close exits reading and discards all visit state.
func (m *Machine) Close() Transition {
	if m.Phase == corev1.TerminalHistoryPhase_TERMINAL_HISTORY_PHASE_IDLE &&
		m.Pending == "" &&
		m.Cursor == "" &&
		!m.HasPages &&
		m.Trouble == corev1.TerminalHistoryStatus_TERMINAL_HISTORY_STATUS_NONE &&
		m.MaxCells == 0 {
		return Transition{}
	}
	hadPending := m.Pending != ""
	m.reset()
	return Transition{Changed: true, CancelTimer: hadPending, ResetPages: true}
}

func (m *Machine) reset() {
	m.Phase = corev1.TerminalHistoryPhase_TERMINAL_HISTORY_PHASE_IDLE
	m.Cursor = ""
	m.Pending = ""
	m.Trouble = corev1.TerminalHistoryStatus_TERMINAL_HISTORY_STATUS_NONE
	m.HasPages = false
	m.MaxCells = 0
}

// Status derives the single active reader status for the current state.
func (m *Machine) Status() corev1.TerminalHistoryStatus {
	switch m.Phase {
	case corev1.TerminalHistoryPhase_TERMINAL_HISTORY_PHASE_OPENING:
		return corev1.TerminalHistoryStatus_TERMINAL_HISTORY_STATUS_OPENING
	case corev1.TerminalHistoryPhase_TERMINAL_HISTORY_PHASE_OPENING_FAILED:
		return corev1.TerminalHistoryStatus_TERMINAL_HISTORY_STATUS_OPENING_FAILED
	case corev1.TerminalHistoryPhase_TERMINAL_HISTORY_PHASE_OPEN:
		if m.Pending != "" {
			return corev1.TerminalHistoryStatus_TERMINAL_HISTORY_STATUS_LOADING_EARLIER
		}
		if m.Trouble != corev1.TerminalHistoryStatus_TERMINAL_HISTORY_STATUS_NONE {
			return m.Trouble
		}
		if m.Cursor == "" && m.HasPages {
			return corev1.TerminalHistoryStatus_TERMINAL_HISTORY_STATUS_START
		}
		return corev1.TerminalHistoryStatus_TERMINAL_HISTORY_STATUS_NONE
	default:
		return corev1.TerminalHistoryStatus_TERMINAL_HISTORY_STATUS_NONE
	}
}

// CanRetry reports whether the user can retry the failed request.
func (m *Machine) CanRetry() bool {
	st := m.Status()
	return st == corev1.TerminalHistoryStatus_TERMINAL_HISTORY_STATUS_OPENING_FAILED ||
		st == corev1.TerminalHistoryStatus_TERMINAL_HISTORY_STATUS_FAILED
}

// CanLoadEarlier reports whether scrolling near the top should trigger another page fetch.
func (m *Machine) CanLoadEarlier() bool {
	return m.Phase == corev1.TerminalHistoryPhase_TERMINAL_HISTORY_PHASE_OPEN &&
		m.Pending == "" &&
		m.Trouble == corev1.TerminalHistoryStatus_TERMINAL_HISTORY_STATUS_NONE &&
		m.Cursor != ""
}
