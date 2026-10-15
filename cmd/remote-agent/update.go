package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Lewin671/remote-agent/internal/paths"
	"github.com/Lewin671/remote-agent/internal/updater"
)

const updateUsage = `Usage:
  remote-agent update check [--version X.Y.Z]  check the latest stable release
  remote-agent update [--version X.Y.Z]        install a newer compatible release
  remote-agent update status                  show automatic-update and last-result status
  remote-agent update auto enable [--time HH:MM]
  remote-agent update auto disable

Automatic updates are off by default. Times use this computer's local timezone.
Only official release installations managed by remote-agent-service can update.
Both paths require matching protocol/storage generations, release permission,
and 30 seconds without active work or pending interactions. Breaking upgrades
use the release installer and the release notes. tmux sessions are preserved;
remote connections briefly reconnect. Source builds never replace themselves.
`

func printUpdateJSON(v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err == nil {
		fmt.Println(string(b))
	}
	return err
}

func runUpdate(args []string) error {
	if len(args) > 0 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h") {
		fmt.Print(updateUsage)
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	u, err := updater.New(exe, paths.Dir())
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()
	op := "apply"
	if len(args) > 0 && (args[0] == "check" || args[0] == "status" || args[0] == "auto") {
		op, args = args[0], args[1:]
	}
	if op == "status" {
		if len(args) > 0 {
			return errors.New(updateUsage)
		}
		v, err := u.Status()
		if err != nil {
			return err
		}
		return printUpdateJSON(v)
	}
	if op == "auto" {
		if len(args) == 0 || (args[0] != "enable" && args[0] != "disable") {
			return errors.New(updateUsage)
		}
		enabled := args[0] == "enable"
		fs := flag.NewFlagSet("update auto", flag.ContinueOnError)
		at := fs.String("time", "04:00", "daily local time")
		if err = fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 0 || (!enabled && len(args) > 1) {
			return errors.New(updateUsage)
		}
		if err = u.Auto(ctx, enabled, *at); err != nil {
			return err
		}
		v, err := u.Status()
		if err != nil {
			return err
		}
		return printUpdateJSON(v)
	}
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	version := fs.String("version", "", "select a stable release")
	scheduled := fs.Bool("scheduled", false, "run an enabled automatic update")
	helperFor := fs.String("helper-for", "", "retained updater target")
	if err = fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || (*scheduled && (*version != "" || op != "apply")) {
		return errors.New(updateUsage)
	}
	if *helperFor != "" {
		target, e := filepath.EvalSymlinks(*helperFor)
		if e != nil {
			// A transaction can recover a missing target, too.
			target = *helperFor
		}
		target, e = filepath.Abs(target)
		if e != nil {
			return e
		}
		self, e := filepath.EvalSymlinks(exe)
		if e != nil {
			return e
		}
		self, e = filepath.Abs(self)
		if e != nil {
			return e
		}
		if op != "apply" || self != target+".updater" {
			return errors.New("--helper-for is reserved for the retained updater")
		}
		u, err = updater.NewHelper(target, paths.Dir())
		if err != nil {
			return err
		}
	}
	if op == "check" {
		v, err := u.Check(ctx, *version)
		if err != nil {
			return err
		}
		return printUpdateJSON(v)
	}
	v, err := u.Apply(ctx, *version, *scheduled)
	if v.Outcome != "" {
		if e := printUpdateJSON(v); err == nil {
			err = e
		}
	}
	return err
}
