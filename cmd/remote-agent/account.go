package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Lewin671/remote-agent/internal/paths"
	"github.com/Lewin671/remote-agent/internal/relay"
	"golang.org/x/term"
	"rsc.io/qr"
)

const accountUsage = `Usage:
  remote-agent account join --server URL --code CODE
  remote-agent account join LINK            join with a pairing code or link from the phone
  remote-agent account secret               print offline decryption backup material
  remote-agent account status               show this machine's account
`

func runAccount(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, accountUsage)
		os.Exit(2)
	}
	switch args[0] {
	case "join":
		return runAccountJoin(args[1:])
	case "secret":
		return runAccountSecret()
	case "status":
		return runAccountStatus()
	case "dev-init":
		return runAccountDevInit(args[1:])
	}
	fmt.Fprint(os.Stderr, accountUsage)
	os.Exit(2)
	return nil
}

func runAccountJoin(args []string) error {
	fs := flag.NewFlagSet("account join", flag.ExitOnError)
	server := fs.String("server", "", "server URL, with --code")
	code := fs.String("code", "", "pairing code shown by the phone")
	fs.Parse(args)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var link relay.PairLink
	switch {
	case *code != "":
		if *server == "" {
			return errors.New("--code needs --server")
		}
		link = relay.PairLink{Code: *code, Server: *server}
	case fs.NArg() == 1:
		l, err := relay.ParsePairLink(fs.Arg(0))
		if err != nil {
			return err
		}
		link = l
	default:
		return errors.New("add a device in the phone app, then run `account join --server URL --code CODE`")
	}
	fmt.Println("Waiting for the phone…")
	a, err := relay.JoinByPairing(ctx, link, func(emoji []string) bool {
		fmt.Printf("\nCheck that the phone shows the same four pictures:\n\n    %s\n\n", strings.Join(emoji, "  "))
		answer, _ := readVisibleLine("Do all four match? Trust this phone [y/N]: ")
		return strings.EqualFold(strings.TrimSpace(answer), "y")
	})
	if err != nil {
		return explainJoin(err)
	}
	fmt.Printf("Joined the account on %s; this machine is %s.\n", a.Server, a.DeviceID)
	fmt.Println("The running daemon connects within a few seconds.")
	return nil
}

// runAccountDevInit is the test-only account creator used by the isolated
// development environment and the distribution smoke test. It is hidden from
// the usage text and refuses to run without RA_DEV_AUTHORITY=1.
func runAccountDevInit(args []string) error {
	if os.Getenv("RA_DEV_AUTHORITY") != "1" {
		return errors.New("account dev-init is a test helper; set RA_DEV_AUTHORITY=1")
	}
	fs := flag.NewFlagSet("account dev-init", flag.ExitOnError)
	server := fs.String("server", "", "server URL")
	fs.Parse(args)
	if *server == "" {
		return errors.New("--server is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	a, err := relay.DevInit(ctx, *server, os.Getenv("RA_SETUP_TOKEN"))
	if err != nil {
		return explainJoin(err)
	}
	fmt.Printf("Created a development account on %s; this machine is %s.\n", a.Server, a.DeviceID)
	return nil
}

// readVisibleLine prompts and reads a line with echo.
func readVisibleLine(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// printQR draws a QR code with half-block characters.
func printQR(text string) {
	code, err := qr.Encode(text, qr.M)
	if err != nil {
		return
	}
	n := code.Size
	const quiet = 2
	var b strings.Builder
	for y := -quiet; y < n+quiet; y += 2 {
		b.WriteString("  ")
		for x := -quiet; x < n+quiet; x++ {
			top := code.Black(x, y)
			bottom := code.Black(x, y+1)
			switch {
			case top && bottom:
				b.WriteString(" ")
			case top:
				b.WriteString("▄")
			case bottom:
				b.WriteString("▀")
			default:
				b.WriteString("█")
			}
		}
		b.WriteString("\n")
	}
	fmt.Println(b.String())
}

func explainJoin(err error) error {
	return fmt.Errorf("could not register with the server: %w", explain(err))
}

func runAccountSecret() error {
	a, err := relay.LoadAccount()
	if err != nil {
		return err
	}
	secret, err := a.Secret()
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "This key can decrypt account content but cannot authorize device enrollment. Keep it offline; add devices in the phone app.")
	fmt.Println(secret.String())
	return nil
}

func runAccountStatus() error {
	a, err := relay.LoadAccount()
	if err != nil {
		return err
	}
	st, err := relay.AccountStatusInfo(a)
	if err != nil {
		return err
	}
	fmt.Printf("server:     %s\nkind:       %s\ndevice:     %s\naccount-id: %s\nauthority:  %s\nhead epoch: %d\nlease until: %s\nfile:       %s\n",
		st.Server, st.Kind, st.DeviceID, st.AccountID, st.Authority, st.HeadEpoch, formatLeaseExpiration(st.NotAfter), paths.Account())
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

func formatLeaseExpiration(notAfter uint64) string {
	if notAfter == 0 {
		return "not installed for this head"
	}
	return time.UnixMilli(int64(notAfter)).Format(time.RFC3339)
}
