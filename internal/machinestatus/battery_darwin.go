package machinestatus

import (
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

var batteryPercent = regexp.MustCompile(`(\d+)%;`)

var macBatteryCache = batteryCache{now: time.Now}

func macBattery(ctx context.Context) *pb.MachineBattery {
	return macBatteryCache.get(ctx, readMacBattery)
}
func readMacBattery(ctx context.Context) *pb.MachineBattery {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	b, e := exec.CommandContext(ctx, "/usr/bin/pmset", "-g", "batt").Output()
	if e != nil {
		return nil
	}
	m := batteryPercent.FindSubmatch(b)
	if m == nil {
		return nil
	}
	v, _ := strconv.ParseFloat(string(m[1]), 64)
	return &pb.MachineBattery{Percent: v, Charging: strings.Contains(string(b), "; charging;")}
}
