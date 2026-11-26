// Package httpclient provides the shared Fetcher contract, a retrying HTTP
// client with Worker proxy support, and a fixture-backed replay fetcher.
package httpclient

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultSearchRetries is the retry count used when Request.Retries is nil.
	DefaultSearchRetries = 2
	// DefaultDetailRetries is the retry count used for job detail requests.
	DefaultDetailRetries = 2
	// DefaultRetryBaseSeconds is the exponential backoff base.
	DefaultRetryBaseSeconds = 3.0
	// DefaultRetryAfterCapSeconds caps a numeric Retry-After header.
	DefaultRetryAfterCapSeconds = 15.0
	// DefaultRequestTimeout bounds a single response wait.
	DefaultRequestTimeout = 20 * time.Second
	// DefaultConnectTimeout bounds connection establishment and TLS handshake.
	DefaultConnectTimeout = 5 * time.Second
	// DefaultTransportFailureLimit opens the circuit after consecutive failures.
	DefaultTransportFailureLimit = 3
)

var (
	// ErrNoResponse means all configured transports failed to produce a response.
	ErrNoResponse = errors.New("http request produced no response")
	// ErrCircuitOpen means the client is skipping requests for this run.
	ErrCircuitOpen = errors.New("transport circuit is open")
	// ErrMissingFixture means the replay fetcher has no recorded request.
	ErrMissingFixture = errors.New("fixture request not found")
)

// transportError marks failures from the network exchange itself. Other
// errors, such as a malformed URL or a body-read failure, do not open the
// circuit breaker.
type transportError struct {
	err error
}

func (e *transportError) Error() string { return e.err.Error() }

func (e *transportError) Unwrap() error { return e.err }

func isTransportError(err error) bool {
	var target *transportError
	return errors.As(err, &target)
}

// Request describes one GET request. A nil Retries selects
// Options.SearchRetries; a pointer to zero performs a single attempt.
type Request struct {
	URL     string
	Params  map[string]string
	Headers map[string]string
	Retries *int
	Label   string
}

// Response is the materialized result of a successful HTTP exchange.
type Response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
	FinalURL   string
}

// Text returns the response body as text.
func (r *Response) Text() string {
	if r == nil {
		return ""
	}
	return string(r.Body)
}

// IsSuccess reports whether the status is in the 2xx range.
func (r *Response) IsSuccess() bool {
	return r != nil && r.StatusCode >= 200 && r.StatusCode < 300
}

// IsAuthwall reports whether the final URL contains a LinkedIn authwall or
// login redirect.
func IsAuthwall(response *Response) bool {
	if response == nil {
		return false
	}
	finalURL := strings.ToLower(response.FinalURL)
	return strings.Contains(finalURL, "authwall") || strings.Contains(finalURL, "/login")
}

// Fetcher is the interface used by scrapers. BeginCycle resets per-run state
// such as the transport circuit breaker.
type Fetcher interface {
	Fetch(ctx context.Context, request Request) (*Response, error)
	BeginCycle()
	CircuitOpen() bool
}

// Options configures a Client. Zero-valued duration and count fields receive
// the Python client's defaults.
type Options struct {
	Source                string
	ProxyMode             string
	ProxyHosts            []string
	SearchRetries         int
	DetailRetries         int
	RetryBaseSeconds      float64
	RetryAfterCapSeconds  float64
	RequestTimeout        time.Duration
	ConnectTimeout        time.Duration
	TransportFailureLimit int
	LookupEnv             func(string) (string, bool)
	Sleep                 func(context.Context, time.Duration) error
	Random                func() float64
	HTTPClient            *http.Client
}

type clientOptions struct {
	Options
	proxyHosts map[string]struct{}
}

type proxyTarget struct {
	url     string
	headers map[string]string
}

// Client implements Fetcher with retries and proxy fallback.
type Client struct {
	options clientOptions

	httpClient *http.Client

	mu                sync.Mutex
	transportFailures int
	circuitOpen       bool
}

// NewClient builds a retrying client. Proxy credentials are read from the
// process environment unless Options.LookupEnv is supplied.
func NewClient(options Options) *Client {
	normalized := normalizeOptions(options)
	client := &Client{
		options:    normalized,
		httpClient: options.HTTPClient,
	}
	if client.httpClient == nil {
		dialer := &net.Dialer{Timeout: normalized.ConnectTimeout}
		transport := &http.Transport{
			DialContext:           dialer.DialContext,
			TLSHandshakeTimeout:   normalized.ConnectTimeout,
			ResponseHeaderTimeout: normalized.RequestTimeout,
		}
		client.httpClient = &http.Client{
			Transport: transport,
			Timeout:   normalized.RequestTimeout,
		}
	}
	return client
}

func normalizeOptions(options Options) clientOptions {
	if options.Source == "" {
		options.Source = "scraper"
	}
	if options.ProxyMode == "" {
		options.ProxyMode = "off"
	}
	if options.SearchRetries <= 0 {
		options.SearchRetries = DefaultSearchRetries
	}
	if options.DetailRetries <= 0 {
		options.DetailRetries = DefaultDetailRetries
	}
	if options.RetryBaseSeconds <= 0 {
		options.RetryBaseSeconds = DefaultRetryBaseSeconds
	}
	if options.RetryAfterCapSeconds <= 0 {
		options.RetryAfterCapSeconds = DefaultRetryAfterCapSeconds
	}
	if options.RequestTimeout <= 0 {
		options.RequestTimeout = DefaultRequestTimeout
	}
	if options.ConnectTimeout <= 0 {
		options.ConnectTimeout = DefaultConnectTimeout
	}
	if options.TransportFailureLimit <= 0 {
		options.TransportFailureLimit = DefaultTransportFailureLimit
	}

	hosts := make(map[string]struct{}, len(options.ProxyHosts))
	for _, host := range options.ProxyHosts {
		host = strings.ToLower(strings.TrimSpace(host))
		if host != "" {
			hosts[host] = struct{}{}
		}
	}
	return clientOptions{Options: options, proxyHosts: hosts}
}

// Close releases idle connections.
func (c *Client) Close() {
	if closer, ok := c.httpClient.Transport.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

// BeginCycle closes the circuit breaker for a new scrape run.
func (c *Client) BeginCycle() {
	c.resetTransportHealth()
}

// ResetTransportHealth is an explicit alias for BeginCycle.
func (c *Client) ResetTransportHealth() {
	c.resetTransportHealth()
}

func (c *Client) resetTransportHealth() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.transportFailures = 0
	c.circuitOpen = false
}

// CircuitOpen reports whether remaining requests in this run are skipped.
func (c *Client) CircuitOpen() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.circuitOpen
}

func (c *Client) env(name string) string {
	if c.options.LookupEnv != nil {
		value, _ := c.options.LookupEnv(name)
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(os.Getenv(name))
}

func (c *Client) proxyBase() string {
	base := c.env("JOB_PROXY_URL")
	if base == "" {
		base = c.env("CHILETRABAJOS_PROXY_URL")
	}
	return strings.TrimRight(base, "/")
}

func (c *Client) proxyToken() string {
	token := c.env("JOB_PROXY_TOKEN")
	if token == "" {
		token = c.env("CHILETRABAJOS_PROXY_TOKEN")
	}
	return token
}

func validProxyMode(mode string) bool {
	return mode == "off" || mode == "fallback" || mode == "always"
}

func (c *Client) proxyMode() string {
	if c.proxyBase() == "" || c.proxyToken() == "" || len(c.options.proxyHosts) == 0 {
		return "off"
	}
	override := strings.ToLower(c.env("JOB_PROXY_MODE"))
	if validProxyMode(override) {
		return override
	}
	mode := strings.ToLower(c.options.ProxyMode)
	if validProxyMode(mode) {
		return mode
	}
	return "fallback"
}

func (c *Client) proxyRequest(rawURL string) (string, map[string]string) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", nil
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return "", nil
	}
	if _, ok := c.options.proxyHosts[host]; !ok {
		return "", nil
	}

	path := parsed.EscapedPath()
	if path == "" {
		path = "/"
	}
	targetURL := c.proxyBase() + path
	if parsed.RawQuery != "" {
		targetURL += "?" + parsed.RawQuery
	}
	return targetURL, map[string]string{
		"X-Proxy-Token": c.proxyToken(),
		"X-Target-Host": host,
	}
}

func (c *Client) requestTargets(rawURL string) []proxyTarget {
	if c.proxyMode() == "off" {
		return []proxyTarget{{url: rawURL}}
	}
	proxyURL, headers := c.proxyRequest(rawURL)
	if proxyURL == "" {
		return []proxyTarget{{url: rawURL}}
	}
	if c.proxyMode() == "always" {
		return []proxyTarget{{url: proxyURL, headers: headers}}
	}
	return []proxyTarget{
		{url: rawURL},
		{url: proxyURL, headers: headers},
	}
}

func mergeHeaders(base, extra map[string]string) map[string]string {
	merged := make(map[string]string, len(base)+len(extra))
	for name, value := range base {
		merged[name] = value
	}
	for name, value := range extra {
		merged[name] = value
	}
	return merged
}

func (c *Client) attempts(request Request) int {
	if request.Retries != nil {
		if *request.Retries < 0 {
			return 0
		}
		return *request.Retries
	}
	return c.options.SearchRetries
}

// Fetch performs one logical request across the configured transports.
func (c *Client) Fetch(ctx context.Context, request Request) (*Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	if strings.TrimSpace(request.URL) == "" {
		return nil, fmt.Errorf("fetch: empty URL")
	}

	label := request.Label
	if label == "" {
		label = "request"
	}
	targets := c.requestTargets(request.URL)
	attempts := c.attempts(request)
	var lastResponse *Response
	var lastErr error

	for index, target := range targets {
		if index > 0 {
			c.resetTransportHealth()
			slog.Warn(
				"HTTP request failed on the direct transport; retrying via proxy",
				"source", c.options.Source,
				"label", label,
			)
		}
		headers := request.Headers
		if len(target.headers) > 0 {
			headers = mergeHeaders(request.Headers, target.headers)
		}
		transportAttempts := attempts
		if len(target.headers) > 0 {
			// A 429 through the same proxy IP will not improve by retrying.
			transportAttempts = 0
		}
		response, err := c.requestAttempt(
			ctx, target.url, request.Params, headers, transportAttempts, label,
		)
		if response != nil {
			lastResponse = response
			if !IsProxyWorthy(response) {
				return response, nil
			}
		}
		if err != nil {
			lastErr = err
		}
	}

	if lastResponse != nil {
		return lastResponse, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("%w: no transport produced a response", ErrNoResponse)
}

// IsProxyWorthy reports whether a failed direct response should be retried
// through the Worker proxy.
func IsProxyWorthy(response *Response) bool {
	if response == nil {
		return true
	}
	return response.StatusCode == 403 ||
		response.StatusCode == 429 ||
		response.StatusCode == 999 ||
		response.StatusCode >= 500
}

func (c *Client) requestAttempt(
	ctx context.Context,
	rawURL string,
	params, headers map[string]string,
	attempts int,
	label string,
) (*Response, error) {
	if c.CircuitOpen() {
		return nil, fmt.Errorf("%w: skipping %s", ErrCircuitOpen, label)
	}

	for attempt := 0; attempt <= attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("request %s: %w", label, err)
		}

		response, err := c.get(ctx, rawURL, params, headers)
		if err != nil {
			if ctx.Err() != nil {
				return nil, fmt.Errorf("request %s: %w", label, ctx.Err())
			}
			if !isTransportError(err) {
				return nil, fmt.Errorf("request %s: %w", label, err)
			}
			if c.noteTransportFailure(err) {
				return nil, fmt.Errorf(
					"%w after %d consecutive failures: %v",
					ErrCircuitOpen, c.options.TransportFailureLimit, err,
				)
			}
			if attempt >= attempts {
				return nil, fmt.Errorf("request %s failed after %d attempts: %w", label, attempt+1, err)
			}
			delay := c.retryDelay(nil, attempt)
			slog.Warn(
				"HTTP request error; retrying",
				"source", c.options.Source,
				"label", label,
				"delay_seconds", delay.Seconds(),
				"attempt", attempt+1,
				"max_attempts", attempts+1,
				"error", err,
			)
			if err := c.sleep(ctx, delay); err != nil {
				return nil, err
			}
			continue
		}

		c.noteSuccess()
		if response.StatusCode == 429 || response.StatusCode >= 500 {
			status := response.StatusCode
			if attempt >= attempts {
				return nil, fmt.Errorf(
					"%w: status %d after %d attempts", ErrNoResponse, status, attempt+1,
				)
			}
			delay := c.retryDelay(response, attempt)
			slog.Warn(
				"HTTP request returned a retryable status; retrying",
				"source", c.options.Source,
				"label", label,
				"status", status,
				"delay_seconds", delay.Seconds(),
				"attempt", attempt+1,
				"max_attempts", attempts+1,
			)
			if err := c.sleep(ctx, delay); err != nil {
				return nil, err
			}
			continue
		}
		return response, nil
	}
	return nil, fmt.Errorf("%w: attempts exhausted for %s", ErrNoResponse, label)
}

func (c *Client) noteSuccess() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.transportFailures = 0
}

func (c *Client) noteTransportFailure(err error) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.transportFailures++
	if c.transportFailures < c.options.TransportFailureLimit {
		return false
	}
	if !c.circuitOpen {
		slog.Warn(
			"HTTP transport circuit opened after consecutive failures",
			"source", c.options.Source,
			"failures", c.transportFailures,
			"error", err,
		)
		c.circuitOpen = true
	}
	return true
}

func (c *Client) get(
	ctx context.Context,
	rawURL string,
	params, headers map[string]string,
) (*Response, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse request URL: %w", err)
	}
	query := parsed.Query()
	for name, value := range params {
		query.Set(name, value)
	}
	parsed.RawQuery = query.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, &transportError{err: fmt.Errorf("GET %s: %w", parsed.String(), err)}
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("read response body: %w", err)
	}
	finalURL := rawURL
	if response.Request != nil && response.Request.URL != nil {
		finalURL = response.Request.URL.String()
	}
	return &Response{
		StatusCode: response.StatusCode,
		Header:     response.Header.Clone(),
		Body:       body,
		FinalURL:   finalURL,
	}, nil
}

func (c *Client) retryDelay(response *Response, attempt int) time.Duration {
	if response != nil {
		raw := strings.TrimSpace(response.Header.Get("Retry-After"))
		if raw != "" {
			if seconds, err := strconv.ParseFloat(raw, 64); err == nil &&
				!math.IsNaN(seconds) && !math.IsInf(seconds, 0) {
				if seconds < 1 {
					seconds = 1
				}
				if seconds > c.options.RetryAfterCapSeconds {
					seconds = c.options.RetryAfterCapSeconds
				}
				return time.Duration(seconds * float64(time.Second))
			}
		}
	}

	base := c.options.RetryBaseSeconds * math.Pow(2, float64(attempt))
	jitter := 0.0
	if c.options.Random != nil {
		jitter = c.options.Random()
		if jitter < 0 {
			jitter = 0
		}
		if jitter > 1.5 {
			jitter = 1.5
		}
	}
	return time.Duration((base + jitter) * float64(time.Second))
}

func (c *Client) sleep(ctx context.Context, delay time.Duration) error {
	if c.options.Sleep != nil {
		if err := c.options.Sleep(ctx, delay); err != nil {
			return fmt.Errorf("retry sleep: %w", err)
		}
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("retry sleep: %w", ctx.Err())
	}
}
