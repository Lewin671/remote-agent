package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Lewin671/remote-agent/internal/e2e"
	"github.com/Lewin671/remote-agent/internal/paths"
	"github.com/Lewin671/remote-agent/internal/relay"
	"golang.org/x/term"
)

const accountUsage = `Usage:
  remote-agent account init --server URL    create the account on a new server
  remote-agent account join                 join an existing account (reads its secret from stdin)
  remote-agent account secret               print the account secret for another device
  remote-agent account status               show this machine's account
`

func runAccount(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, accountUsage)
		os.Exit(2)
	}
	switch args[0] {
	case "init":
		return runAccountInit(args[1:])
	case "join":
		return runAccountJoin(args[1:])
	case "secret":
		return runAccountSecret()
	case "status":
		return runAccountStatus()
	}
	fmt.Fprint(os.Stderr, accountUsage)
	os.Exit(2)
	return nil
}

func runAccountInit(args []string) error {
	fs := flag.NewFlagSet("account init", flag.ExitOnError)
	server := fs.String("server", "", "server URL, e.g. https://ra.example.com")
	fs.Parse(args)
	if *server == "" {
		return errors.New("--server is required")
	}
	token := os.Getenv("RA_SETUP_TOKEN")
	if token == "" {
		var err error
		if token, err = readLine("Setup token (RA_SETUP_TOKEN on the server): "); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	a, err := relay.Join(ctx, *server, e2e.NewKey(), token)
	if err != nil {
		return explainJoin(err)
	}
	fmt.Printf("Created the account on %s; this machine is %s.\n", a.Server, a.DeviceID)
	fmt.Println("The running daemon connects within a few seconds.")
	fmt.Println("To add the web client or another computer, run `remote-agent account secret`.")
	return nil
}

func runAccountJoin(args []string) error {
	text, err := readLine("Account secret (from `remote-agent account secret`): ")
	if err != nil {
		return err
	}
	sec, err := e2e.ParseSecret(text)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	a, err := relay.Join(ctx, sec.Server, sec.Root, "")
	if err != nil {
		return explainJoin(err)
	}
	fmt.Printf("Joined the account on %s; this machine is %s.\n", a.Server, a.DeviceID)
	fmt.Println("The running daemon connects within a few seconds.")
	return nil
}

func explainJoin(err error) error {
	return fmt.Errorf("could not register with the server: %w", explain(err))
}

func runAccountSecret() error {
	a, err := relay.LoadAccount()
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "This secret gives full access to all your sessions. Paste it only into your own devices, then clear it from the clipboard.")
	fmt.Println(a.Secret().String())
	return nil
}

func runAccountStatus() error {
	a, err := relay.LoadAccount()
	if err != nil {
		return err
	}
	keys, err := e2e.Derive(a.Root)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(keys.AccountPublicKey())
	fmt.Printf("server:   %s\nmachine:  %s\naccount:  %s\nfile:     %s\n", a.Server, a.DeviceID, hex.EncodeToString(sum[:6]), paths.Account())
	return nil
}

// readLine prompts on stderr and reads one line without echo when stdin
// is a terminal, so secrets stay out of shell history and scrollback.
func readLine(prompt string) (string, error) {
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		fmt.Fprint(os.Stderr, prompt)
		b, err := term.ReadPassword(fd)
		fmt.Fprintln(os.Stderr)
		return strings.TrimSpace(string(b)), err
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
