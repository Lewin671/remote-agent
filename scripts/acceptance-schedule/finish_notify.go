package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/remoteclient"
	"github.com/Lewin671/remote-agent/internal/schedule"
)

// Synthetic encrypted client -> local relay -> daemon -> real native Claude CLI
// -> persisted ScheduledNotification -> encrypted history read by the client.
func runFinishNotifyAcceptance(ctx context.Context, client *remoteclient.Client, machine, socket string) {
	cwd, err := os.Getwd()
	must(err)
	at := time.Now().UTC().Add(2 * time.Second)
	spec := schedule.Spec{Title: "Completion notification acceptance", Message: "This is an isolated acceptance test. Do not use any tools, including notify_user. Reply with exactly: Completion fallback verified.", Trigger: schedule.Trigger{Type: "at", At: &at}, Target: schedule.Target{Type: "new_session", Cwd: cwd, Agent: "claude", Model: "claude-haiku-5-5", Mode: "ask"}}
	raw, err := json.Marshal(spec)
	must(err)
	res, err := client.DoMachine(ctx, machine, &pb.Command{Kind: &pb.Command_Schedule{Schedule: &pb.ScheduleRequest{Operation: "create", SpecJson: raw}}})
	must(err)
	var task schedule.Task
	must(json.Unmarshal(res.GetSchedule().ResultJson, &task))
	var run schedule.Run
	wait(ctx, "real CLI completes without notify_user and daemon records completion notice", func() bool {
		res, err := client.DoMachine(ctx, machine, &pb.Command{Kind: &pb.Command_Schedule{Schedule: &pb.ScheduleRequest{Operation: "get", TaskId: task.ID}}})
		if err != nil {
			return false
		}
		must(json.Unmarshal(res.GetSchedule().ResultJson, &task))
		if active := task.Active; active != nil && active.SessionID != "" {
			pane := "ra-" + active.SessionID[:8]
			out, _ := exec.Command("tmux", "-L", socket, "capture-pane", "-p", "-t", pane).Output()
			if strings.Contains(string(out), "trust this folder") || strings.Contains(string(out), "Trust this folder") {
				exec.Command("tmux", "-L", socket, "send-keys", "-t", pane, "Down", "Enter").Run()
			}
		}
		if len(task.Runs) == 0 {
			return false
		}
		run = task.Runs[0]
		if run.Status != "completed" {
			panic("unexpected completion result: " + run.Status)
		}
		return run.Notified
	})
	if run.NotificationCount != 1 || run.NotificationSource != "daemon" || run.FinalSummary != "Completion fallback verified." || run.NotificationText != run.FinalSummary {
		panic("wrong fallback notification record")
	}
	var session remoteclient.Session
	wait(ctx, "encrypted by-ID history contains exactly one daemon notification", func() bool {
		rows, err := client.API().ListRemoteSessions(ctx, connect.NewRequest(&pb.ListRemoteSessionsRequest{SessionIds: []string{run.SessionID}}))
		if err != nil || len(rows.Msg.Sessions) != 1 {
			return false
		}
		session, err = client.OpenSession(rows.Msg.Sessions[0])
		must(err)
		events, err := client.Events(ctx, session, 0)
		if err != nil {
			return false
		}
		count := 0
		for _, e := range events {
			if n := e.GetScheduledNotification(); n != nil {
				count++
				if e.Origin.GetKind() != pb.OriginKind_ORIGIN_KIND_SYSTEM || n.Title != spec.Title || n.Text != run.FinalSummary {
					panic("wrong encrypted notification event")
				}
			}
		}
		if count > 1 {
			panic("duplicate notification events")
		}
		return count == 1
	})
	fmt.Println("PASS default-on task, real CLI final answer, daemon fallback and encrypted notification event; synthetic client commands used, no product UI or OS push delivery exercised")
}
