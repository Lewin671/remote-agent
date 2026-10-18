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

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"github.com/Lewin671/remote-agent/internal/paths"
	"github.com/Lewin671/remote-agent/internal/relay"
	"golang.org/x/term"
	"rsc.io/qr"
)

const accountUsage = `Usage:
  remote-agent account init --server URL    create the account on a new server
  remote-agent account pair [--web URL]     add a device: show a QR code and a pairing code
  remote-agent account join --server URL --code CODE
  remote-agent account join LINK            join with a pairing code or link from another device
  remote-agent account security-enroll --server URL --code CODE
                                          pin management on a retained device without changing its identity
  remote-agent account security-init        initialize the pinned authority locally (existing accounts)
  remote-agent account security-genesis --output FILE
                                          export signed public genesis for offline relay setup
  remote-agent account secret               print offline decryption backup material (not enrollment authorization)
  remote-agent account status               show this machine's account
`

func runAccount(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, accountUsage)
		os.Exit(2)
	}
	switch args[0] {
	case "security-init":
		a, err := relay.LoadAccount()
		if err != nil {
			return err
		}
		return a.InitSecurityAuthority()
	case "security-genesis":
		fs := flag.NewFlagSet("account security-genesis", flag.ContinueOnError)
		output := fs.String("output", "", "new public genesis file (daemon must be stopped)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *output == "" || fs.NArg() != 0 {
			return errors.New("--output FILE is required")
		}
		a, err := relay.LoadAccount()
		if err != nil {
			return err
		}
		genesis, err := a.PublicSecurityGenesis()
		if err != nil {
			return err
		}
		f, err := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		_, err = f.Write(genesis)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		return closeErr
	case "security-enroll":
		return runAccountPairJoin(args[1:], true)
	case "init":
		return runAccountInit(args[1:])
	case "join":
		return runAccountJoin(args[1:])
	case "secret":
		return runAccountSecret()
	case "pair":
		return runAccountPair(args[1:])
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
	// The relay says what it needs: its setup token, asked for only then,
	// or on a relay open to anyone, a proof of work, solved here.
	ask := func() (string, error) { return readLine("Setup token (RA_SETUP_TOKEN on the server): ") }
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	a, err := relay.CreateAccount(ctx, *server, os.Getenv("RA_SETUP_TOKEN"), ask)
	if err != nil {
		return explainJoin(err)
	}
	fmt.Printf("Created the account on %s; this machine is %s.\n", a.Server, a.DeviceID)
	fmt.Println("The running daemon connects within a few seconds.")
	fmt.Println("To add a client or another computer, run `remote-agent account pair` and approve it on this device.")
	return nil
}

func runAccountJoin(args []string) error { return runAccountPairJoin(args, false) }
func runAccountPairJoin(args []string, existing bool) error {
	fs := flag.NewFlagSet("account join", flag.ExitOnError)
	server := fs.String("server", "", "server URL, with --code")
	code := fs.String("code", "", "pairing code shown by the other device")
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
		return joinWithSecret(ctx)
	}
	fmt.Println("Waiting for the other device…")
	join := relay.JoinByPairing
	if existing {
		join = relay.EnrollByPairing
	}
	a, err := join(ctx, link, func(emoji []string) bool {
		fmt.Printf("\nCheck that the management device shows the same four pictures:\n\n    %s\n\n", strings.Join(emoji, "  "))
		answer, _ := readVisibleLine("Do all four match? Trust this management device [y/N]: ")
		return strings.EqualFold(strings.TrimSpace(answer), "y")
	})
	if err != nil {
		return explainJoin(err)
	}
	fmt.Printf("Joined the account on %s; this machine is %s.\n", a.Server, a.DeviceID)
	fmt.Println("The running daemon connects within a few seconds.")
	return nil
}

func joinWithSecret(ctx context.Context) error {
	return errors.New("an account secret cannot authorize a new device; use account pair on an enrolled device, then account join --server URL --code CODE")
}

func runAccountPair(args []string) error {
	fs := flag.NewFlagSet("account pair", flag.ExitOnError)
	web := fs.String("web", "", "web client address for the QR code, e.g. https://app.example.com (remembered)")
	fs.Parse(args)
	a, err := relay.LoadAccount()
	if err != nil {
		return err
	}
	if *web != "" {
		a.Web = strings.TrimRight(*web, "/")
		if err := a.Save(); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	err = relay.Initiate(ctx, a, func(l relay.PairLink) {
		if a.Web != "" {
			u := l.URL(a.Web)
			printQR(u)
			fmt.Printf("Scan the code with the new device, or open:\n  %s\n\n", u)
		}
		fmt.Printf("Pairing code: %s %s  (expires in 5 minutes)\n", l.Code[:4], l.Code[4:])
		fmt.Printf("On another computer: remote-agent account join --server %s --code %s\n\n", l.Server, l.Code)
		fmt.Println("Waiting for the new device…")
	}, func(emoji []string, kind pb.DeviceKind) bool {
		what := "A browser"
		if kind == pb.DeviceKind_DEVICE_KIND_MACHINE {
			what = "A computer"
		}
		fmt.Printf("\n%s asks to join. It should show these four pictures:\n\n    %s\n\n", what, strings.Join(emoji, "  "))
		answer, _ := readVisibleLine("Do they match? Allow it to join [y/N]: ")
		return strings.EqualFold(strings.TrimSpace(answer), "y")
	})
	if err != nil {
		return explainJoin(err)
	}
	fmt.Println("The new device joined the account.")
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
	fmt.Fprintln(os.Stderr, "This key can decrypt account content but cannot authorize device enrollment. Keep it offline; add devices through account pair.")
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
	fmt.Printf("server:   %s\nmachine:  %s\naccount:  %s\naccount-id: %s\nfile:     %s\n", a.Server, a.DeviceID, hex.EncodeToString(sum[:6]), e2e.AccountID(keys.AccountPublicKey()), paths.Account())
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
