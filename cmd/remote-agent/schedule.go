package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/buildinfo"
	"github.com/Lewin671/remote-agent/internal/schedulemcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const scheduleHelp = `Usage:
  remote-agent schedule create [--session ID] [--request-id KEY] [--file JSON]
  remote-agent schedule list
  remote-agent schedule get TASK-ID
  remote-agent schedule update TASK-ID [--revision N] [--file JSON]
  remote-agent schedule pause|resume|cancel|run TASK-ID [--revision N]

All results are JSON. create/update read a complete TaskSpec from stdin unless
--file is supplied. --json and a trailing "-" are accepted for stdin use.
--session defaults to RA_SESSION in a managed Agent. "current" resolves there.

Example TaskSpec (resume the current conversation after its turn ends):
{
  "title": "Check deployment",
  "message": "Check the deployment result and continue troubleshooting if needed.",
  "trigger": {"type": "after_turn", "delay_seconds": 1800},
  "target": {"type": "resume_session", "session": "current"}
}

Triggers:
  at: RFC3339 "at" timestamp with timezone, one occurrence.
  interval: "interval_seconds" >= 1; optional "at" or initial "delay_seconds".
  after_turn: "delay_seconds" >= 0; optional "repeat":true to delay again after
    each scheduled execution ends. Requires a session; idle means delay from now.
  "max_runs" optionally limits dispatches. Interrupted turns pause continuation.
Targets:
  resume_session: "session" ID or "current". Automatically resumes an exited CLI.
  new_session: "cwd", "agent" (codex/claude), "model", "effort", "mode"
    (ask/auto/full). Missing fields inherit the creating session where possible.
    New sessions cannot start in Plan mode; choose an explicit start mode then.
"late_seconds" bounds catch-up after offline/sleep; default 86400 seconds.
Missed intervals are coalesced; executions of the same task never overlap.
Pause/cancel stop future dispatches, not work already dispatched. Use normal
session input cancellation/interruption to stop that work. History retains 100 runs.
`

func runSchedule(args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		fmt.Print(scheduleHelp)
		return nil
	}
	op := args[0]
	args = args[1:]
	id := ""
	if op != "create" && op != "list" {
		if len(args) == 0 {
			return errors.New("task ID is required")
		}
		id = args[0]
		args = args[1:]
	}
	fs := flag.NewFlagSet("schedule", flag.ContinueOnError)
	session := fs.String("session", os.Getenv("RA_SESSION"), "creating session")
	key := fs.String("request-id", "", "stable creation key")
	file := fs.String("file", "", "task JSON file; default stdin")
	revision := fs.Uint64("revision", 0, "expected task revision")
	_ = fs.Bool("json", true, "JSON output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 1 || len(fs.Args()) == 1 && fs.Args()[0] != "-" {
		return errors.New("unexpected schedule arguments")
	}
	q := &pb.ScheduleRequest{Operation: op, TaskId: id, SessionId: *session, RequestId: *key, ExpectedRevision: *revision}
	if op == "create" || op == "update" {
		var reader io.Reader = os.Stdin
		if *file != "" && *file != "-" {
			f, err := os.Open(*file)
			if err != nil {
				return err
			}
			defer f.Close()
			reader = f
		}
		b, err := io.ReadAll(io.LimitReader(reader, (64<<10)+1))
		if err != nil {
			return err
		}
		if len(b) > 64<<10 {
			return errors.New("task JSON exceeds 64 KiB")
		}
		q.SpecJson = b
	}
	if op == "create" && q.RequestId == "" {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return err
		}
		q.RequestId = hex.EncodeToString(b[:])
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := dial().Schedule(ctx, connect.NewRequest(q))
	if err != nil {
		return explain(err)
	}
	var data any
	if err := json.Unmarshal(res.Msg.ResultJson, &data); err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(data)
}

func runMCP(args []string) error {
	fs := flag.NewFlagSet("mcp", flag.ContinueOnError)
	session := fs.String("session", os.Getenv("RA_SESSION"), "bound Remote Agent session")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 0 {
		return errors.New("unexpected MCP arguments")
	}
	if *session == "" {
		return errors.New("MCP requires a managed session (--session or RA_SESSION)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	_, err := dial().GetSession(ctx, connect.NewRequest(&pb.GetSessionRequest{SessionId: *session}))
	cancel()
	if err != nil {
		return explain(err)
	}
	return schedulemcp.New(dial(), *session, buildinfo.Version).Run(context.Background(), &mcp.StdioTransport{})
}
