package protocol

import (
	"errors"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

const DiskFull = "disk_full"
const StorageExhausted = "storage_exhausted"

func NewMachineError(code, message string) error {
	e := connect.NewError(connect.CodeResourceExhausted, errors.New(message))
	if d, err := connect.NewErrorDetail(&pb.MachineError{Code: code}); err == nil {
		e.AddDetail(d)
	}
	return e
}
func MachineErrorCode(err error) string {
	var e *connect.Error
	if errors.As(err, &e) {
		for _, d := range e.Details() {
			if v, err := d.Value(); err == nil {
				if info, ok := v.(*pb.MachineError); ok {
					return info.Code
				}
			}
		}
	}
	return ""
}
