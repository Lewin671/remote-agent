package machinestatus

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"runtime"
	"sync"
	"unsafe"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"
)

var mac struct {
	port     uint32
	timebase [2]uint32
	once     sync.Once
	err      error
	host     func() uint32
	stats    func(uint32, int32, unsafe.Pointer, *uint32) int32
	stats64  func(uint32, int32, unsafe.Pointer, *uint32) int32
	name     func(int32, unsafe.Pointer, uint32) int32
	task     func(int32, int32, uint64, unsafe.Pointer, int32) int32
}

func initMac() {
	mac.once.Do(func() {
		h, e := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if e != nil {
			mac.err = e
			return
		}
		purego.RegisterLibFunc(&mac.host, h, "mach_host_self")
		purego.RegisterLibFunc(&mac.stats, h, "host_statistics")
		purego.RegisterLibFunc(&mac.stats64, h, "host_statistics64")
		purego.RegisterLibFunc(&mac.task, h, "proc_pidinfo")
		purego.RegisterLibFunc(&mac.name, h, "proc_name")
		mac.port = mac.host()
		var timebase func(unsafe.Pointer) int32
		purego.RegisterLibFunc(&timebase, h, "mach_timebase_info")
		if timebase(unsafe.Pointer(&mac.timebase[0])) != 0 || mac.timebase[1] == 0 {
			mac.err = fmt.Errorf("mach timebase unavailable")
		}
	})
}

// SDK ABI: host_cpu_load_info (4 uint32), vm_statistics64 (38 uint32),
// proc_taskinfo (6 uint64 + 12 int32). Calls work in release CGO_ENABLED=0 builds.
func readSystem(ctx context.Context) (Raw, error) {
	initMac()
	if mac.err != nil {
		return Raw{}, mac.err
	}
	s := &pb.MachineStatus{Cores: uint32(runtime.NumCPU())}
	r := Raw{Status: s}
	host := mac.port
	var cpu [4]uint32
	n := uint32(4)
	if mac.stats(host, 3, unsafe.Pointer(&cpu[0]), &n) != 0 {
		return r, fmt.Errorf("host cpu counters unavailable")
	}
	for _, v := range cpu {
		r.Total += uint64(v)
	}
	r.Idle = uint64(cpu[2])
	mem, e := unix.SysctlUint64("hw.memsize")
	if e != nil {
		return r, e
	}
	s.MemoryTotalBytes = mem
	var vm [38]uint32
	n = 38
	if mac.stats64(host, 4, unsafe.Pointer(&vm[0]), &n) != 0 {
		return r, fmt.Errorf("host memory counters unavailable")
	}
	s.MemoryUsedBytes = MacMemoryUsed(vm[:], uint64(unix.Getpagesize()), mem)
	if level, e := unix.SysctlUint32("kern.memorystatus_vm_pressure_level"); e == nil {
		s.MemoryPressure = MacPressure(level)
		if s.MemoryPressure != pb.ResourcePressure_RESOURCE_PRESSURE_UNSPECIFIED {
			position := MacPressurePosition(s.MemoryPressure, float64(s.MemoryUsedBytes)/float64(mem))
			s.MemoryPressurePosition = &position
		}
	}
	if b, e := unix.SysctlRaw("vm.swapusage"); e == nil && len(b) >= 24 {
		v := binary.NativeEndian.Uint64(b[16:24])
		s.SwapUsedBytes = &v
	}
	if b, e := unix.SysctlRaw("vm.loadavg"); e == nil && len(b) >= 24 {
		scale := binary.NativeEndian.Uint64(b[16:24])
		if scale > 0 {
			v := float64(binary.NativeEndian.Uint32(b[:4])) / float64(scale)
			s.LoadOne = &v
		}
	}
	if tv, e := unix.SysctlTimeval("kern.boottime"); e == nil {
		s.UptimeSeconds = uint64(max(0, unixTime()-float64(tv.Sec)))
	}
	ps, e := unix.SysctlKinfoProcSlice("kern.proc.all")
	if e != nil {
		return r, e
	}
	for _, kp := range ps {
		if ctx.Err() != nil {
			return r, ctx.Err()
		}
		var task [12]uint64
		if mac.task(kp.Proc.P_pid, 4, 0, unsafe.Pointer(&task[0]), 96) != 96 {
			continue
		}
		var nameBytes [1024]byte
		mac.name(kp.Proc.P_pid, unsafe.Pointer(&nameBytes[0]), 1024)
		name := unix.ByteSliceToString(nameBytes[:])
		if name == "" {
			name = unix.ByteSliceToString(kp.Proc.P_comm[:])
		}
		r.Processes = append(r.Processes, Process{PID: int(kp.Proc.P_pid), Parent: int(kp.Eproc.Ppid), Start: uint64(kp.Proc.P_starttime.Sec)*1e6 + uint64(kp.Proc.P_starttime.Usec), Name: name, CPUSeconds: MacCPUSeconds(task[2]+task[3], mac.timebase[0], mac.timebase[1]), RSS: task[1]})
	}
	s.Battery = macBattery(ctx)
	return r, nil
}

// Keep memory accounting identical to Activity Monitor: anonymous + wired +
// compressed minus purgeable; file-backed inactive pages remain reclaimable.
func MacMemoryUsed(vm []uint32, page, total uint64) uint64 {
	if len(vm) < 36 {
		return 0
	}
	pages := uint64(vm[35]) + uint64(vm[3]) + uint64(vm[32])
	purge := uint64(vm[22])
	return min(total, (pages-min(pages, purge))*page)
}
func MacPressure(v uint32) pb.ResourcePressure {
	switch v {
	case 1:
		return pb.ResourcePressure_RESOURCE_PRESSURE_NORMAL
	case 2:
		return pb.ResourcePressure_RESOURCE_PRESSURE_WARNING
	case 4:
		return pb.ResourcePressure_RESOURCE_PRESSURE_CRITICAL
	}
	return pb.ResourcePressure_RESOURCE_PRESSURE_UNSPECIFIED
}
func MacPressurePosition(p pb.ResourcePressure, used float64) float64 {
	u := math.Max(0, math.Min(1, used))
	switch p {
	case pb.ResourcePressure_RESOURCE_PRESSURE_NORMAL:
		return u * .7
	case pb.ResourcePressure_RESOURCE_PRESSURE_WARNING:
		return .7 + u*.2
	default:
		return .9 + u*.1
	}
}

// proc_taskinfo CPU totals use Mach absolute ticks. Apple Silicon's timebase
// differs from Intel's 1:1; converting ticks as nanoseconds undercounts badly.
func MacCPUSeconds(ticks uint64, numer, denom uint32) float64 {
	if denom == 0 {
		return 0
	}
	return float64(ticks) * float64(numer) / float64(denom) / 1e9
}
