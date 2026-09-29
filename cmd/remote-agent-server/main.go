// Command remote-agent-server relays sealed messages between remote-agent
// daemons and clients. Put it behind a TLS-terminating reverse proxy.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Lewin671/remote-agent/internal/server"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8080", "address to listen on")
	db := flag.String("db", "remote-agent.db", "SQLite database file")
	origins := flag.String("allowed-origins", "", "comma-separated web origins allowed to use the API, e.g. http://localhost:5173")
	fcmAccount := flag.String("fcm-service-account", os.Getenv("RA_FCM_SERVICE_ACCOUNT"), "Firebase service account key file for Android pushes (RA_FCM_SERVICE_ACCOUNT); empty turns them off")
	flag.Parse()

	var allowed []string
	for _, o := range strings.Split(*origins, ",") {
		if o = strings.TrimSpace(o); o != "" {
			allowed = append(allowed, o)
		}
	}
	srv, err := server.New(server.Config{
		DBPath:         *db,
		SetupToken:     os.Getenv("RA_SETUP_TOKEN"),
		AllowedOrigins: allowed,

		FCMServiceAccount: *fcmAccount,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "remote-agent-server:", err)
		os.Exit(1)
	}
	defer srv.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	hs := &http.Server{Addr: *listen, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hs.Shutdown(sctx)
	}()
	slog.Info("listening", "addr", *listen, "allowed_origins", allowed)
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(os.Stderr, "remote-agent-server:", err)
		os.Exit(1)
	}
}
