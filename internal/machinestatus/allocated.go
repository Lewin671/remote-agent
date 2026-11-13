package machinestatus

import (
	"os"
	"syscall"
)

func allocatedInfo(i os.FileInfo, seen map[[2]uint64]bool) uint64 {
	if s, ok := i.Sys().(*syscall.Stat_t); ok {
		k := [2]uint64{uint64(s.Dev), uint64(s.Ino)}
		if seen[k] {
			return 0
		}
		seen[k] = true
		return uint64(s.Blocks) * 512
	}
	return uint64(i.Size())
}
