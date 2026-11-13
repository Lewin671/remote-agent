package machinestatus

import (
	"fmt"
	"strconv"
	"strings"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

func number(s string) uint64 { n, _ := strconv.ParseUint(s, 10, 64); return n }
func ParseLinux(stat, mem, load, uptime string) (Raw, error) {
	r := Raw{Status: &pb.MachineStatus{}}
	s := r.Status
	for _, line := range strings.Split(stat, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		if f[0] == "cpu" {
			for i, v := range f[1:] {
				if i >= 8 {
					break
				}
				r.Total += number(v)
				if i == 3 || i == 4 {
					r.Idle += number(v)
				}
			}
		} else if strings.HasPrefix(f[0], "cpu") {
			s.Cores++
		}
	}
	m := map[string]uint64{}
	for _, line := range strings.Split(mem, "\n") {
		f := strings.Fields(line)
		if len(f) > 1 {
			m[strings.TrimSuffix(f[0], ":")] = number(f[1]) * 1024
		}
	}
	s.MemoryTotalBytes = m["MemTotal"]
	available := m["MemAvailable"]
	if s.MemoryTotalBytes == 0 || available > s.MemoryTotalBytes || r.Total == 0 {
		return r, fmt.Errorf("invalid proc counters")
	}
	s.MemoryUsedBytes = s.MemoryTotalBytes - available
	swap := m["SwapTotal"] - min(m["SwapTotal"], m["SwapFree"])
	s.SwapUsedBytes = &swap
	free := float64(available) / float64(s.MemoryTotalBytes)
	s.MemoryPressure = pb.ResourcePressure_RESOURCE_PRESSURE_NORMAL
	if free < .1 {
		s.MemoryPressure = pb.ResourcePressure_RESOURCE_PRESSURE_CRITICAL
	} else if free < .2 {
		s.MemoryPressure = pb.ResourcePressure_RESOURCE_PRESSURE_WARNING
	}
	pos := 1 - free
	s.MemoryPressurePosition = &pos
	if f := strings.Fields(load); len(f) > 0 {
		if v, e := strconv.ParseFloat(f[0], 64); e == nil {
			s.LoadOne = &v
		}
	}
	if f := strings.Fields(uptime); len(f) > 0 {
		v, _ := strconv.ParseFloat(f[0], 64)
		s.UptimeSeconds = uint64(v)
	}
	return r, nil
}

// stat's comm may contain spaces and ')'. No cmdline, environ or cwd is read.
func ParseLinuxProcess(stat string, pageSize uint64) (Process, error) {
	a, b := strings.Index(stat, "("), strings.LastIndex(stat, ")")
	if a < 1 || b < a {
		return Process{}, fmt.Errorf("invalid process stat")
	}
	f := strings.Fields(stat[b+1:])
	if len(f) < 22 {
		return Process{}, fmt.Errorf("short process stat")
	}
	return Process{PID: int(number(strings.TrimSpace(stat[:a]))), Parent: int(number(f[1])), Name: stat[a+1 : b], Start: number(f[19]), CPUSeconds: float64(number(f[11])+number(f[12])) / 100, RSS: number(f[21]) * pageSize}, nil
}
