package security

import (
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/shared/devicesec"
)

// ValidateAndDecodeBody unpacks p per h.Compression, rejects unknown fields and
// wrong types recursively, checks purpose-specific header bindings, and returns
// the decoded v1 protobuf message (or raw 32-byte seed for SessionKey).
//
//go:noinline
func ValidateAndDecodeBody(h devicesec.Header, p []byte) (any, []byte, error) {
	plain, err := devicesec.Unpack(h, p)
	if err != nil {
		return nil, nil, err
	}
	switch h.Purpose {
	case devicesec.Event:
		var m v1.Event
		if m.Unmarshal(plain) != nil || m.HasUnknown() {
			return nil, nil, devicesec.ErrInvalid
		}
		if m.SessionId != h.Session || m.Seq != h.Seq || m.Kind == nil {
			return nil, nil, devicesec.ErrInvalid
		}
		return &m, plain, nil
	case devicesec.SessionMeta:
		var m v1.Session
		if m.Unmarshal(plain) != nil || m.HasUnknown() || m.Id != h.Session {
			return nil, nil, devicesec.ErrInvalid
		}
		return &m, plain, nil
	case devicesec.SessionKey:
		if len(plain) != 32 {
			return nil, nil, devicesec.ErrInvalid
		}
		return plain, plain, nil
	case devicesec.MachineMeta:
		var m v1.MachineMeta
		if m.Unmarshal(plain) != nil || m.HasUnknown() {
			return nil, nil, devicesec.ErrInvalid
		}
		return &m, plain, nil
	case devicesec.Command:
		var m v1.Command
		if m.Unmarshal(plain) != nil || m.HasUnknown() || m.Kind == nil {
			return nil, nil, devicesec.ErrInvalid
		}
		return &m, plain, nil
	case devicesec.Result:
		var m v1.CommandResult
		if m.Unmarshal(plain) != nil || m.HasUnknown() {
			return nil, nil, devicesec.ErrInvalid
		}
		return &m, plain, nil
	case devicesec.TerminalInput, devicesec.TerminalResize, devicesec.TerminalHistory, devicesec.TerminalOutput:
		var m v1.TerminalFrame
		if m.Unmarshal(plain) != nil || m.HasUnknown() {
			return nil, nil, devicesec.ErrInvalid
		}
		switch h.Purpose {
		case devicesec.TerminalInput:
			if _, ok := m.Kind.(*v1.TerminalFrame_Input); !ok {
				return nil, nil, devicesec.ErrInvalid
			}
		case devicesec.TerminalResize:
			if _, ok := m.Kind.(*v1.TerminalFrame_Resize); !ok {
				return nil, nil, devicesec.ErrInvalid
			}
		case devicesec.TerminalHistory:
			if _, ok := m.Kind.(*v1.TerminalFrame_HistoryRequest); !ok {
				return nil, nil, devicesec.ErrInvalid
			}
		case devicesec.TerminalOutput:
			switch m.Kind.(type) {
			case *v1.TerminalFrame_Snapshot, *v1.TerminalFrame_Output, *v1.TerminalFrame_HistoryPage, *v1.TerminalFrame_Closed:
			default:
				return nil, nil, devicesec.ErrInvalid
			}
		}
		return &m, plain, nil
	case devicesec.Notify:
		var m v1.NotifyContent
		if m.Unmarshal(plain) != nil || m.HasUnknown() {
			return nil, nil, devicesec.ErrInvalid
		}
		if m.MachineId != h.Machine || m.SessionId != h.Session ||
			(m.Kind != v1.NotifyKind_NOTIFY_KIND_ATTENTION &&
				m.Kind != v1.NotifyKind_NOTIFY_KIND_TURN_ENDED &&
				m.Kind != v1.NotifyKind_NOTIFY_KIND_SCHEDULED &&
				m.Kind != v1.NotifyKind_NOTIFY_KIND_MACHINE_DISK) {
			return nil, nil, devicesec.ErrInvalid
		}
		if m.Kind == v1.NotifyKind_NOTIFY_KIND_MACHINE_DISK {
			if h.Session != "" || m.DiskStatus == nil || m.MachineName == "" || m.Title != "" || m.Text != "" || m.Approval != nil {
				return nil, nil, devicesec.ErrInvalid
			}
		} else if h.Session == "" {
			return nil, nil, devicesec.ErrInvalid
		}
		return &m, plain, nil
	case devicesec.Image:
		var m v1.ImagePayload
		if m.Unmarshal(plain) != nil || m.HasUnknown() {
			return nil, nil, devicesec.ErrInvalid
		}
		return &m, plain, nil
	case devicesec.Prefs:
		var m v1.ProjectPrefs
		if m.Unmarshal(plain) != nil || m.HasUnknown() || m.Schema != 1 {
			return nil, nil, devicesec.ErrInvalid
		}
		return &m, plain, nil
	case devicesec.DeviceMeta:
		var m v1.DeviceMeta
		if m.Unmarshal(plain) != nil || m.HasUnknown() {
			return nil, nil, devicesec.ErrInvalid
		}
		return &m, plain, nil
	default:
		return nil, nil, devicesec.ErrInvalid
	}
}

// ValidateBody checks that packed plaintext p matches h's signed purpose and bindings.
func ValidateBody(h devicesec.Header, p []byte) error {
	_, _, err := ValidateAndDecodeBody(h, p)
	return err
}

// IsReadOnlyCommand matches internal/deviceauth.IsReadOnlyCommand cleanly on v1.Command.
func IsReadOnlyCommand(cmd *v1.Command) bool {
	if cmd == nil {
		return false
	}
	switch k := cmd.Kind.(type) {
	case *v1.Command_GetMachineStatus,
		*v1.Command_GetUsageStats,
		*v1.Command_GetSettings,
		*v1.Command_ListModels,
		*v1.Command_ListDirectories,
		*v1.Command_GetLiveSnapshot,
		*v1.Command_GetImageUploadStatus,
		*v1.Command_GetSharedFileChunk:
		return true
	case *v1.Command_Schedule:
		return k.Schedule != nil && (k.Schedule.Operation == "list" || k.Schedule.Operation == "get")
	}
	return false
}
