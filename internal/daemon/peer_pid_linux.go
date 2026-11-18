//go:build linux

package daemon

import (
	"bytes"
	"net"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func peerPID(c *net.UnixConn) (int, bool) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, false
	}
	var ucred *unix.Ucred
	var serr error
	if err := raw.Control(func(fd uintptr) {
		ucred, serr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil || serr != nil || ucred == nil || ucred.Pid <= 0 {
		return 0, false
	}
	return int(ucred.Pid), true
}

func osParentPID(pid int) (int, bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	idx := bytes.LastIndexByte(b, ')')
	if idx < 0 || idx+1 >= len(b) {
		return 0, false
	}
	fields := strings.Fields(string(b[idx+1:]))
	if len(fields) < 2 {
		return 0, false
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil || ppid <= 0 {
		return 0, false
	}
	return ppid, true
}
