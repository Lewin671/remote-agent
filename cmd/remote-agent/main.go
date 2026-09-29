// Command remote-agent runs the local daemon and its command-line tools.
package main

import (
	"fmt"
	"os"
)

const usage = `remote-agent: operate native AI coding CLIs running in tmux

Usage:
  remote-agent daemon [flags]              run the daemon in the foreground
  remote-agent codex [codex args]          start a Codex session here and attach to it
  remote-agent new [codex args]            start a Codex session here without attaching
  remote-agent ls [-a]                     list sessions
  remote-agent attach <id>                 attach to a session
  remote-agent send <id> <text>            send a message ("-" reads stdin)
  remote-agent events <id> [-f] [--json]   show the session's events
  remote-agent approve <id> <approval-id>  allow a permission request
  remote-agent deny <id> <approval-id> [-m reason]
  remote-agent answer <id> <question-id> <option>
  remote-agent interrupt <id>              interrupt the running turn
  remote-agent stop <id>                   end the session
  remote-agent resume <id>                 start an ended session again
  remote-agent model [<id> <model> <effort>]
                                           list models, or switch a session's model
  remote-agent mode <id> ask|auto|full|plan switch the approval mode
  remote-agent compact <id>                compact the session's context
  remote-agent plan <id> implement|clear|keep
                                           answer "Implement this plan?"
  remote-agent doctor                      check that the installed Codex works with remote-agent
  remote-agent account init|join|secret|status
                                           connect this machine to a server

Session IDs may be abbreviated to any unique prefix.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "daemon":
		err = runDaemon(args)
	case "codex":
		err = runCodex(args, true)
	case "new":
		err = runCodex(args, false)
	case "ls", "list":
		err = runList(args)
	case "attach":
		err = runAttach(args)
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
	case "model":
		err = runModel(args)
	case "mode":
		err = runMode(args)
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
		fmt.Fprintln(os.Stderr, "remote-agent:", err)
		os.Exit(1)
	}
}
