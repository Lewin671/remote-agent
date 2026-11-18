//go:build darwin

package daemon

import (
	"net"

	"golang.org/x/sys/unix"
)

func peerPID(c *net.UnixConn) (int, bool) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, false
	}
	var pid int
	var serr error
	if err := raw.Control(func(fd uintptr) {
		pid, serr = unix.GetsockoptInt(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERPID)
	}); err != nil || serr != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

func osParentPID(pid int) (int, bool) {
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || kp.Eproc.Ppid <= 0 {
		return 0, false
	}
	return int(kp.Eproc.Ppid), true
}
