package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/Lewin671/remote-agent/internal/server"
)

const adminUsage = `Usage: remote-agent-server admin [-db FILE] <command>

  accounts                 list the accounts, with what each stores
  suspend <account-id>     stop an account's devices; what it stored is kept
  resume <account-id>      undo suspend
  plan <account-id> owner|trial
                           move an account to another plan
  delete <account-id>      delete an account and everything stored for it

Run it next to the server, for example:
  docker compose exec remote-agent-server /remote-agent-server admin -db /data/remote-agent.db accounts
`

func runAdmin(args []string) int {
	fs := flag.NewFlagSet("admin", flag.ExitOnError)
	db := fs.String("db", "remote-agent.db", "the relay's SQLite database file")
	fs.Usage = func() { fmt.Fprint(os.Stderr, adminUsage) }
	fs.Parse(args)
	args = fs.Args()
	if len(args) == 0 {
		fs.Usage()
		return 2
	}
	a, err := server.OpenAdmin(*db)
	if err != nil {
		fmt.Fprintln(os.Stderr, "admin:", err)
		return 1
	}
	defer a.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	need := func(n int) bool {
		if len(args) != n+1 {
			fs.Usage()
			return false
		}
		return true
	}
	switch args[0] {
	case "accounts":
		list, err := a.Accounts(ctx)
		if err != nil {
			break
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ACCOUNT\tPLAN\tSTATE\tCREATED\tLAST ACTIVE\tDEVICES\tCOMPUTERS\tSESSIONS\tEVENTS\tIMAGES")
		for _, i := range list {
			state := "active"
			if i.Suspended {
				state = "suspended"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%d\t%d\t%d\t%s\t%s\n", i.ID, i.Plan, state,
				i.Created.Format(time.DateOnly), i.LastActive.Format(time.DateTime), i.Devices, i.Machines, i.Sessions, size(i.EventBytes), size(i.ImageBytes))
		}
		w.Flush()
		return 0
	case "suspend", "resume":
		if !need(1) {
			return 2
		}
		err = a.SetSuspended(ctx, args[1], args[0] == "suspend")
	case "plan":
		if !need(2) {
			return 2
		}
		err = a.SetPlan(ctx, args[1], args[2])
	case "delete":
		if !need(1) {
			return 2
		}
		err = a.Delete(ctx, args[1])
	default:
		fs.Usage()
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "admin:", err)
		return 1
	}
	return 0
}

func size(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
