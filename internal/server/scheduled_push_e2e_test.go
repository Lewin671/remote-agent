package server

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Opt-in acceptance: builds via dev-env, then uses an otherwise normal local
// relay with a test-only HTTP transport. No browser push service is contacted.
func TestScheduledWebPushDaemonE2E(t *testing.T) {
	if os.Getenv("RA_RUN_SCHEDULE_PUSH_E2E") != "1" {
		t.Skip("requires real Claude CLI; set RA_RUN_SCHEDULE_PUSH_E2E=1")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	// macOS Unix socket paths are limited to 104 bytes.
	dir, err := os.MkdirTemp("/tmp", "ra-push-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	env := append(os.Environ(), "RA_DEV_DIR="+dir)
	harness := filepath.Join(root, "ios/scripts/dev-env.sh")
	start := exec.Command(harness, "start", "--no-account")
	start.Env = env
	if _, err := start.CombinedOutput(); err != nil {
		t.Fatalf("isolated environment start: %v", err)
	}
	t.Cleanup(func() {
		cmd := exec.Command(harness, "stop")
		cmd.Env = env
		if _, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	s, err := New(Config{DBPath: filepath.Join(dir, "push-relay.db"), SetupToken: "test-setup"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	receiver, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		t.Fatal(err)
	}
	payloadFile := filepath.Join(dir, "push.json")
	s.push.client = &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		payload, err := openTestWebPush(receiver, auth, body)
		if err != nil {
			return nil, err
		}
		// No task name or summary may appear even after the outer encryption.
		if bytes.Contains(payload, []byte("Completion notification acceptance")) || bytes.Contains(payload, []byte("Completion fallback verified.")) {
			t.Error("plaintext content in Web Push payload")
		}
		if err := os.WriteFile(payloadFile+".tmp", payload, 0600); err != nil {
			return nil, err
		}
		if err := os.Rename(payloadFile+".tmp", payloadFile); err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: http.StatusCreated, Body: http.NoBody, Request: req}, nil
	})}
	hs := httptest.NewServer(s.Handler())
	defer hs.Close()
	for name, value := range map[string]string{"url": hs.URL, "setup-token": "test-setup"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	sub, err := json.Marshal(map[string]any{"endpoint": "https://push.example.com/acceptance", "keys": map[string]string{
		"p256dh": base64.RawURLEncoding.EncodeToString(receiver.PublicKey().Bytes()), "auth": base64.RawURLEncoding.EncodeToString(auth),
	}})
	if err != nil {
		t.Fatal(err)
	}
	subFile := filepath.Join(dir, "subscription.json")
	if err := os.WriteFile(subFile, sub, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "run", "./scripts/acceptance-schedule", "--finish-notify")
	cmd.Dir, cmd.Env = root, append(env, "RA_DEV_WEB_PUSH_SUBSCRIPTION="+subFile, "RA_DEV_WEB_PUSH_PAYLOAD="+payloadFile)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("scheduled Web Push acceptance: %v\n%s", err, out)
	}
	t.Log(string(out))
}

// RFC 8291 single-record receiver for the test transport, independent of the
// sender implementation. All keys here belong to a disposable subscription.
func openTestWebPush(receiver *ecdh.PrivateKey, auth, body []byte) ([]byte, error) {
	if len(body) < 86+17 || body[20] != 65 {
		return nil, io.ErrUnexpectedEOF
	}
	sender, err := ecdh.P256().NewPublicKey(body[21:86])
	if err != nil {
		return nil, err
	}
	shared, err := receiver.ECDH(sender)
	if err != nil {
		return nil, err
	}
	info := append(append([]byte("WebPush: info\x00"), receiver.PublicKey().Bytes()...), sender.Bytes()...)
	ikm, err := hkdf.Key(sha256.New, shared, auth, string(info), 32)
	if err != nil {
		return nil, err
	}
	key, err := hkdf.Key(sha256.New, ikm, body[:16], "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Key(sha256.New, ikm, body[:16], "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, nonce, body[86:], nil)
	if err != nil {
		return nil, err
	}
	plain = bytes.TrimRight(plain, "\x00")
	if len(plain) == 0 || plain[len(plain)-1] != 2 {
		return nil, io.ErrUnexpectedEOF
	}
	return plain[:len(plain)-1], nil
}
