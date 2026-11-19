package machine

import (
	"sort"
	"strings"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

const (
	IdleCloseDefaultMinutes int32 = 360
	MaxClientNameLength     int32 = 40
	DefaultUploadQuotaBytes int64 = 1024 * 1024 * 1024
)

var (
	baseApprovalTimeouts = []int32{120, 300, 600, 1800}
	baseIdleCloseMinutes = []int32{-1, 60, 120, IdleCloseDefaultMinutes, 720, 1440}
	baseUploadQuotas     = []int64{256 * mibBytes, 1 * gibBytes, 5 * gibBytes, 10 * gibBytes}
)

// IdleCloseValue normalizes 0 to the daemon default of 360 minutes.
func IdleCloseValue(minutes int32) int32 {
	if minutes == 0 {
		return IdleCloseDefaultMinutes
	}
	return minutes
}

// IdleCloseLabel formats an idle-close option label.
func IdleCloseLabel(minutes int32) *corev1.Text {
	m := IdleCloseValue(minutes)
	if m < 0 {
		return text.Msg(text.KeySettingsIdleCloseOff)
	}
	if m%60 == 0 {
		return text.Msg(text.KeyQuotaHr, text.Int("count", int64(m/60)))
	}
	return text.Msg(text.KeyQuotaMin, text.Int("count", int64(m)))
}

// BuildIdleCloseChoices builds sorted idle-close choices, preserving custom non-standard values.
func BuildIdleCloseChoices(current int32) []*corev1.IdleCloseChoiceView {
	cur := IdleCloseValue(current)
	vals := append([]int32(nil), baseIdleCloseMinutes...)
	found := false
	for _, v := range vals {
		if v == cur {
			found = true
			break
		}
	}
	if !found {
		vals = append(vals, cur)
		sort.Slice(vals, func(i, j int) bool { return vals[i] < vals[j] })
	}
	out := make([]*corev1.IdleCloseChoiceView, 0, len(vals))
	for _, v := range vals {
		lbl := IdleCloseLabel(v)
		if v == IdleCloseDefaultMinutes {
			lbl = text.JoinLiteral(" ", lbl, text.Msg(text.KeySettingsDefault))
		}
		out = append(out, &corev1.IdleCloseChoiceView{
			Minutes:  v,
			Label:    lbl,
			Selected: v == cur,
		})
	}
	return out
}

// TimeoutLabel formats an approval timeout duration in seconds.
func TimeoutLabel(seconds int32) *corev1.Text {
	if seconds <= 0 {
		return text.Plain("—")
	}
	if seconds%60 == 0 {
		return text.Msg(text.KeyQuotaMin, text.Int("count", int64(seconds/60)))
	}
	return text.Msg(text.KeyTimeDurationSeconds, text.Int("count", int64(seconds)))
}

// BuildTimeoutChoices builds approval timeout options [2, 5, 10, 30]m plus any custom active value.
func BuildTimeoutChoices(current int32) []*corev1.TimeoutChoiceView {
	vals := append([]int32(nil), baseApprovalTimeouts...)
	if current > 0 {
		found := false
		for _, v := range vals {
			if v == current {
				found = true
				break
			}
		}
		if !found {
			vals = append(vals, current)
			sort.Slice(vals, func(i, j int) bool { return vals[i] < vals[j] })
		}
	}
	out := make([]*corev1.TimeoutChoiceView, 0, len(vals))
	for _, v := range vals {
		out = append(out, &corev1.TimeoutChoiceView{
			Seconds:  v,
			Label:    TimeoutLabel(v),
			Selected: v == current,
		})
	}
	return out
}

// BuildImageQuotaChoices builds attachment storage quota options [256MiB, 1GiB, 5GiB, 10GiB] plus custom.
func BuildImageQuotaChoices(current int64) []*corev1.QuotaSizeChoiceView {
	vals := append([]int64(nil), baseUploadQuotas...)
	if current > 0 {
		found := false
		for _, v := range vals {
			if v == current {
				found = true
				break
			}
		}
		if !found {
			vals = append(vals, current)
			sort.Slice(vals, func(i, j int) bool { return vals[i] < vals[j] })
		}
	}
	out := make([]*corev1.QuotaSizeChoiceView, 0, len(vals))
	for _, v := range vals {
		out = append(out, &corev1.QuotaSizeChoiceView{
			Bytes:    v,
			Label:    FormatBinarySize(uint64(v)),
			Selected: v == current,
		})
	}
	return out
}

// ShortID returns "…" + last 4 characters of an ID, or the ID itself if <= 4 bytes.
func ShortID(id string) string {
	r := []rune(id)
	if len(r) <= 4 {
		return id
	}
	return "…" + string(r[len(r)-4:])
}

// ShortMachineID returns "…" + last 8 characters of a machine ID, or the ID itself if <= 8 runes.
func ShortMachineID(id string) string {
	r := []rune(id)
	if len(r) <= 8 {
		return id
	}
	return "…" + string(r[len(r)-8:])
}

// DefaultClientName returns the hardware/browser description fallback for a client device.
func DefaultClientName(d *corev1.SyncDeviceView) string {
	if d != nil && d.Meta != nil && strings.TrimSpace(d.Meta.Description) != "" {
		return d.Meta.Description
	}
	return ""
}

// ClientName returns custom name, else default hardware description, else empty string (callers use settings.unnamed).
func ClientName(d *corev1.SyncDeviceView) string {
	if d != nil && d.Meta != nil && strings.TrimSpace(d.Meta.Name) != "" {
		return strings.TrimSpace(d.Meta.Name)
	}
	return DefaultClientName(d)
}

// SortClients orders clients: self first, then LastSeenMs desc, CreatedMs desc, DeviceId asc.
func SortClients(devices []*corev1.SyncDeviceView, selfID string) []*corev1.SyncDeviceView {
	out := make([]*corev1.SyncDeviceView, 0, len(devices))
	for _, d := range devices {
		if d != nil {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		aSelf := a.DeviceId == selfID
		bSelf := b.DeviceId == selfID
		if aSelf != bSelf {
			return aSelf
		}
		if a.LastSeenMs != b.LastSeenMs {
			return a.LastSeenMs > b.LastSeenMs
		}
		if a.CreatedMs != b.CreatedMs {
			return a.CreatedMs > b.CreatedMs
		}
		return a.DeviceId < b.DeviceId
	})
	return out
}

// AuthorityName resolves a display name for the managing authority device.
func AuthorityName(syncState *corev1.SyncStateView, machines []*lists.Machine) string {
	if syncState == nil {
		return ""
	}
	authID := syncState.AuthorityDeviceId
	for _, d := range syncState.Devices {
		if d != nil && d.DeviceId == authID {
			if n := ClientName(d); n != "" {
				return n
			}
		}
	}
	for _, m := range machines {
		if m != nil && m.ID == authID {
			if n := lists.MachineName(m, ""); n != "" {
				return n
			}
		}
	}
	if syncState.ManagerName != nil && syncState.ManagerName.Literal != "" {
		return syncState.ManagerName.Literal
	}
	return ShortID(authID)
}

// MachineProjectEntry represents one project directory held or reported on a machine.
type MachineProjectEntry struct {
	Path         string
	Name         string
	LastActiveMs int64
}

// CollectMachineProjects collects and sorts projects associated with a machine from daemon meta + held sessions.
func CollectMachineProjects(m *lists.Machine, sessions []*lists.Session) []MachineProjectEntry {
	byPath := make(map[string]MachineProjectEntry)
	if m != nil && m.Meta != nil {
		for _, p := range m.Meta.Projects {
			if p == nil || p.Path == "" {
				continue
			}
			ts := p.LatestCreatedMs
			if p.LatestEndedMs > ts {
				ts = p.LatestEndedMs
			}
			cur := byPath[p.Path]
			if cur.Path == "" || ts > cur.LastActiveMs {
				byPath[p.Path] = MachineProjectEntry{
					Path:         p.Path,
					Name:         lists.ProjectName(p.Path),
					LastActiveMs: ts,
				}
			}
		}
	}
	if m != nil {
		for _, s := range sessions {
			if s == nil || s.MachineID != m.ID || s.Info == nil || s.Info.Cwd == "" {
				continue
			}
			cwd := s.Info.Cwd
			ts := lists.CreatedMs(s)
			if e := lists.EndedMs(s); e > ts {
				ts = e
			}
			cur := byPath[cwd]
			if cur.Path == "" || ts > cur.LastActiveMs {
				byPath[cwd] = MachineProjectEntry{
					Path:         cwd,
					Name:         lists.ProjectName(cwd),
					LastActiveMs: ts,
				}
			}
		}
	}
	out := make([]MachineProjectEntry, 0, len(byPath))
	for _, v := range byPath {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastActiveMs != out[j].LastActiveMs {
			return out[i].LastActiveMs > out[j].LastActiveMs
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// BuildCleanupPrompt appends machine_status.cleanup_prompt to an existing draft if not already included.
func BuildCleanupPrompt(existing, prompt string) string {
	trimmed := strings.TrimSpace(existing)
	if trimmed == "" {
		return prompt
	}
	if strings.Contains(trimmed, prompt) {
		return existing
	}
	return existing + "\n\n" + prompt
}
