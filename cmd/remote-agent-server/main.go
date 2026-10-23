// Command remote-agent-server relays sealed messages between remote-agent
// daemons and clients. Put it behind a TLS-terminating reverse proxy.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Lewin671/remote-agent/internal/buildinfo"
	"github.com/Lewin671/remote-agent/internal/server"

	// The server runs in a scratch image, which has no CA certificates.
	// Web Push and FCM need them, so the binary carries Mozilla's roots
	// and uses them when the system has none.
	_ "golang.org/x/crypto/x509roots/fallback"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println("remote-agent-server", buildinfo.String())
		return
	}
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "admin":
			os.Exit(runAdmin(os.Args[2:]))
		case "healthcheck":
			os.Exit(runHealthcheck(os.Args[2:]))
		}
	}
	listen := flag.String("listen", "127.0.0.1:8080", "address to listen on")
	db := flag.String("db", "remote-agent.db", "SQLite database file")
	signup := flag.String("signup", envOr("RA_SIGNUP", "token"), "who may create accounts (RA_SIGNUP): token, only with RA_SETUP_TOKEN; open, anyone, with a proof of work")
	maxAccounts := flag.Int("max-accounts", envInt("RA_MAX_ACCOUNTS", 0), "most accounts on this relay (RA_MAX_ACCOUNTS); 0 means 1 with token signup, 100 with open")
	trusted := flag.String("trusted-proxies", os.Getenv("RA_TRUSTED_PROXIES"), "comma-separated CIDRs of reverse proxies whose X-Forwarded-For names the client (RA_TRUSTED_PROXIES)")
	minFree := flag.Int64("min-free-bytes", 2<<30, "free disk below which accounts stop growing; 0 turns the check off")
	origins := flag.String("allowed-origins", "", "comma-separated web origins allowed to use the API, e.g. http://localhost:5173")
	fcmAccount := flag.String("fcm-service-account", os.Getenv("RA_FCM_SERVICE_ACCOUNT"), "Firebase service account key file for Android pushes (RA_FCM_SERVICE_ACCOUNT); empty turns them off")
	retention := flag.Duration("image-retention", 30*24*time.Hour, "how long image display copies are kept")
	quota := flag.Int64("image-quota", 1<<30, "bytes of image display copies the account may store")
	flag.Parse()

	var allowed []string
	for _, o := range strings.Split(*origins, ",") {
		if o = strings.TrimSpace(o); o != "" {
			allowed = append(allowed, o)
		}
	}
	var proxies []netip.Prefix
	for _, c := range strings.Split(*trusted, ",") {
		if c = strings.TrimSpace(c); c == "" {
			continue
		}
		p, err := netip.ParsePrefix(c)
		if err != nil {
			fmt.Fprintln(os.Stderr, "remote-agent-server: --trusted-proxies:", err)
			os.Exit(2)
		}
		proxies = append(proxies, p)
	}
	srv, err := server.New(server.Config{
		DBPath:         *db,
		SetupToken:     os.Getenv("RA_SETUP_TOKEN"),
		Signup:         server.Signup(*signup),
		MaxAccounts:    *maxAccounts,
		TrustedProxies: proxies,
		MinFreeBytes:   *minFree,
		AllowedOrigins: allowed,

		FCMServiceAccount: *fcmAccount,
		ImageRetention:    *retention,
		ImageQuota:        *quota,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "remote-agent-server:", err)
		os.Exit(1)
	}
	defer srv.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	hs := &http.Server{Addr: *listen, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, "remote-agent-server:", err)
		_ = srv.Close()
		os.Exit(1)
	}
	slog.Info("listening", "version", buildinfo.Version, "commit", buildinfo.Commit, "addr", *listen, "allowed_origins", allowed, "signup", *signup)
	if err := serveHTTP(ctx, hs, ln); err != nil {
		fmt.Fprintln(os.Stderr, "remote-agent-server:", err)
		_ = srv.Close()
		os.Exit(1)
	}
}

// serveHTTP waits for the active HTTP requests to drain before its caller
// closes the relay and storage. Hijacked streams are owned by Server.Close.
func serveHTTP(ctx context.Context, hs *http.Server, ln net.Listener) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		<-ctx.Done()
		sctx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		err := hs.Shutdown(sctx)
		if err != nil {
			_ = hs.Close()
		}
		done <- err
	}()
	err := hs.Serve(ln)
	cancel()
	shutdownErr := <-done
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return shutdownErr
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return v
	}
	return def
}

// runHealthcheck asks the running server whether it answers, for the
// container's health check: the image has no shell or curl.
func runHealthcheck(args []string) int {
	fs := flag.NewFlagSet("healthcheck", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:8080", "the server's listen address")
	fs.Parse(args)
	c := &http.Client{Timeout: 5 * time.Second}
	res, err := c.Get("http://" + *addr + "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "healthcheck: status", res.StatusCode)
		return 1
	}
	return 0
}
