package remoteclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
)

const relayEpochHeader = "Remote-Agent-Relay-Epoch"

type epochTransport struct {
	client *Client
	base   http.RoundTripper
}

func (t epochTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(r)
	if response != nil {
		t.client.observeEpoch(response.Header.Get(relayEpochHeader))
	}
	return response, err
}
func (c *Client) bindHTTP(httpClient *http.Client) {
	base := httpClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	httpClient.Transport = epochTransport{c, base}
	c.http = httpClient
}
func (c *Client) observeEpoch(epoch string) {
	if epoch == "" {
		return
	}
	c.epochMu.Lock()
	c.relayEpoch = epoch
	c.epochMu.Unlock()
}

// RelayEpoch is a pre-dispatch discovery. Each Forward freezes its returned
// value in the protobuf body, so an opaque HTTP retry cannot acquire a new epoch.
func (c *Client) RelayEpoch(ctx context.Context) (string, error) {
	c.epochMu.Lock()
	epoch := c.relayEpoch
	c.epochMu.Unlock()
	if epoch != "" {
		return epoch, nil
	}
	if c.http == nil {
		return "", errors.New("relay_epoch_unavailable")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.Server, "/")+"/healthz", nil)
	if err != nil {
		return "", err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	res.Body.Close()
	c.epochMu.Lock()
	epoch = c.relayEpoch
	c.epochMu.Unlock()
	if epoch == "" {
		return "", errors.New("relay_epoch_unavailable")
	}
	return epoch, nil
}
