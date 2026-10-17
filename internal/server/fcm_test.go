package server

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"google.golang.org/protobuf/proto"
)

// fakeFCM stands in for Google's token endpoint and the FCM send API.
type fakeFCM struct {
	t      *testing.T
	key    *rsa.PublicKey
	srv    *httptest.Server
	mu     sync.Mutex
	sent   []map[string]string
	gone   bool
	tokens int
	// Statuses to answer the next sends with, before succeeding, and the
	// Retry-After to send with them.
	fail       []int
	retryAfter string
	sendTimes  []time.Time
	account    string
}

func newFakeFCM(t *testing.T) *fakeFCM {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeFCM{t: t, key: &priv.PublicKey}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	der, _ := x509.MarshalPKCS8PrivateKey(priv)
	sa, _ := json.Marshal(map[string]string{
		"type": "service_account", "project_id": "test-project", "client_email": "push@test-project.iam.gserviceaccount.com",
		"private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), "token_uri": f.srv.URL + "/token",
	})
	f.account = filepath.Join(t.TempDir(), "sa.json")
	if err := os.WriteFile(f.account, sa, 0o600); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fakeFCM) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case "/token":
		// The assertion must be signed by the service account's key.
		r.ParseForm()
		parts := strings.Split(r.Form.Get("assertion"), ".")
		sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
		sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		if len(parts) != 3 || rsa.VerifyPKCS1v15(f.key, crypto.SHA256, sum[:], sig) != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.tokens++
		json.NewEncoder(w).Encode(map[string]any{"access_token": fmt.Sprintf("access-%d", f.tokens), "expires_in": 3600})
	case "/send":
		f.sendTimes = append(f.sendTimes, time.Now())
		if len(f.fail) > 0 {
			code := f.fail[0]
			f.fail = f.fail[1:]
			if f.retryAfter != "" {
				w.Header().Set("Retry-After", f.retryAfter)
			}
			w.WriteHeader(code)
			return
		}
		if r.Header.Get("Authorization") != fmt.Sprintf("Bearer access-%d", f.tokens) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body struct {
			Message struct {
				Token   string            `json:"token"`
				Data    map[string]string `json:"data"`
				Android struct {
					Priority string `json:"priority"`
				} `json:"android"`
			} `json:"message"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if f.gone {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":{"status":"NOT_FOUND","details":[{"errorCode":"UNREGISTERED"}]}}`))
			return
		}
		body.Message.Data["token"] = body.Message.Token
		body.Message.Data["priority"] = body.Message.Android.Priority
		f.sent = append(f.sent, body.Message.Data)
		w.Write([]byte(`{"name":"projects/test-project/messages/1"}`))
	}
}

func (f *fakeFCM) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestFCMPush(t *testing.T) {
	fake := newFakeFCM(t)
	s, err := New(Config{DBPath: filepath.Join(t.TempDir(), "ra.db"), SetupToken: "setup", FCMServiceAccount: fake.account})
	if err != nil {
		t.Fatal(err)
	}
	s.push.fcm.sendURL = fake.srv.URL + "/send"
	hs := httptest.NewServer(s.Handler())
	t.Cleanup(func() { hs.Close(); s.Close() })
	keys, _ := e2e.Derive(e2e.NewKey())
	ts := &testServer{srv: s, url: hs.URL, keys: keys, auth: remoteagentv1connect.NewAuthServiceClient(http.DefaultClient, hs.URL)}

	mpriv, mid, err := ts.register(t, keys, pb.DeviceKind_DEVICE_KIND_MACHINE, "setup")
	if err != nil {
		t.Fatal(err)
	}
	cpriv, cid, _ := ts.register(t, keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "")
	ctx := context.Background()
	m := remoteagentv1connect.NewMachineServiceClient(http.DefaultClient, hs.URL, withToken(ts.login(t, mpriv, mid)))
	c := remoteagentv1connect.NewClientServiceClient(http.DefaultClient, hs.URL, withToken(ts.login(t, cpriv, cid)))
	skey := e2e.NewKey()
	keyEnv, _ := e2e.Seal(keys.Wrap, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_SESSION_KEY, MachineId: mid, SessionId: "s"}, skey)
	if _, err := m.PutSession(ctx, connect.NewRequest(&pb.PutSessionRequest{SessionId: "s", Key: keyEnv})); err != nil {
		t.Fatal(err)
	}

	res, err := c.SetPushTarget(ctx, connect.NewRequest(&pb.SetPushTargetRequest{
		Type: pb.PushType_PUSH_TYPE_FCM, Target: "fcm-token-1", Kinds: []pb.NotifyKind{pb.NotifyKind_NOTIFY_KIND_ATTENTION},
	}))
	if err != nil || !res.Msg.Enabled {
		t.Fatalf("set target: %v %v", res, err)
	}

	notifyKey := e2e.NewKey()
	content, _ := e2e.SealMessage(notifyKey, &pb.Header{
		Kind: pb.PayloadKind_PAYLOAD_KIND_NOTIFY, MachineId: mid, SessionId: "s", RequestId: "appr-1", Sender: mid,
	}, &pb.NotifyContent{SessionId: "s", Approval: &pb.NotifyApproval{ApprovalId: "appr-1", Command: "go test ./..."}})
	notify := func(key string, env *pb.Envelope) error {
		_, err := m.Notify(ctx, connect.NewRequest(&pb.NotifyRequest{SessionId: "s", Kind: pb.NotifyKind_NOTIFY_KIND_ATTENTION, Content: env, DedupeKey: key}))
		return err
	}
	if err := notify("appr-1", content); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "an FCM message", func() bool { return fake.count() == 1 })
	got := fake.sent[0]
	if got["token"] != "fcm-token-1" || got["session"] != "s" || got["kind"] != "1" || got["priority"] != "high" {
		t.Fatalf("message %v", got)
	}
	raw, _ := base64.StdEncoding.DecodeString(got["content"])
	var env pb.Envelope
	if err := proto.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var nc pb.NotifyContent
	if _, err := e2e.OpenMessage(notifyKey, &env, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_NOTIFY, SessionId: "s"}, &nc); err != nil || nc.Approval.Command != "go test ./..." {
		t.Fatalf("content does not open: %v", err)
	}

	// The same approval is pushed once; another one right after it is not
	// swallowed by the per-session coalescing.
	if err := notify("appr-1", content); err != nil {
		t.Fatal(err)
	}
	if err := notify("appr-2", nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the second approval", func() bool { return fake.count() == 2 })
	time.Sleep(50 * time.Millisecond)
	if fake.count() != 2 || fake.sent[1]["content"] != "" {
		t.Fatalf("sent %d, second %v", fake.count(), fake.sent[1])
	}
	if fake.tokens != 1 {
		t.Errorf("fetched %d access tokens, want 1", fake.tokens)
	}

	// Content for another session is refused.
	other, _ := e2e.SealMessage(notifyKey, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_NOTIFY, MachineId: mid, SessionId: "t"}, &pb.NotifyContent{})
	if err := notify("appr-3", other); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("misplaced content: %v", err)
	}

	// A token FCM no longer knows is deleted.
	fake.mu.Lock()
	fake.gone = true
	fake.mu.Unlock()
	if err := notify("appr-4", nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the target to be deleted", func() bool {
		var n int
		s.store.db.QueryRow(`SELECT COUNT(*) FROM fcm_targets`).Scan(&n)
		return n == 0
	})
}

func TestFCMOff(t *testing.T) {
	ts := newTestServer(t)
	_, _, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_MACHINE, "setup")
	if err != nil {
		t.Fatal(err)
	}
	cpriv, cid, _ := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "")
	c := remoteagentv1connect.NewClientServiceClient(http.DefaultClient, ts.url, withToken(ts.login(t, cpriv, cid)))
	res, err := c.SetPushTarget(context.Background(), connect.NewRequest(&pb.SetPushTargetRequest{Type: pb.PushType_PUSH_TYPE_FCM, Target: "tok"}))
	if err != nil || res.Msg.Enabled {
		t.Fatalf("without FCM: %v %v", res, err)
	}
	if _, err := New(Config{DBPath: filepath.Join(t.TempDir(), "x.db"), FCMServiceAccount: filepath.Join(t.TempDir(), "missing.json")}); err == nil {
		t.Fatal("a missing service account file should fail")
	}
}

func TestFCMRetriesTransientFailures(t *testing.T) {
	fake := newFakeFCM(t)
	f, err := loadFCM(fake.account)
	if err != nil {
		t.Fatal(err)
	}
	f.sendURL = fake.srv.URL + "/send"
	f.retryBase = 5 * time.Millisecond
	ctx := context.Background()

	// Overloaded, then an access token FCM no longer accepts: both are
	// retried, the second with a fresh token.
	fake.fail = []int{http.StatusServiceUnavailable, http.StatusUnauthorized}
	if err := f.sendRetrying(ctx, "tok", map[string]string{"session": "s"}, time.Minute); err != nil {
		t.Fatal(err)
	}
	if fake.count() != 1 || fake.tokens != 2 {
		t.Fatalf("sent %d with %d access tokens", fake.count(), fake.tokens)
	}

	// A lasting failure gives up after a few attempts.
	fake.fail = []int{500, 500, 500, 500, 500}
	if err := f.sendRetrying(ctx, "tok", map[string]string{}, time.Minute); err == nil {
		t.Fatal("expected an error")
	}
	if len(fake.fail) != 1 {
		t.Fatalf("%d attempts, want %d", 5-len(fake.fail), fcmAttempts)
	}

	// A token FCM does not know is not retried.
	fake.fail = nil
	fake.gone = true
	if err := f.sendRetrying(ctx, "tok", map[string]string{}, time.Minute); !errors.Is(err, errFCMToken) {
		t.Fatalf("err = %v", err)
	}
}

func TestFCMRespectsRetryAfter(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	for v, want := range map[string]time.Duration{
		"120": 120 * time.Second,
		"0":   0,
		now.Add(90 * time.Second).Format(http.TimeFormat): 90 * time.Second,
		"garbage": 0,
		"":        0,
	} {
		if got := retryAfter(v, now); got != want {
			t.Errorf("retryAfter(%q) = %v, want %v", v, got, want)
		}
	}

	fake := newFakeFCM(t)
	f, err := loadFCM(fake.account)
	if err != nil {
		t.Fatal(err)
	}
	f.sendURL = fake.srv.URL + "/send"
	f.retryBase = time.Millisecond

	// A wait longer than our own backoff cap is honoured, not shortened.
	fake.fail = []int{http.StatusTooManyRequests}
	fake.retryAfter = "1"
	if err := f.sendRetrying(context.Background(), "tok", map[string]string{}, time.Minute); err != nil {
		t.Fatal(err)
	}
	if gap := fake.sendTimes[1].Sub(fake.sendTimes[0]); gap < time.Second {
		t.Fatalf("retried after %v, before FCM's Retry-After", gap)
	}

	// A wait that does not fit in the time left ends the retries at once.
	fake.fail = []int{http.StatusServiceUnavailable, http.StatusServiceUnavailable}
	fake.retryAfter = "120"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	before := len(fake.sendTimes)
	start := time.Now()
	if err := f.sendRetrying(ctx, "tok", map[string]string{}, time.Minute); err == nil {
		t.Fatal("expected an error")
	}
	if n := len(fake.sendTimes) - before; n != 1 || time.Since(start) > time.Second {
		t.Fatalf("%d sends in %v; want one and no early retry", n, time.Since(start))
	}
}
