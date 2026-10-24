// Run only against ios/scripts/dev-env.sh's throwaway relay:
// RA_DEV_DIR=<isolated directory> go run ./scripts/acceptance-schedule
// Uses a synthetic client over encrypted Forward, and a real Claude Haiku CLI.
// Add --environment to verify source-less task creation and post-restart resume.
// RA_DEV_AGENT_BIN selects a before/after binary; RA_DEV_EXPECT_ENV_FAILURE=1
// requires the old missing-PATH failure rather than accepting success.
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
	"strings"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"github.com/Lewin671/remote-agent/internal/relay"
	"github.com/Lewin671/remote-agent/internal/remoteclient"
	"github.com/Lewin671/remote-agent/internal/schedule"
	"github.com/Lewin671/remote-agent/internal/updater"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}
func read(path string) string {
	b, e := os.ReadFile(path)
	must(e)
	return strings.TrimSpace(string(b))
}
func wait(ctx context.Context, what string, fn func() bool) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if fn() {
			fmt.Println("PASS", what)
			return
		}
		select {
		case <-ctx.Done():
			panic("timeout: " + what)
		case <-ticker.C:
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
	_, ed, err := ed25519.GenerateKey(nil)
	must(err)
	x, _, err := deviceauth.NewXKey()
	must(err)
	machine, err := auth.Enroll(ctx, ed, x, deviceauth.Machine)
	must(err)
	home := filepath.Join(dir, "schedule-home")
	must(os.MkdirAll(home, 0700))
	must(updater.EnsureState(home))
	must(os.Setenv("REMOTE_AGENT_HOME", home))
	must(machine.Endpoint.Save(filepath.Join(home, "security-v3.db")))
	acct := &relay.Account{Server: server, Root: auth.Root, DeviceSeed: ed.Seed(), DeviceID: machine.ID, Security: &relay.SecurityConfig{Pin: auth.Pin, XPrivate: x}}
	must(acct.Save())
	machine.Endpoint.Close()
	phone, err := deviceauth.InitEndpoint("", auth.Pin, auth.States, auth.Proofs, auth.Member.ID, auth.Ed.Seed(), auth.X, auth.Lease)
	must(err)
	defer phone.Close()
	keys, err := e2e.Derive(auth.Root)
	must(err)
	client := remoteclient.NewClient(server, auth.Token, auth.Member.ID, phone, keys)
	socket := read(filepath.Join(dir, "tmux-socket")) + "-schedule"
	must(os.Setenv("REMOTE_AGENT_TMUX_SOCKET", socket))
	binary := os.Getenv("RA_DEV_AGENT_BIN")
	if binary == "" {
		binary = filepath.Join(dir, "bin", "remote-agent")
	}
	log, err := os.OpenFile(filepath.Join(dir, "schedule-daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	must(err)
	defer log.Close()
	newDaemon := func() *exec.Cmd {
		cmd := exec.Command(binary, "daemon")
		// The scenario must use command-name lookup, never a daemon CLI override.
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
	if len(os.Args) == 2 && os.Args[1] == "--environment" {
		runEnvironmentAcceptance(ctx, client, machine.ID, home, socket, func() {
			must(process.Process.Signal(os.Interrupt))
			must(process.Wait())
			process = newDaemon()
			must(process.Start())
		})
		return
	}
	// Use this checkout as a trusted project; never open an existing session.
	cwd, err := os.Getwd()
	must(err)
	at := time.Now().UTC().Add(3 * time.Minute).Truncate(time.Minute)
	spec := schedule.Spec{Title: "Calendar acceptance", Message: "This is an isolated scheduled task acceptance check. Call the remote_agent MCP tool notify_user exactly once with text 'Calendar acceptance result' and no title. Then give a short final answer 'Calendar acceptance complete'. Do not create tasks or use any other tool.", Trigger: schedule.Trigger{Type: "calendar", Time: at.Format("15:04"), Timezone: "UTC", MaxRuns: 1}, Target: schedule.Target{Type: "new_session", Cwd: cwd, Agent: "claude", Model: "claude-haiku-5-5", Mode: "ask"}}
	b, err := json.Marshal(spec)
	must(err)
	res, err := client.DoMachine(ctx, machine.ID, &pb.Command{Kind: &pb.Command_Schedule{Schedule: &pb.ScheduleRequest{Operation: "create", SpecJson: b}}})
	must(err)
	var task schedule.Task
	must(json.Unmarshal(res.GetSchedule().ResultJson, &task))
	fmt.Println("PASS calendar task created through encrypted Forward; due", at.Format(time.RFC3339))
	// Restart before the due time: the saved task must still launch, and the
	// offline client's first directory listing must expose its pending approval.
	must(process.Process.Signal(os.Interrupt))
	must(process.Wait())
	process = newDaemon()
	must(process.Start())
	wait(ctx, "restarted daemon serves persisted task", func() bool {
		r, e := client.DoMachine(ctx, machine.ID, &pb.Command{Kind: &pb.Command_Schedule{Schedule: &pb.ScheduleRequest{Operation: "get", TaskId: task.ID}}})
		return e == nil && r.GetSchedule() != nil
	})
	getTask := func() {
		r, e := client.DoMachine(ctx, machine.ID, &pb.Command{Kind: &pb.Command_Schedule{Schedule: &pb.ScheduleRequest{Operation: "get", TaskId: task.ID}}})
		must(e)
		var latest schedule.Task
		must(json.Unmarshal(r.GetSchedule().ResultJson, &latest))
		task = latest
	}
	var sid string
	approvalRestarted := false
	waitRun := func(label string, manual bool) schedule.Run {
		sid = ""
		attentionSeen := false
		wait(ctx, label, func() bool {
			getTask()
			if task.Active != nil && task.Active.Manual == manual {
				sid = task.Active.SessionID
			}
			if sid != "" {
				// Answer a fresh-project trust prompt only after the CLI draws it.
				out, _ := exec.Command("tmux", "-L", socket, "capture-pane", "-p", "-t", "ra-"+sid[:8]).Output()
				if strings.Contains(string(out), "Notify User") && strings.Contains(string(out), "Do you want to proceed?") {
					// A fresh encrypted client, with no stream, by-ID fetch or
					// session cache: this is the same default cold directory read.
					cold := remoteclient.NewClient(server, auth.Token, auth.Member.ID, phone, keys)
					waiting, e := cold.API().ListRemoteSessions(ctx, connect.NewRequest(&pb.ListRemoteSessionsRequest{}))
					if e != nil {
						return false
					}
					var row *pb.RemoteSession
					for _, candidate := range waiting.Msg.Sessions {
						if candidate.SessionId == sid {
							row = candidate
						}
					}
					if row == nil {
						return false
					}
					view, e := cold.OpenSession(row)
					must(e)
					info := view.Info
					needs := info != nil && (info.PendingApprovals > 0 || info.PendingQuestions > 0 || info.PlanOpen || info.Status == pb.SessionStatus_SESSION_STATUS_BLOCKED)
					if !needs || !info.TaskOwned {
						return false
					}
					if !approvalRestarted {
						// Keep the native CLI and its approval dialog alive while
						// rebuilding daemon state from this session's event log.
						must(process.Process.Signal(os.Interrupt))
						must(process.Wait())
						process = newDaemon()
						must(process.Start())
						wait(ctx, "daemon restored the session while approval is pending", func() bool {
							r, e := client.Do(ctx, view, &pb.Command{Kind: &pb.Command_GetLiveSnapshot{GetLiveSnapshot: &pb.GetLiveSnapshotRequest{SessionId: sid}}})
							return e == nil && r.GetLiveSnapshot() != nil && r.GetLiveSnapshot().Session.GetTaskOwned() && r.GetLiveSnapshot().Session.GetPendingApprovals() > 0
						})
						approvalRestarted = true
						return false
					}
					if !attentionSeen {
						fmt.Println("PASS cold ListRemoteSessions after daemon restart includes task-owned approval in attention data")
						attentionSeen = true
					}
					// Approve this test tool once, never persist a global allow rule.
					exec.Command("tmux", "-L", socket, "send-keys", "-t", "ra-"+sid[:8], "Enter").Run()
				}
				if strings.Contains(string(out), "trust this folder") || strings.Contains(string(out), "Trust this folder") {
					exec.Command("tmux", "-L", socket, "send-keys", "-t", "ra-"+sid[:8], "Down", "Enter").Run()
				}
			}
			for _, run := range task.Runs {
				if run.Manual != manual {
					continue
				}
				if run.Status != "completed" {
					panic(fmt.Sprintf("unexpected execution result: %s/%s", run.Status, run.Reason))
				}
				return true
			}
			return false
		})
		if !attentionSeen {
			panic("no real approval attention data observed")
		}
		for _, run := range task.Runs {
			if run.Manual == manual {
				if !run.Notified || run.NotificationCount != 1 || run.NotificationText != "Calendar acceptance result" {
					panic("notification result missing")
				}
				return run
			}
		}
		panic("run disappeared")
	}
	getTask()
	due := *task.NextRunAt
	planBefore, _ := json.Marshal([]any{task.Status, task.Reason, task.NextRunAt, task.Dispatches, task.LastCalendarDate})
	_, err = client.DoMachine(ctx, machine.ID, &pb.Command{Kind: &pb.Command_Schedule{Schedule: &pb.ScheduleRequest{Operation: "run", TaskId: task.ID}}})
	must(err)
	getTask()
	planRequested, _ := json.Marshal([]any{task.Status, task.Reason, task.NextRunAt, task.Dispatches, task.LastCalendarDate})
	if string(planBefore) != string(planRequested) {
		panic("manual request changed automatic plan")
	}
	manualRun := waitRun("manual calendar execution completed through real CLI", true)
	getTask()
	planAfter, _ := json.Marshal([]any{task.Status, task.Reason, task.NextRunAt, task.Dispatches, task.LastCalendarDate})
	if string(planBefore) != string(planAfter) {
		panic("manual execution changed automatic plan")
	}
	wait(ctx, "manual CLI exited without consuming automatic max_runs", func() bool {
		rows, e := client.API().ListRemoteSessions(ctx, connect.NewRequest(&pb.ListRemoteSessionsRequest{SessionIds: []string{manualRun.SessionID}}))
		if e != nil || len(rows.Msg.Sessions) != 1 {
			return false
		}
		view, e := client.OpenSession(rows.Msg.Sessions[0])
		must(e)
		return view.Info.GetStatus() == pb.SessionStatus_SESSION_STATUS_EXITED
	})
	run := waitRun("same-day automatic calendar execution completed through real CLI", false)
	getTask()
	if !run.ScheduledAt.Equal(due) || task.Dispatches != 1 || task.Status != "completed" || len(task.Runs) != 2 || task.LastCalendarDate != due.UTC().Format("2006-01-02") {
		panic("manual trial displaced automatic occurrence or max_runs")
	}
	fmt.Println("PASS manual execution leaves today's automatic occurrence and max_runs=1 intact")

	sid = run.SessionID
	if !run.Notified || run.NotificationCount != 1 || run.NotificationText != "Calendar acceptance result" {
		panic(fmt.Sprintf("unexpected notification result: %+v", run))
	}
	fmt.Println("PASS real CLI invoked notify_user; run summary persisted")
	var session remoteclient.Session
	getSession := func() bool {
		r, e := client.API().ListRemoteSessions(ctx, connect.NewRequest(&pb.ListRemoteSessionsRequest{SessionIds: []string{sid}}))
		if e != nil || len(r.Msg.Sessions) != 1 {
			return false
		}
		session, e = client.OpenSession(r.Msg.Sessions[0])
		must(e)
		return true
	}
	wait(ctx, "CLI exited and encrypted session retained by ID", func() bool {
		return getSession() && session.Info.GetStatus() == pb.SessionStatus_SESSION_STATUS_EXITED && session.Info.TaskOwned
	})
	ss, err := client.Sessions(ctx)
	must(err)
	for _, s := range ss {
		if s.ID == sid {
			panic("task session leaked to default listing")
		}
	}
	evs, err := client.Events(ctx, session, 0)
	must(err)
	marked, notice := false, false
	for _, e := range evs {
		if end := e.GetTurnEnded(); end != nil && end.ScheduledTaskId == task.ID && end.ScheduledNotified {
			marked = true
		}
		if e.GetScheduledNotification() != nil {
			notice = true
		}
	}
	if !marked || !notice {
		panic("remote event stream lost scheduled annotations")
	}
	fmt.Println("PASS default listing hides task session; by-ID history contains notification and annotated end")
	_, err = client.Do(ctx, session, &pb.Command{Kind: &pb.Command_ResumeSession{ResumeSession: &pb.ResumeSessionRequest{SessionId: sid}}})
	must(err)
	_, err = client.Do(ctx, session, &pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: sid, RequestId: remoteclient.NewRequestID(), Text: "User takeover acceptance. Reply with only 'Taken over'."}}})
	must(err)
	wait(ctx, "client reply promotes session into default listing", func() bool {
		ss, e := client.Sessions(ctx)
		if e != nil {
			return false
		}
		for _, s := range ss {
			if s.ID == sid && !s.Info.TaskOwned {
				return true
			}
		}
		return false
	})
	_, err = client.Do(ctx, session, &pb.Command{Kind: &pb.Command_StopSession{StopSession: &pb.StopSessionRequest{SessionId: sid}}})
	must(err)
	// Exercise the public whole-definition update path after a real interval
	// execution, rather than directly adjusting a daemon cursor.
	first := time.Now().UTC().Add(2 * time.Second)
	intervalSpec := spec
	intervalSpec.Title = "Interval update acceptance"
	intervalSpec.Trigger = schedule.Trigger{Type: "interval", At: &first, IntervalSeconds: 3600}
	raw, err := json.Marshal(intervalSpec)
	must(err)
	created, err := client.DoMachine(ctx, machine.ID, &pb.Command{Kind: &pb.Command_Schedule{Schedule: &pb.ScheduleRequest{Operation: "create", SpecJson: raw}}})
	must(err)
	task = schedule.Task{}
	must(json.Unmarshal(created.GetSchedule().ResultJson, &task))
	intervalRun := waitRun("real interval first execution completed", false)
	getTask()
	expectedTrigger, err := json.Marshal(task.Spec.Trigger)
	must(err)
	next := *task.NextRunAt
	edited := task.Spec
	edited.Title = "Interval update acceptance revised"
	raw, err = json.Marshal(edited)
	must(err)
	_, err = client.DoMachine(ctx, machine.ID, &pb.Command{Kind: &pb.Command_Schedule{Schedule: &pb.ScheduleRequest{Operation: "update", TaskId: task.ID, ExpectedRevision: task.Revision, SpecJson: raw}}})
	must(err)
	until := time.Now().Add(4 * time.Second)
	wait(ctx, "whole-definition title-only edit does not repeat the completed interval", func() bool {
		getTask()
		trigger, e := json.Marshal(task.Spec.Trigger)
		must(e)
		if string(trigger) != string(expectedTrigger) || task.Active != nil || task.Dispatches != 1 || len(task.Runs) != 1 || task.Runs[0].ID != intervalRun.ID || task.NextRunAt == nil || !task.NextRunAt.Equal(next) || !task.NextRunAt.After(intervalRun.ScheduledAt) {
			panic("title-only interval update repeated or rewound execution")
		}
		return !time.Now().Before(until)
	})
	sid = intervalRun.SessionID
	wait(ctx, "interval CLI exited with its original automatic phase intact", func() bool { return getSession() && session.Info.GetStatus() == pb.SessionStatus_SESSION_STATUS_EXITED })
	_, err = client.DoMachine(ctx, machine.ID, &pb.Command{Kind: &pb.Command_Schedule{Schedule: &pb.ScheduleRequest{Operation: "cancel", TaskId: task.ID}}})
	must(err)
	fmt.Println("PASS acceptance complete; synthetic client commands used, no product UI or push delivery service exercised")
}
