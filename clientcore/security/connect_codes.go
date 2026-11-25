package security

const (
	ConnectCodeCanceled           int32 = 1
	ConnectCodeUnknown            int32 = 2
	ConnectCodeInvalidArgument    int32 = 3
	ConnectCodeDeadlineExceeded   int32 = 4
	ConnectCodeNotFound           int32 = 5
	ConnectCodeAlreadyExists      int32 = 6
	ConnectCodePermissionDenied   int32 = 7
	ConnectCodeResourceExhausted  int32 = 8
	ConnectCodeFailedPrecondition int32 = 9
	ConnectCodeAborted            int32 = 10
	ConnectCodeOutOfRange         int32 = 11
	ConnectCodeUnimplemented      int32 = 12
	ConnectCodeInternal           int32 = 13
	ConnectCodeUnavailable        int32 = 14
	ConnectCodeDataLoss           int32 = 15
	ConnectCodeUnauthenticated    int32 = 16

	connectCodeCanceled           = ConnectCodeCanceled
	connectCodeUnknown            = ConnectCodeUnknown
	connectCodeInvalidArgument    = ConnectCodeInvalidArgument
	connectCodeDeadlineExceeded   = ConnectCodeDeadlineExceeded
	connectCodeNotFound           = ConnectCodeNotFound
	connectCodeAlreadyExists      = ConnectCodeAlreadyExists
	connectCodePermissionDenied   = ConnectCodePermissionDenied
	connectCodeResourceExhausted  = ConnectCodeResourceExhausted
	connectCodeFailedPrecondition = ConnectCodeFailedPrecondition
	connectCodeAborted            = ConnectCodeAborted
	connectCodeUnavailable        = ConnectCodeUnavailable
	connectCodeUnauthenticated    = ConnectCodeUnauthenticated
)

// ConnectCodeName maps a numeric ConnectRPC status code to its canonical snake_case name.
func ConnectCodeName(code int32) string {
	switch code {
	case ConnectCodeCanceled:
		return "canceled"
	case ConnectCodeUnknown:
		return "unknown"
	case ConnectCodeInvalidArgument:
		return "invalid_argument"
	case ConnectCodeDeadlineExceeded:
		return "deadline_exceeded"
	case ConnectCodeNotFound:
		return "not_found"
	case ConnectCodeAlreadyExists:
		return "already_exists"
	case ConnectCodePermissionDenied:
		return "permission_denied"
	case ConnectCodeResourceExhausted:
		return "resource_exhausted"
	case ConnectCodeFailedPrecondition:
		return "failed_precondition"
	case ConnectCodeAborted:
		return "aborted"
	case ConnectCodeOutOfRange:
		return "out_of_range"
	case ConnectCodeUnimplemented:
		return "unimplemented"
	case ConnectCodeInternal:
		return "internal"
	case ConnectCodeUnavailable:
		return "unavailable"
	case ConnectCodeDataLoss:
		return "data_loss"
	case ConnectCodeUnauthenticated:
		return "unauthenticated"
	default:
		return "unknown"
	}
}
