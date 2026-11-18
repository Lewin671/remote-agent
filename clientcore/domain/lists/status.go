package lists

import (
	"cmp"
	"unicode/utf16"

	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/gen/timestamppb"
	"github.com/Lewin671/remote-agent/clientcore/projection"
)

// compareUTF16 compares two strings by UTF-16 code units, matching JS/Kotlin/Swift ordering.
func compareUTF16(a, b string) int {
	if a == b {
		return 0
	}
	ia, ib := 0, 0
	for ia < len(a) && ib < len(b) {
		ra, sa := decodeRuneFast(a[ia:])
		rb, sb := decodeRuneFast(b[ib:])
		if ra != rb {
			if ra >= 0x10000 || rb >= 0x10000 {
				return compareUTF16Slices(utf16.Encode([]rune(a[ia:])), utf16.Encode([]rune(b[ib:])))
			}
			return cmp.Compare(ra, rb)
		}
		ia += sa
		ib += sb
	}
	return cmp.Compare(len(a)-ia, len(b)-ib)
}

func decodeRuneFast(s string) (rune, int) {
	if s[0] < 0x80 {
		return rune(s[0]), 1
	}
	for _, r := range s {
		return r, len(string(r))
	}
	return 0, 0
}

func compareUTF16Slices(a, b []uint16) int {
	n := min(len(a), len(b))
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return cmp.Compare(a[i], b[i])
		}
	}
	return cmp.Compare(len(a), len(b))
}

func tsMs(ts *timestamppb.Timestamp) int64 {
	if ts == nil {
		return 0
	}
	return ts.Seconds*1000 + int64(ts.Nanos)/1_000_000
}

// WorldUnlinked reports whether the relay link is currently unreachable.
func WorldUnlinked(w *World) bool {
	if w == nil {
		return false
	}
	if w.Unlinked != nil {
		return *w.Unlinked
	}
	return w.Connection == ConnNoNetwork ||
		w.Connection == ConnOffline ||
		(w.Connection == ConnReconnecting && !w.LiveLoaded)
}

// MoreSessions reports whether the server may hold older ended sessions not yet fetched.
func MoreSessions(w *World) bool {
	if w == nil {
		return true
	}
	return !w.SessionsListed || w.HasMoreSessions || w.SessionsNext != nil || w.SessionsCursor != ""
}

// MachineReachable reports whether a machine can currently be reached.
func MachineReachable(m *Machine) bool {
	if m == nil {
		return false
	}
	if m.Reachable != nil {
		return *m.Reachable
	}
	return m.Online
}

// MachineUnlinked reports whether a machine's peer state is unlinked from the server.
func MachineUnlinked(m *Machine, w *World) bool {
	if m != nil && m.Unlinked != nil {
		return *m.Unlinked
	}
	return WorldUnlinked(w)
}

// MachineProtocolVersion returns the machine's effective protocol version, or 0 if unknown.
func MachineProtocolVersion(m *Machine) uint32 {
	if m == nil {
		return 0
	}
	if m.ProtocolVersion > 0 {
		return m.ProtocolVersion
	}
	if m.Meta != nil {
		return m.Meta.ProtocolVersion
	}
	return 0
}

// ProtocolMismatchDirection returns -1 if the daemon is older than clientVer,
// 1 if the client is older than the daemon, or 0 when matching or unknown.
func ProtocolMismatchDirection(m *Machine, clientVer uint32) int32 {
	if m != nil {
		if m.DaemonOutOfDate {
			return -1
		}
		if m.ClientOutOfDate {
			return 1
		}
	}
	if clientVer == 0 {
		return 0
	}
	theirs := MachineProtocolVersion(m)
	if theirs == 0 {
		return 0
	}
	if theirs < clientVer {
		return -1
	}
	if theirs > clientVer {
		return 1
	}
	return 0
}

// Exited reports whether the session has exited.
func Exited(s *Session) bool {
	return s != nil && s.Info != nil && s.Info.Status == v1.SessionStatus_SESSION_STATUS_EXITED
}

// Forked reports whether the session was forked from another session.
func Forked(s *Session) bool {
	return s != nil && s.Info != nil && s.Info.ForkedFrom != ""
}

// SessionAgent returns the effective CLI for s, defaulting to Codex.
func SessionAgent(s *Session) v1.Agent {
	if s == nil || s.Info == nil {
		return DefaultAgent
	}
	return EffectiveAgent(s.Info.Agent)
}

// StatusOf computes the highest-priority display status of s given machine reachability (nil = ignore machine offline).
func StatusOf(s *Session, machine *Machine) SessionDisplayStatus {
	if s == nil || s.Info == nil {
		return StatusLoading
	}
	i := s.Info
	if i.Status == v1.SessionStatus_SESSION_STATUS_EXITED {
		return StatusEnded
	}
	if machine != nil && !machine.ReachabilityUnknown && !MachineReachable(machine) {
		return StatusOffline
	}
	if i.PendingApprovals > 0 || s.Pending.HasApproval {
		return StatusApproval
	}
	if i.PendingQuestions > 0 || s.Pending.HasQuestion {
		return StatusQuestion
	}
	if i.PlanOpen || s.Pending.HasPlan {
		return StatusPlan
	}
	if i.Status == v1.SessionStatus_SESSION_STATUS_BLOCKED || s.Pending.HasPopup {
		return StatusPopup
	}
	if i.Status == v1.SessionStatus_SESSION_STATUS_RUNNING {
		return StatusRunning
	}
	if i.Status == v1.SessionStatus_SESSION_STATUS_STARTING {
		return StatusStarting
	}
	return StatusIdle
}

// ShownStatus overlays StatusUnlinked when the server is out of reach for non-exited, non-loading sessions.
func ShownStatus(s *Session, machine *Machine, unlinked bool) SessionDisplayStatus {
	st := StatusOf(s, machine)
	if unlinked && st != StatusEnded && st != StatusLoading {
		return StatusUnlinked
	}
	return st
}

// ToneOf maps a SessionDisplayStatus to its visual tone.
func ToneOf(st SessionDisplayStatus) SessionTone {
	switch st {
	case StatusApproval, StatusQuestion, StatusPlan, StatusPopup:
		return ToneWait
	case StatusRunning, StatusStarting:
		return ToneRun
	case StatusIdle:
		return ToneIdle
	default:
		return ToneOff
	}
}

// NeedsAttention reports whether s is waiting for user attention, regardless of machine reachability.
func NeedsAttention(s *Session) bool {
	return ToneOf(StatusOf(s, nil)) == ToneWait
}

// CreatedMs returns the session creation timestamp in Unix milliseconds, or 0 if unset.
func CreatedMs(s *Session) int64 {
	if s == nil || s.Info == nil {
		return 0
	}
	return tsMs(s.Info.CreatedAt)
}

// EndedMs returns the session exit timestamp in Unix milliseconds, falling back to CreatedMs.
func EndedMs(s *Session) int64 {
	if s != nil && s.Info != nil && s.Info.EndedAt != nil {
		return tsMs(s.Info.EndedAt)
	}
	return CreatedMs(s)
}

// AttentionMs returns when the current attention prompt began in Unix milliseconds, falling back to CreatedMs.
func AttentionMs(s *Session) int64 {
	if s != nil && s.Info != nil && s.Info.AttentionSince != nil {
		return tsMs(s.Info.AttentionSince)
	}
	return CreatedMs(s)
}

// CompareLive orders non-ended sessions newest-created first, then by ID ascending.
func CompareLive(a, b *Session) int {
	if c := cmp.Compare(CreatedMs(b), CreatedMs(a)); c != 0 {
		return c
	}
	return compareUTF16(a.ID, b.ID)
}

// CompareEnded orders ended sessions latest-ended first, then by ID ascending.
func CompareEnded(a, b *Session) int {
	if c := cmp.Compare(EndedMs(b), EndedMs(a)); c != 0 {
		return c
	}
	return compareUTF16(a.ID, b.ID)
}

// CompareSession orders live sessions before ended sessions, applying CompareLive and CompareEnded within each group.
func CompareSession(a, b *Session) int {
	ea, eb := Exited(a), Exited(b)
	if ea != eb {
		if !ea {
			return -1
		}
		return 1
	}
	if ea {
		return CompareEnded(a, b)
	}
	return CompareLive(a, b)
}

// CompareAttention orders waiting sessions by attention category priority, then oldest wait first, then ID ascending.
func CompareAttention(a, b *Session) int {
	sa, sb := StatusOf(a, nil), StatusOf(b, nil)
	if sa != sb {
		return cmp.Compare(sa, sb)
	}
	if c := cmp.Compare(AttentionMs(a), AttentionMs(b)); c != 0 {
		return c
	}
	return compareUTF16(a.ID, b.ID)
}

// ContextPercent computes integer context window occupancy (clamped to 100), or nil if unknown.
func ContextPercent(s *Session) *int32 {
	if s == nil || s.Info == nil || s.Info.ContextWindow <= 0 {
		return nil
	}
	pct := (s.Info.ContextTokens * 100) / s.Info.ContextWindow
	if pct > 100 {
		pct = 100
	}
	if pct < 0 {
		pct = 0
	}
	v := int32(pct)
	return &v
}

// SessionMode resolves the effective client ApprovalMode (or UNSPECIFIED + raw mode for CLI-specific modes).
func SessionMode(s *Session) (v1.ApprovalMode, string) {
	if s == nil || s.Info == nil || s.Info.Status == v1.SessionStatus_SESSION_STATUS_STARTING {
		return v1.ApprovalMode_APPROVAL_MODE_UNSPECIFIED, ""
	}
	i := s.Info
	if m := projection.ModeOf(i.Mode); m != v1.ApprovalMode_APPROVAL_MODE_UNSPECIFIED {
		return m, ""
	}
	if i.PermissionMode != "" {
		if m := projection.PermissionMode(i.PermissionMode); m != v1.ApprovalMode_APPROVAL_MODE_UNSPECIFIED {
			return m, ""
		}
		return v1.ApprovalMode_APPROVAL_MODE_UNSPECIFIED, i.PermissionMode
	}
	return projection.ContextMode(i.ApprovalsReviewer, i.ApprovalPolicy, i.Sandbox, i.CollaborationMode), ""
}
