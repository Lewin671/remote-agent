package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/remoteclient"
	"github.com/Lewin671/remote-agent/internal/schedule"
)

func runEnvironmentAcceptance(ctx context.Context, client *remoteclient.Client, machine, state, socket string, restart func()) {
	cwd, err := os.Getwd()
	must(err)
	at := time.Now().Add(2 * time.Second)
	spec := schedule.Spec{Title: "Environment acceptance", Message: "This is an isolated environment acceptance check. Reply exactly 'Scheduled environment ready'. Do not call any tool or create tasks.", Trigger: schedule.Trigger{Type: "at", At: &at}, Target: schedule.Target{Type: "new_session", Cwd: cwd, Agent: "claude", Model: "claude-haiku-5-5", Mode: "ask"}}
	b, err := json.Marshal(spec)
	must(err)
	created, err := client.DoMachine(ctx, machine, &pb.Command{Kind: &pb.Command_Schedule{Schedule: &pb.ScheduleRequest{Operation: "create", SpecJson: b}}})
	must(err)
	var task schedule.Task
	must(json.Unmarshal(created.GetSchedule().ResultJson, &task))
	if task.SourceSession != "" {
		panic("test task unexpectedly inherited a source session")
	}
	var sid string
	var view remoteclient.Session
	getSession := func() bool {
		rows, err := client.API().ListRemoteSessions(ctx, connect.NewRequest(&pb.ListRemoteSessionsRequest{SessionIds: []string{sid}}))
		if err != nil || len(rows.Msg.Sessions) != 1 {
			return false
		}
		view, err = client.OpenSession(rows.Msg.Sessions[0])
		return err == nil
	}
	hasReply := func(text string, after uint64) bool {
		events, err := client.Events(ctx, view, after)
		if err != nil {
			return false
		}
		for _, e := range events {
			if m := e.GetAgentMessage(); m != nil && strings.Contains(m.Text, text) {
				return true
			}
		}
		return false
	}
	wait(ctx, "source-less encrypted client task dispatched and real Claude replied", func() bool {
		result, err := client.DoMachine(ctx, machine, &pb.Command{Kind: &pb.Command_Schedule{Schedule: &pb.ScheduleRequest{Operation: "get", TaskId: task.ID}}})
		if err != nil {
			return false
		}
		must(json.Unmarshal(result.GetSchedule().ResultJson, &task))
		if task.Active != nil {
			sid = task.Active.SessionID
		}
		for _, run := range task.Runs {
			if run.SessionID != "" {
				sid = run.SessionID
			}
		}
		if sid == "" || !getSession() {
			return false
		}
		return hasReply("Scheduled environment ready", 0)
	})
	wait(ctx, "scheduled CLI automatically closed", func() bool {
		return getSession() && view.Info.GetStatus() == pb.SessionStatus_SESSION_STATUS_EXITED
	})
	dir := filepath.Join(state, "sessions", sid)
	var meta struct {
		Shell bool `json:"shell"`
	}
	b, err = os.ReadFile(filepath.Join(dir, "session.json"))
	must(err)
	must(json.Unmarshal(b, &meta))
	var env map[string]string
	b, err = os.ReadFile(filepath.Join(dir, "env.json"))
	must(err)
	must(json.Unmarshal(b, &env))
	expectFailure := os.Getenv("RA_DEV_EXPECT_ENV_FAILURE") == "1"
	if expectFailure {
		if meta.Shell || env["PATH"] != "" {
			panic("old binary did not exhibit the expected direct launch with missing PATH")
		}
		fmt.Println("PASS before fix: dispatch works but persists direct launch and no PATH")
	} else {
		if !meta.Shell || env["PATH"] == "" {
			panic("source-less task did not persist the client login-shell launch style")
		}
		resolved, err := os.ReadFile(filepath.Join(dir, "shell-bin"))
		must(err)
		if strings.TrimSpace(string(resolved)) == "" {
			panic("login shell did not report a resolved CLI")
		}
		fmt.Println("PASS after fix: login shell resolved real Claude and persisted a nonempty starting PATH")
	}
	restart()
	wait(ctx, "new daemon restored the exited task session", func() bool {
		_, err := client.DoMachine(ctx, machine, &pb.Command{Kind: &pb.Command_Schedule{Schedule: &pb.ScheduleRequest{Operation: "get", TaskId: task.ID}}})
		return err == nil && getSession()
	})
	result, err := client.Do(ctx, view, &pb.Command{Kind: &pb.Command_ResumeSession{ResumeSession: &pb.ResumeSessionRequest{SessionId: sid}}})
	if expectFailure {
		if err == nil || !strings.Contains(err.Error(), "claude was not found in the session's saved PATH") {
			panic(fmt.Sprintf("old resume did not reproduce missing-PATH failure: %v", err))
		}
		fmt.Println("PASS before fix: post-restart resume fails looking up Claude in the saved PATH")
		return
	}
	must(err)
	if result.GetResumeSession() == nil {
		panic("resume response missing")
	}
	after := view.Info.LastSeq
	_, err = client.Do(ctx, view, &pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: sid, RequestId: remoteclient.NewRequestID(), Text: "Reply exactly 'Resumed environment ready'. Do not call any tool."}}})
	must(err)
	wait(ctx, "post-restart login-shell resume received input and real Claude replied", func() bool {
		return hasReply("Resumed environment ready", after)
	})
	// Confirm the second launch also used the login shell rather than a direct path.
	resolved, err := os.ReadFile(filepath.Join(dir, "shell-bin"))
	must(err)
	if strings.TrimSpace(string(resolved)) == "" {
		panic("resumed login shell did not report its CLI")
	}
	_, err = client.Do(ctx, view, &pb.Command{Kind: &pb.Command_StopSession{StopSession: &pb.StopSessionRequest{SessionId: sid}}})
	must(err)
	wait(ctx, "resumed acceptance CLI stopped", func() bool {
		return getSession() && view.Info.GetStatus() == pb.SessionStatus_SESSION_STATUS_EXITED
	})
	if out, _ := exec.Command("tmux", "-L", socket, "list-sessions").Output(); strings.Contains(string(out), "ra-"+sid[:8]) {
		panic("acceptance CLI remained in tmux after stop")
	}
	fmt.Println("PASS environment acceptance complete; encrypted synthetic client, no product UI")
}
