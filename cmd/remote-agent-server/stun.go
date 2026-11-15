package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/Lewin671/remote-agent/internal/i18n"
	"github.com/Lewin671/remote-agent/internal/stun"
)

// The STUN subcommand runs as a separate service. It never opens account state,
// enables HTTP request logging, or starts the relay's background components.
func runSTUN(args []string) int {
	locale := i18n.Resolve("", []string{os.Getenv("LC_ALL"), os.Getenv("LC_MESSAGES"), os.Getenv("LANG")})
	flags := flag.NewFlagSet("stun", flag.ContinueOnError)
	config := stun.DefaultConfig()
	listen := flags.String("listen", ":3478", i18n.TransportStunListen(locale))
	flags.IntVar(&config.PacketsPerSecond, "rate", config.PacketsPerSecond, i18n.TransportStunRate(locale))
	flags.IntVar(&config.SourcePacketsPerSecond, "source-rate", config.SourcePacketsPerSecond, i18n.TransportStunSourceRate(locale))
	flags.IntVar(&config.BytesPerSecond, "bytes", config.BytesPerSecond, i18n.TransportStunBytes(locale))
	flags.IntVar(&config.MaxSources, "sources", config.MaxSources, i18n.TransportStunSources(locale))
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(os.Stderr, i18n.TransportStunInvalid(locale))
		return 2
	}
	srv := stun.New(config)
	if srv == nil {
		fmt.Fprintln(os.Stderr, i18n.TransportStunInvalid(locale))
		return 2
	}
	address, err := net.ResolveUDPAddr("udp", *listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, i18n.TransportStunFailed(locale))
		return 1
	}
	conn, err := net.ListenUDP("udp", address)
	if err != nil {
		fmt.Fprintln(os.Stderr, i18n.TransportStunFailed(locale))
		return 1
	}
	defer conn.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err = srv.Serve(ctx, conn); err != nil {
		fmt.Fprintln(os.Stderr, i18n.TransportStunFailed(locale))
		return 1
	}
	return 0
}
