// relaytestserver runs the real relay on a kernel-assigned loopback port.
// Its readiness record names the listener this process actually owns, so
// integration tests cannot accidentally connect to another test's relay.
package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Lewin671/remote-agent/internal/server"
)

func main() {
	addr := flag.String("listen", "127.0.0.1:0", "test loopback listener")
	db := flag.String("db", "", "isolated test database")
	signup := flag.String("signup", "token", "test signup mode")
	bits := flag.Uint("signup-bits", 0, "test proof difficulty")
	flag.Parse()
	if *db == "" {
		panic("test database is required")
	}
	s, err := server.New(server.Config{DBPath: *db, SetupToken: os.Getenv("RA_SETUP_TOKEN"), Signup: server.Signup(*signup), SignupBits: *bits, ImageRetention: 30 * 24 * time.Hour, ImageQuota: 1 << 30})
	if err != nil {
		panic(err)
	}
	defer s.Close()
	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		panic(err)
	}
	defer listener.Close()
	hs := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hs.Shutdown(ctx)
	}()
	fmt.Println("TEST_SERVER_URL=http://" + listener.Addr().String())
	if err := hs.Serve(listener); err != nil && err != http.ErrServerClosed {
		panic(err)
	}
}
