package machinestatus

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

func readSystem(ctx context.Context) (Raw, error) {
	texts := make([]string, 4)
	for i, name := range []string{"stat", "meminfo", "loadavg", "uptime"} {
		b, e := os.ReadFile("/proc/" + name)
		if e != nil {
			return Raw{}, e
		}
		texts[i] = string(b)
	}
	r, e := ParseLinux(texts[0], texts[1], texts[2], texts[3])
	if e != nil {
		return r, e
	}
	entries, e := os.ReadDir("/proc")
	if e != nil {
		return r, e
	}
	for _, p := range entries {
		if ctx.Err() != nil {
			return r, ctx.Err()
		}
		if _, e := strconv.Atoi(p.Name()); e != nil {
			continue
		}
		b, e := os.ReadFile(filepath.Join("/proc", p.Name(), "stat"))
		if e != nil {
			continue
		}
		if p, e := ParseLinuxProcess(string(b), uint64(os.Getpagesize())); e == nil {
			r.Processes = append(r.Processes, p)
		}
	}
	r.Status.Battery = linuxBattery()
	return r, nil
}
func linuxBattery() *pb.MachineBattery {
	paths, _ := filepath.Glob("/sys/class/power_supply/*")
	for _, p := range paths {
		b, _ := os.ReadFile(filepath.Join(p, "type"))
		if strings.TrimSpace(string(b)) != "Battery" {
			continue
		}
		b, e := os.ReadFile(filepath.Join(p, "capacity"))
		if e != nil {
			continue
		}
		v, e := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
		if e != nil {
			continue
		}
		b, _ = os.ReadFile(filepath.Join(p, "status"))
		return &pb.MachineBattery{Percent: v, Charging: strings.TrimSpace(string(b)) == "Charging"}
	}
	return nil
}
