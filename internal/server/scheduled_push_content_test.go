package server

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

func TestScheduledWebPushDelivery(t *testing.T) {
	ts := newTestServer(t)
	p := ts.srv.push
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	if _, err := rand.Read(auth); err != nil {
		t.Fatal(err)
	}
	sub, err := json.Marshal(map[string]any{"endpoint": "https://push.example.com/endpoint", "keys": map[string]string{
		"p256dh": base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), "auth": base64.RawURLEncoding.EncodeToString(auth),
	}})
	if err != nil {
		t.Fatal(err)
	}
	delivered := make(chan []byte, 4)
	p.client = &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		payload, err := openTestWebPush(key, auth, body)
		if err != nil {
			return nil, err
		}
		delivered <- payload
		return &http.Response{StatusCode: http.StatusCreated, Body: http.NoBody, Request: req}, nil
	})}
	if err := p.subscribe(context.Background(), ts.tenant(), "client", string(sub), []pb.NotifyKind{pb.NotifyKind_NOTIFY_KIND_SCHEDULED, pb.NotifyKind_NOTIFY_KIND_ATTENTION}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		kind     pb.NotifyKind
		size     int
		detailed bool
	}{
		{pb.NotifyKind_NOTIFY_KIND_SCHEDULED, 512, true},
		{pb.NotifyKind_NOTIFY_KIND_SCHEDULED, 4096, false},
		{pb.NotifyKind_NOTIFY_KIND_ATTENTION, 512, false},
	} {
		content := bytes.Repeat([]byte{0xa5}, tc.size)
		if err := p.notify(context.Background(), ts.tenant(), "session", tc.kind, content, "scheduled:7"); err != nil {
			t.Fatal(err)
		}
		select {
		case payload := <-delivered:
			var data map[string]string
			if err := json.Unmarshal(payload, &data); err != nil {
				t.Fatal(err)
			}
			if len(payload) > webPushMaxPayload || (data["content"] != "") != tc.detailed {
				t.Fatal("wrong delivery content or size")
			}
			if tc.detailed {
				raw, err := base64.StdEncoding.DecodeString(data["content"])
				if err != nil || !bytes.Equal(raw, content) {
					t.Fatal("delivery changed envelope bytes")
				}
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no Web Push delivery")
		}
		p.mu.Lock()
		clear(p.recent)
		p.mu.Unlock()
	}
}

func TestWebPushOnlyScheduledCarriesCiphertext(t *testing.T) {
	content := []byte("synthetic encrypted envelope")
	for kind := range pushText {
		payload := webPushPayload("session", kind, content, "scheduled:7")
		var data map[string]string
		if err := json.Unmarshal(payload, &data); err != nil {
			t.Fatal(err)
		}
		if kind == pb.NotifyKind_NOTIFY_KIND_SCHEDULED {
			raw, err := base64.StdEncoding.DecodeString(data["content"])
			if err != nil || !bytes.Equal(raw, content) {
				t.Fatal("scheduled push lost opaque envelope")
			}
		} else if _, ok := data["content"]; ok {
			t.Fatalf("content leaked into %s Web Push", kind)
		}
		if data["title"] != "remote-agent" || data["body"] != pushText[kind] || data["session"] != "session" {
			t.Fatal("changed generic routing payload")
		}
	}
}

func TestWebPushPayloadLimitCountsBase64AndJSON(t *testing.T) {
	lastDetailed := 0
	for size := 0; size <= 4096; size++ {
		payload := webPushPayload("session", pb.NotifyKind_NOTIFY_KIND_SCHEDULED, bytes.Repeat([]byte{0xab}, size), "scheduled:7")
		if len(payload) > webPushMaxPayload {
			t.Fatalf("oversized Web Push: %d", len(payload))
		}
		var data map[string]string
		if err := json.Unmarshal(payload, &data); err != nil {
			t.Fatal(err)
		}
		if data["content"] != "" {
			lastDetailed = size
		}
		if size == 4096 && data["content"] != "" {
			t.Fatal("oversized content did not fall back")
		}
	}
	if lastDetailed < 2000 || lastDetailed > 3000 {
		t.Fatalf("unexpected content budget: %d", lastDetailed)
	}
	// Metadata and JSON escaping consume the same budget as the ciphertext.
	payload := webPushPayload(string(bytes.Repeat([]byte{'"'}, 300)), pb.NotifyKind_NOTIFY_KIND_SCHEDULED, bytes.Repeat([]byte{1}, lastDetailed), "scheduled:7")
	var data map[string]string
	if err := json.Unmarshal(payload, &data); err != nil {
		t.Fatal(err)
	}
	if data["content"] != "" || data["body"] != pushText[pb.NotifyKind_NOTIFY_KIND_SCHEDULED] {
		t.Fatal("metadata overflow did not fall back to generic")
	}
}
