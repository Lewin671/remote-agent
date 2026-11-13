// Isolated encrypted acceptance. Run with RA_DEV_DIR from dev-env.sh start --no-account.
// It creates and tears down its own daemon/tmux/Codex session; never uses daily state.
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/relay"
	"github.com/Lewin671/remote-agent/internal/remoteclient"
	"github.com/Lewin671/remote-agent/internal/updater"
)

func must(e error) {
	if e != nil {
		panic(e)
	}
}
func read(path string) string {
	b, e := os.ReadFile(path)
	must(e)
	return strings.TrimSpace(string(b))
}
func wait(ctx context.Context, what string, fn func() bool) {
	for {
		if fn() {
			fmt.Println("PASS", what)
			return
		}
		select {
		case <-ctx.Done():
			panic(what + ": timeout")
		case <-time.After(300 * time.Millisecond):
		}
	}
}
func main() {
	dir := os.Getenv("RA_DEV_DIR")
	if dir == "" {
		panic("RA_DEV_DIR is required")
	}
	server := read(filepath.Join(dir, "url"))
	u, err := url.Parse(server)
	must(err)
	if u.Scheme != "http" || u.Hostname() != "127.0.0.1" {
		panic("refusing a non-local dev relay")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	auth, err := remoteclient.NewAuthority(ctx, server, read(filepath.Join(dir, "setup-token")))
	must(err)
	if len(os.Args) > 1 && os.Args[1] == "--subscriptions-only" {
		endpoint, e := deviceauth.InitEndpoint("", auth.Pin, auth.States, auth.Proofs, auth.Member.ID, auth.Ed.Seed(), auth.X, auth.Lease)
		must(e)
		defer endpoint.Close()
		c := remoteclient.NewClient(server, auth.Token, auth.Member.ID, endpoint)
		base := []pb.NotifyKind{pb.NotifyKind_NOTIFY_KIND_ATTENTION, pb.NotifyKind_NOTIFY_KIND_TURN_ENDED, pb.NotifyKind_NOTIFY_KIND_SECURITY, pb.NotifyKind_NOTIFY_KIND_SCHEDULED}
		_, e = c.API().SetPushTarget(ctx, connect.NewRequest(&pb.SetPushTargetRequest{Type: pb.PushType_PUSH_TYPE_FCM, Target: "compatibility-fixture", Kinds: base}))
		must(e)
		cap, e := c.API().GetPushKey(ctx, connect.NewRequest(&pb.GetPushKeyRequest{}))
		must(e)
		requested := append(append([]pb.NotifyKind(nil), base...), pb.NotifyKind_NOTIFY_KIND_MACHINE_DISK)
		if !cap.Msg.MachineDiskNotifications {
			_, e = c.API().SetPushTarget(ctx, connect.NewRequest(&pb.SetPushTargetRequest{Type: pb.PushType_PUSH_TYPE_FCM, Target: "compatibility-fixture", Kinds: requested}))
			if connect.CodeOf(e) != connect.CodeInvalidArgument {
				panic("legacy server did not exercise the unknown-kind rejection")
			}
		}
		_, e = c.SetMachinePushTarget(ctx, pb.PushType_PUSH_TYPE_FCM, "compatibility-fixture", requested)
		must(e)
		_, e = c.SetMachinePushTarget(ctx, pb.PushType_PUSH_TYPE_FCM, "", nil)
		must(e)
		fmt.Println("PASS capability-gated subscription; advertised=", cap.Msg.MachineDiskNotifications)
		return
	}
	_, ed, err := ed25519.GenerateKey(nil)
	must(err)
	x, _, err := deviceauth.NewXKey()
	must(err)
	machine, err := auth.Enroll(ctx, ed, x, deviceauth.Machine)
	must(err)
	home, err := os.MkdirTemp(dir, "status-home-")
	must(err)
	must(updater.EnsureState(home))
	must(os.Setenv("REMOTE_AGENT_HOME", home))
	must(machine.Endpoint.Save(filepath.Join(home, "security-v4.db")))
	acct := &relay.Account{Server: server, Root: auth.Root, DeviceSeed: ed.Seed(), DeviceID: machine.ID, Security: &relay.SecurityConfig{Pin: auth.Pin, XPrivate: x}}
	must(acct.Save())
	machine.Endpoint.Close()
	phone, err := deviceauth.InitEndpoint("", auth.Pin, auth.States, auth.Proofs, auth.Member.ID, auth.Ed.Seed(), auth.X, auth.Lease)
	must(err)
	defer phone.Close()
	client := remoteclient.NewClient(server, auth.Token, auth.Member.ID, phone)
	socket := read(filepath.Join(dir, "tmux-socket")) + "-status"
	must(os.Setenv("REMOTE_AGENT_TMUX_SOCKET", socket))
	binary := os.Getenv("RA_DEV_AGENT_BIN")
	if binary == "" {
		binary = filepath.Join(dir, "bin", "remote-agent")
	}
	log, err := os.OpenFile(filepath.Join(dir, "status-daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	must(err)
	defer log.Close()
	// A bounded CPU worker is injected at native CLI launch for deterministic
	// process-tree measurement. The wrapper then execs the actual interactive
	// Codex, preserving the parent PID. No SDK or fake CLI is used.
	realCodex, err := exec.LookPath("codex")
	must(err)
	python, err := exec.LookPath("python3")
	must(err)
	workerPID := filepath.Join(home, "load-worker.pid")
	wrapper := filepath.Join(home, "codex-load")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	script := "#!/bin/sh\ncase \"$1\" in --version|--help) exec " + quote(realCodex) + " \"$@\";; esac\n" + quote(python) + " -c 'import time; end=time.time()+180; exec(\"while time.time()<end: pass\")' >/dev/null 2>&1 &\necho \"$!\" > " + quote(workerPID) + "\nexec " + quote(realCodex) + " \"$@\"\n"
	must(os.WriteFile(wrapper, []byte(script), 0700))
	defer func() {
		if b, e := os.ReadFile(workerPID); e == nil {
			_ = exec.Command("kill", "-TERM", strings.TrimSpace(string(b))).Run()
		}
	}()
	newDaemon := func() *exec.Cmd {
		cmd := exec.Command(binary, "daemon", "--codex", wrapper)
		// Only this test daemon uses the bounded-load native launcher.
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "REMOTE_AGENT_CLAUDE=") {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		cmd.Stdout, cmd.Stderr = log, log
		return cmd
	}
	process := newDaemon()
	must(process.Start())
	defer func() {
		process.Process.Signal(os.Interrupt)
		process.Wait()
		exec.Command("tmux", "-L", socket, "kill-server").Run()
	}()
	wait(ctx, "isolated machine online", func() bool {
		ms, e := client.Machines(ctx)
		if e != nil {
			return false
		}
		for _, m := range ms {
			if m.ID == machine.ID && m.Online {
				return true
			}
		}
		return false
	})
	runStatus(ctx, client, machine.ID, home, socket)
}

func runStatus(ctx context.Context, c *remoteclient.Client, machine, home, socket string) {
	project := filepath.Join(home, "project")
	must(os.MkdirAll(project, 0700))
	must(exec.Command("git", "-C", project, "init", "-q").Run())
	// The encrypted create reaches daemon -> tmux -> actual native Codex.
	r, e := c.DoMachine(ctx, machine, &pb.Command{Kind: &pb.Command_CreateSession{CreateSession: &pb.CreateSessionRequest{Cwd: project, Agent: pb.Agent_AGENT_CODEX}}})
	must(e)
	sid := r.GetCreateSession().GetSession().GetId()
	if sid == "" {
		panic("no native session")
	}
	wait(ctx, "controlled child belongs to real native session", func() bool {
		sample, e := c.GetMachineStatus(ctx, machine)
		if e != nil {
			return false
		}
		for _, p := range sample.Processes {
			if strings.Contains(strings.ToLower(p.Executable), "python") && p.SessionId == sid && p.CpuPercent > 30 {
				return true
			}
		}
		return false
	})
	started := time.Now()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	var first, last *pb.MachineStatus
	count, maxPoints := 0, 0
	attributed := false
	maxSessionCPU := 0.0
	for {
		sample, e := c.GetMachineStatus(ctx, machine)
		must(e)
		if first == nil {
			first = sample
		}
		last = sample
		count++
		maxPoints = max(maxPoints, len(sample.Samples))
		for _, p := range sample.Processes {
			if strings.Contains(strings.ToLower(p.Executable), "python") && p.SessionId == sid && p.CpuPercent > 30 {
				attributed = true
			}
		}
		for _, s := range sample.Sessions {
			if s.SessionId == sid {
				maxSessionCPU = max(maxSessionCPU, s.CpuPercent)
			}
		}
		if time.Since(started) >= 122*time.Second {
			break
		}
		select {
		case <-ctx.Done():
			panic("status test timeout")
		case <-tick.C:
		}
	}
	summary := map[string]any{"requests": count, "elapsed_seconds": time.Since(started).Seconds(), "history_points": maxPoints, "child_attributed": attributed, "max_session_cpu_percent": maxSessionCPU, "first_cpu_percent": first.CpuPercent, "last_cpu_percent": last.CpuPercent, "memory_used_bytes": last.MemoryUsedBytes, "memory_total_bytes": last.MemoryTotalBytes, "disk_total_bytes": last.GetDisk().GetTotalBytes(), "disk_available_bytes": last.GetDisk().GetAvailableBytes(), "agent_bytes": last.GetAgentBytes(), "reserve_bytes": last.GetDisk().GetEmergencyReserveBytes(), "uptime_seconds": last.UptimeSeconds}
	if b, e := exec.Command("top", "-l", "2", "-s", "1", "-n", "0").Output(); e == nil {
		matches := regexp.MustCompile(`([0-9.]+)% idle`).FindAllSubmatch(b, -1)
		if len(matches) > 0 {
			v, _ := strconv.ParseFloat(string(matches[len(matches)-1][1]), 64)
			summary["top_cpu_used_percent"] = 100 - v
		}
	}
	if b, e := exec.Command("df", "-k", home).Output(); e == nil {
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		if len(lines) > 1 {
			f := strings.Fields(lines[1])
			if len(f) > 3 {
				total, _ := strconv.ParseUint(f[1], 10, 64)
				available, _ := strconv.ParseUint(f[3], 10, 64)
				summary["df_total_bytes"] = total * 1024
				summary["df_available_bytes"] = available * 1024
			}
		}
	}
	if b, e := exec.Command("vm_stat").Output(); e == nil {
		m := map[string]uint64{}
		for _, line := range strings.Split(string(b), "\n") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				m[strings.TrimSpace(parts[0])], _ = strconv.ParseUint(strings.TrimRight(strings.TrimSpace(parts[1]), "."), 10, 64)
			}
		}
		summary["vm_stat_used_bytes"] = (m["Anonymous pages"] + m["Pages wired down"] + m["Pages occupied by compressor"] - m["Pages purgeable"]) * uint64(os.Getpagesize())
	}
	if pid, e := os.ReadFile(filepath.Join(home, "load-worker.pid")); e == nil {
		if b, e := exec.Command("ps", "-p", strings.TrimSpace(string(pid)), "-o", "pcpu=,rss=").Output(); e == nil {
			f := strings.Fields(string(b))
			if len(f) == 2 {
				summary["ps_child_cpu_percent"], _ = strconv.ParseFloat(f[0], 64)
				rss, _ := strconv.ParseUint(f[1], 10, 64)
				summary["ps_child_memory_bytes"] = rss * 1024
			}
		}
	}
	b, _ := json.MarshalIndent(summary, "", "  ")
	fmt.Println(string(b))
	if maxPoints != 60 || !attributed {
		panic("history or real child attribution failed")
	}
	fmt.Println("PASS encrypted status calls, two minute history, real native child attribution")
}
