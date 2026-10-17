package relay_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/Lewin671/remote-agent/internal/relay"
	"github.com/Lewin671/remote-agent/internal/server"
)

func TestCreateAccountWithProofOfWork(t *testing.T) {
	e := setupWithHandler(t, func(h http.Handler) http.Handler { return h }, func(c *server.Config) { c.Signup, c.SignupBits, c.MaxAccounts = server.SignupOpen, 8, 10 })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	asked := false
	acct, err := relay.CreateAccount(ctx, e.http.URL, "", func() (string, error) { asked = true; return "", nil })
	if err != nil {
		t.Fatal(err)
	}
	if asked || acct.DeviceID == "" || acct.DeviceID == e.acct.DeviceID {
		t.Fatalf("signed up: asked=%v %+v", asked, acct)
	}
}

func TestCreateAccountAsksForTheSetupToken(t *testing.T) {
	e := setupWithHandler(t, func(h http.Handler) http.Handler { return h }, func(c *server.Config) { c.MaxAccounts = 10 })
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	if _, err := relay.CreateAccount(ctx, e.http.URL, "", func() (string, error) { return "wrong", nil }); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("wrong token: %v", err)
	}
	asks := 0
	if _, err := relay.CreateAccount(ctx, e.http.URL, "", func() (string, error) { asks++; return "setup", nil }); err != nil || asks != 1 {
		t.Fatalf("asked %d times: %v", asks, err)
	}
}
