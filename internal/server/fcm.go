package server

import (
	"bytes"
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
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// FCM sends Android pushes through the Firebase Cloud Messaging HTTP v1
// API, authenticated with a service account (architecture §15). Payloads
// carry only IDs and the daemon's ciphertext.

// fcmMaxData keeps a message under FCM's 4 KB payload limit with room for
// the keys and JSON.
const fcmMaxData = 3500

const fcmScope = "https://www.googleapis.com/auth/firebase.messaging"

// errFCMToken means FCM no longer knows the registration token.
var errFCMToken = errors.New("fcm: token not registered")

// fcmTransient is a failure worth retrying: the network, FCM overloaded
// (429, 5xx) or an access token that expired early (401). after is FCM's
// Retry-After, zero when it gave none.
type fcmTransient struct {
	after time.Duration
	err   error
}

func (e *fcmTransient) Error() string { return e.err.Error() }
func (e *fcmTransient) Unwrap() error { return e.err }

// fcmAttempts bounds retries of one message; fcmMaxWait caps a wait.
const (
	fcmAttempts = 4
	fcmMaxWait  = 30 * time.Second
)

type fcm struct {
	projectID string
	email     string
	key       *rsa.PrivateKey
	tokenURI  string
	sendURL   string // overridable in tests
	client    *http.Client
	retryBase time.Duration // first backoff; doubles per attempt

	mu      sync.Mutex
	access  string
	expires time.Time
}

// loadFCM reads a service account JSON file. It never logs its contents.
func loadFCM(path string) (*fcm, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("fcm service account: %w", err)
	}
	var sa struct {
		Type        string `json:"type"`
		ProjectID   string `json:"project_id"`
		ClientEmail string `json:"client_email"`
		PrivateKey  string `json:"private_key"`
		TokenURI    string `json:"token_uri"`
	}
	if err := json.Unmarshal(b, &sa); err != nil || sa.Type != "service_account" || sa.ProjectID == "" || sa.ClientEmail == "" {
		return nil, errors.New("fcm service account: not a service account key file")
	}
	block, _ := pem.Decode([]byte(sa.PrivateKey))
	if block == nil {
		return nil, errors.New("fcm service account: bad private key")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("fcm service account: bad private key")
	}
	rk, ok := k.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("fcm service account: not an RSA key")
	}
	if sa.TokenURI == "" {
		sa.TokenURI = "https://oauth2.googleapis.com/token"
	}
	return &fcm{
		projectID: sa.ProjectID,
		email:     sa.ClientEmail,
		key:       rk,
		tokenURI:  sa.TokenURI,
		sendURL:   "https://fcm.googleapis.com/v1/projects/" + url.PathEscape(sa.ProjectID) + "/messages:send",
		client:    &http.Client{Timeout: 20 * time.Second},
		retryBase: time.Second,
	}, nil
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

// accessToken returns an OAuth token for FCM, refreshing it before it expires.
func (f *fcm) accessToken(ctx context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.access != "" && time.Until(f.expires) > 5*time.Minute {
		return f.access, nil
	}
	now := time.Now()
	head, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]any{
		"iss": f.email, "scope": fcmScope, "aud": f.tokenURI,
		"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(),
	})
	unsigned := b64(head) + "." + b64(claims)
	sum := sha256.Sum256([]byte(unsigned))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", err
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {unsigned + "." + b64(sig)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.tokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := f.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fcm auth: %w", err)
	}
	defer res.Body.Close()
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if res.StatusCode != http.StatusOK || json.NewDecoder(res.Body).Decode(&tok) != nil || tok.AccessToken == "" {
		return "", fmt.Errorf("fcm auth: status %d", res.StatusCode)
	}
	f.access = tok.AccessToken
	f.expires = now.Add(time.Duration(tok.ExpiresIn) * time.Second)
	return f.access, nil
}

// send delivers a high-priority data message to one registration token.
func (f *fcm) send(ctx context.Context, token string, data map[string]string, ttl time.Duration) error {
	access, err := f.accessToken(ctx)
	if err != nil {
		return &fcmTransient{err: err}
	}
	body, _ := json.Marshal(map[string]any{"message": map[string]any{
		"token": token,
		"data":  data,
		"android": map[string]any{
			"priority": "high",
			"ttl":      fmt.Sprintf("%ds", int(ttl.Seconds())),
		},
	}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.sendURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Content-Type", "application/json")
	res, err := f.client.Do(req)
	if err != nil {
		return &fcmTransient{err: fmt.Errorf("fcm send: %w", err)}
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusOK {
		return nil
	}
	b, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	// A token that is gone or malformed is removed; the app registers a
	// new one when it next starts.
	if res.StatusCode == http.StatusNotFound || bytes.Contains(b, []byte("UNREGISTERED")) ||
		(res.StatusCode == http.StatusBadRequest && bytes.Contains(b, []byte("registration token"))) {
		return errFCMToken
	}
	err = fmt.Errorf("fcm send: status %d", res.StatusCode)
	switch {
	case res.StatusCode == http.StatusUnauthorized:
		// The access token went stale early: fetch a new one and try again.
		f.mu.Lock()
		f.access = ""
		f.mu.Unlock()
		return &fcmTransient{err: err}
	case res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500:
		t := &fcmTransient{err: err}
		if secs, e := strconv.Atoi(res.Header.Get("Retry-After")); e == nil && secs > 0 {
			t.after = time.Duration(secs) * time.Second
		}
		return t
	}
	return err
}

// sendRetrying sends a message, retrying transient failures with backoff
// and FCM's Retry-After, a few times at most: by then Notify has returned
// and the dedupe key is taken, so nobody else would send it again.
func (f *fcm) sendRetrying(ctx context.Context, token string, data map[string]string, ttl time.Duration) error {
	var err error
	for attempt := 0; attempt < fcmAttempts; attempt++ {
		err = f.send(ctx, token, data, ttl)
		var t *fcmTransient
		if !errors.As(err, &t) || attempt == fcmAttempts-1 {
			return err
		}
		wait := t.after
		if wait == 0 {
			wait = f.retryBase << attempt
		}
		wait = min(wait, fcmMaxWait)
		select {
		case <-ctx.Done():
			return err
		case <-time.After(wait):
		}
	}
	return err
}
