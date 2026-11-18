//go:build !darwin && !linux

package daemon

import "net"

func peerPID(*net.UnixConn) (int, bool) { return 0, false }
func osParentPID(int) (int, bool)      { return 0, false }
