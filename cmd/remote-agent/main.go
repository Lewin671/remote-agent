// Command remote-agent runs the local daemon and its command-line tools.
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/Lewin671/remote-agent/internal/buildinfo"
	"github.com/Lewin671/remote-agent/internal/daemon"
	"github.com/Lewin671/remote-agent/internal/i18n"
	"github.com/Lewin671/remote-agent/internal/paths"
	"github.com/Lewin671/remote-agent/internal/updater"
)

func usage() string {
	ids := strings.Join(daemon.AgentIDs(), "|")
	var agentLines strings.Builder
	for _, a := range daemon.RegisteredAgents {
		article := "a"
		if strings.ContainsAny(strings.ToLower(a.Name[:1]), "aeiou") {
			article = "an"
		}
		fmt.Fprintf(&agentLines, "  remote-agent %-24s start %s %s session here and attach to it\n", a.ID+" ["+a.ID+" args]", article, a.Name)
	}
	return i18n.StartupCliUsage("en", agentLines.String(), ids)
}

func main() {
	if len(os.Args) == 2 && (os.Args[1] == "--version" || os.Args[1] == "version") {
		fmt.Println("remote-agent", buildinfo.String())
		return
	}
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage())
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
		fmt.Print(usage())
		return
	default:
		if spec, ok := daemon.FindAgent(cmd); ok {
			err = runAgent(spec.Kind, args, true)
			break
		}
		fmt.Fprintf(os.Stderr, "remote-agent: unknown command %q\n\n%s", cmd, usage())
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "remote-agent:", terminalText(err.Error(), true))
		os.Exit(1)
	}
}
