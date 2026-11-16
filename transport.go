package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrHostNotAllowed is returned when a quota request targets a host outside the
// fixed allowlist, a non-HTTPS URL, or a URL carrying userinfo.
var ErrHostNotAllowed = errors.New("quota: quota host is not allowlisted")

// AllowedQuotaHosts returns the fixed set of upstream hosts the quota readers may
// contact. It returns a fresh slice so callers cannot mutate the allowlist.
func AllowedQuotaHosts() []string {
	return []string{
		"daily-cloudcode-pa.googleapis.com",
		"daily-cloudcode-pa.sandbox.googleapis.com",
		"cloudcode-pa.googleapis.com",
		"api.anthropic.com",
		"chatgpt.com",
	}
}

const (
	// DefaultQuotaTransportTimeout bounds one quota HTTP request.
	DefaultQuotaTransportTimeout = 15 * time.Second
	// DefaultQuotaMaxBodyBytes bounds one quota response body.
	DefaultQuotaMaxBodyBytes = 64 * 1024
)

// Transport is the plugin-owned HTTP transport for quota reads. It is deliberately
// separate from the host bridge: the pinned host transport follows redirects and
// its effective proxy is not discoverable, while token-bearing quota requests must
// never follow a redirect. When ProxyURL is set the operator has explicitly chosen
// an outbound proxy; when it is empty the transport dials directly. It never falls
// back to an undiscovered policy. Normal model traffic keeps using the host bridge.
type Transport struct {
	ProxyURL string
	Timeout  time.Duration
	MaxBody  int64
	// allowLoopback admits plain-HTTP loopback targets. It is set only by tests
	// and the native probe through AllowLoopbackForTest; production transports
	// never reach loopback.
	allowLoopback bool
	client        *http.Client
}

// NewTransport builds a quota transport. An unparseable proxy URL, an empty host,
// userinfo or a non-http(s) scheme is rejected rather than silently ignored; the
// value is never echoed in the error.
func NewTransport(proxyURL string) (*Transport, error) {
	transport := &Transport{
		ProxyURL: strings.TrimSpace(proxyURL),
		Timeout:  DefaultQuotaTransportTimeout,
		MaxBody:  DefaultQuotaMaxBodyBytes,
	}
	httpTransport := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	if transport.ProxyURL != "" {
		parsed, err := url.Parse(transport.ProxyURL)
		if err != nil || parsed.Hostname() == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return nil, errors.New("quota: invalid quota_proxy URL")
		}
		httpTransport.Proxy = http.ProxyURL(parsed)
	}
	transport.client = &http.Client{
		Transport: httpTransport,
		// Never follow a redirect: a redirected token-bearing request could leak
		// the Authorization header to a host outside the allowlist.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return transport, nil
}

// Do performs one bounded, redirect-free request. Non-2xx statuses are returned
// to the caller as responses; readers decide whether they are failures.
func (t *Transport) Do(ctx context.Context, req HTTPRequest) (HTTPResponse, error) {
	if t == nil || t.client == nil {
		return HTTPResponse{}, errors.New("quota: transport is not configured")
	}
	parsed, err := url.Parse(req.URL)
	if err != nil || parsed.Host == "" {
		return HTTPResponse{}, errors.New("quota: invalid request URL")
	}
	if err := t.checkHost(parsed); err != nil {
		return HTTPResponse{}, err
	}

	timeout := t.Timeout
	if timeout <= 0 {
		timeout = DefaultQuotaTransportTimeout
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(requestCtx, req.Method, req.URL, bodyReader(req.Body))
	if err != nil {
		return HTTPResponse{}, errors.New("quota: build request")
	}
	for name, values := range req.Headers {
		for _, value := range values {
			httpReq.Header.Add(name, value)
		}
	}
	response, err := t.client.Do(httpReq)
	if err != nil {
		return HTTPResponse{}, errors.New("quota: request failed")
	}
	defer func() { _ = response.Body.Close() }()

	limit := t.MaxBody
	if limit <= 0 {
		limit = DefaultQuotaMaxBodyBytes
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return HTTPResponse{}, errors.New("quota: read response failed")
	}
	if int64(len(body)) > limit {
		return HTTPResponse{}, fmt.Errorf("quota: response exceeds %d bytes", limit)
	}
	return HTTPResponse{StatusCode: response.StatusCode, Body: body}, nil
}

// AllowLoopbackForTest admits plain-HTTP loopback targets for fixtures. Its name
// marks it test-only; the production transport never calls it.
func (t *Transport) AllowLoopbackForTest() *Transport {
	t.allowLoopback = true
	return t
}

func (t *Transport) checkHost(parsed *url.URL) error {
	host := strings.ToLower(parsed.Hostname())
	if t.allowLoopback && (host == "127.0.0.1" || host == "localhost" || host == "::1") {
		return nil
	}
	if parsed.Scheme != "https" || parsed.User != nil {
		return ErrHostNotAllowed
	}
	for _, candidate := range AllowedQuotaHosts() {
		if host == candidate {
			return nil
		}
	}
	return ErrHostNotAllowed
}

func bodyReader(body []byte) io.Reader {
	if len(body) == 0 {
		return nil
	}
	return strings.NewReader(string(body))
}
