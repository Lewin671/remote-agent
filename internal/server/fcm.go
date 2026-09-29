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

type fcm struct {
	projectID string
	email     string
	key       *rsa.PrivateKey
	tokenURI  string
	sendURL   string // overridable in tests
	client    *http.Client

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
		return err
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
		return fmt.Errorf("fcm send: %w", err)
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
	if res.StatusCode == http.StatusUnauthorized {
		f.mu.Lock()
		f.access = ""
		f.mu.Unlock()
	}
	return fmt.Errorf("fcm send: status %d", res.StatusCode)
}
