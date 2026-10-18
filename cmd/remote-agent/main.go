// Command remote-agent runs the local daemon and its command-line tools.
package main

import (
	"fmt"
	"os"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/buildinfo"
	"github.com/Lewin671/remote-agent/internal/paths"
	"github.com/Lewin671/remote-agent/internal/updater"
)

const usage = `remote-agent: operate native AI coding CLIs running in tmux

Usage:
  remote-agent update [check|status|auto]  update the daemon; update help for options
  remote-agent --version                  show product version and source revision
  remote-agent schedule <operation>       manage persistent timed Agent messages (schedule help)
  remote-agent mcp [--session ID]          serve scheduling tools over stdio
  remote-agent daemon [flags]              run the daemon in the foreground
  remote-agent codex [codex args]          start a Codex session here and attach to it
  remote-agent claude [claude args]        start a Claude Code session here and attach to it
  remote-agent new [--agent claude] [args] start a session here without attaching
  remote-agent ls [-a]                     list sessions
  remote-agent attach <id>                 attach to a session
  remote-agent send [--queue] <id> <text>  send a message ("-" reads stdin); --queue waits for the turn to end
  remote-agent cancel <id> <request-id>    withdraw a message the model has not received
  remote-agent resume-input <id> <req-id>  send a message paused by an interrupted turn
  remote-agent events <id> [-f] [--json]   show the session's events
  remote-agent approve <id> <approval-id>  allow a permission request (--always: don't ask again)
  remote-agent deny <id> <approval-id> [-m reason]
  remote-agent answer <id> <question-id> <option>[,<option>...]
  remote-agent interrupt <id>              interrupt the running turn
  remote-agent stop <id>                   end the session
  remote-agent resume <id>                 start an ended session again
  remote-agent fork <id>                   start a new session from a session's conversation
  remote-agent model [codex|claude]        list a CLI's models
  remote-agent model <id> <model> [<effort>]
                                           switch a session's model
  remote-agent mode <id> ask|auto|full|plan switch the approval mode
  remote-agent rename <id> [<title>]       title the session; no title goes back
                                           to the automatic one
  remote-agent compact <id>                compact the session's context
  remote-agent plan <id> implement|clear|keep
                                           answer "Implement this plan?"
  remote-agent doctor [codex|claude]       check that the installed CLIs work with remote-agent
  remote-agent account join|secret|status
                                           connect this machine to a server

Session IDs may be abbreviated to any unique prefix.
`

func main() {
	if len(os.Args) == 2 && (os.Args[1] == "--version" || os.Args[1] == "version") {
		fmt.Println("remote-agent", buildinfo.String())
		return
	}
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	if cmd != "update" && cmd != "update-build-info" && cmd != "help" && cmd != "-h" && cmd != "--help" {
		if err = updater.EnsureState(paths.Dir()); err != nil {
			fmt.Fprintln(os.Stderr, "remote-agent:", err)
			os.Exit(1)
		}
	}
	switch cmd {
	case "update":
		err = runUpdate(args)
	case "update-build-info":
		err = printUpdateJSON(updater.Build())
	case "schedule":
		err = runSchedule(args)
	case "mcp":
		err = runMCP(args)
	case "daemon":
		err = runDaemon(args)
	case "codex":
		err = runAgent(pb.Agent_AGENT_CODEX, args, true)
	case "claude":
		err = runAgent(pb.Agent_AGENT_CLAUDE, args, true)
	case "new":
		err = runNew(args)
	case "ls", "list":
		err = runList(args)
	case "attach":
		err = runAttach(args)
	case "cancel":
		err = runCancel(args)
	case "resume-input":
		err = runResumeInput(args)
	case "send":
		err = runSend(args)
	case "events":
		err = runEvents(args)
	case "approve":
		err = runResolve(args, true)
	case "deny":
		err = runResolve(args, false)
	case "answer":
		err = runAnswer(args)
	case "interrupt":
		err = runInterrupt(args)
	case "stop":
		err = runStop(args)
	case "resume":
		err = runResume(args)
	case "fork":
		err = runFork(args)
	case "model":
		err = runModel(args)
	case "mode":
		err = runMode(args)
	case "rename":
		err = runRename(args)
	case "compact":
		err = runCompact(args)
	case "plan":
		err = runPlan(args)
	case "doctor":
		err = runDoctor(args)
	case "account":
		err = runAccount(args)
	case "hook":
		os.Exit(runHook(args))
	case "statusline":
		os.Exit(runStatusLine(args))
	case "pty-host":
		os.Exit(runPTYHost(args))
	case "help", "-h", "--help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "remote-agent: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "remote-agent:", terminalText(err.Error(), true))
		os.Exit(1)
	}
}
