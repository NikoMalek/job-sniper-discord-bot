package httpclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

type sleepRecorder struct {
	durations []time.Duration
}

func (s *sleepRecorder) sleep(_ context.Context, delay time.Duration) error {
	s.durations = append(s.durations, delay)
	return nil
}

func intPtr(value int) *int { return &value }

func envLookup(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func newTestClient(t *testing.T, server *httptest.Server, options Options) (*Client, *sleepRecorder) {
	t.Helper()
	recorder := &sleepRecorder{}
	if options.Sleep == nil {
		options.Sleep = recorder.sleep
	}
	if options.Random == nil {
		options.Random = func() float64 { return 0.5 }
	}
	options.HTTPClient = server.Client()
	client := NewClient(options)
	t.Cleanup(client.Close)
	return client, recorder
}

func TestFetchRetries429WithRetryAfter(t *testing.T) {
	var calls atomic.Int32
	var queries []url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		queries = append(queries, r.URL.Query())
		if calls.Load() == 1 {
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	client, sleeper := newTestClient(t, server, Options{Source: "test"})
	response, err := client.Fetch(context.Background(), Request{
		URL:    server.URL + "/search",
		Params: map[string]string{"q": "python"},
		Label:  "search",
	})
	if err != nil {
		t.Fatalf("Fetch() unexpected error: %v", err)
	}
	if got := response.Text(); got != "ok" {
		t.Fatalf("body = %q, want ok", got)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("server calls = %d, want 2", got)
	}
	if len(sleeper.durations) != 1 || sleeper.durations[0] != 2*time.Second {
		t.Fatalf("sleep durations = %v, want [2s]", sleeper.durations)
	}
	for _, query := range queries {
		if got := query.Get("q"); got != "python" {
			t.Errorf("query q = %q, want python", got)
		}
	}
}

func TestFetchRetriesServerErrorsWithBackoff(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, "recovered")
	}))
	defer server.Close()

	client, sleeper := newTestClient(t, server, Options{Source: "test"})
	response, err := client.Fetch(context.Background(), Request{
		URL:     server.URL + "/search",
		Retries: intPtr(2),
	})
	if err != nil {
		t.Fatalf("Fetch() unexpected error: %v", err)
	}
	if got := response.Text(); got != "recovered" {
		t.Fatalf("body = %q, want recovered", got)
	}
	want := []time.Duration{3500 * time.Millisecond, 6500 * time.Millisecond}
	if len(sleeper.durations) != len(want) {
		t.Fatalf("sleep durations = %v, want %v", sleeper.durations, want)
	}
	for index := range want {
		if sleeper.durations[index] != want[index] {
			t.Errorf("sleep[%d] = %v, want %v", index, sleeper.durations[index], want[index])
		}
	}
}

func TestFetchZeroRetriesMakesOneAttempt(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client, sleeper := newTestClient(t, server, Options{Source: "test", SearchRetries: 4})
	_, err := client.Fetch(context.Background(), Request{
		URL:     server.URL + "/details",
		Retries: intPtr(0),
	})
	if !errors.Is(err, ErrNoResponse) {
		t.Fatalf("Fetch() error = %v, want ErrNoResponse", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("server calls = %d, want 1", got)
	}
	if len(sleeper.durations) != 0 {
		t.Errorf("sleep durations = %v, want none", sleeper.durations)
	}
}

func TestIsProxyWorthy(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		want   bool
	}{
		{name: "nil response", status: 0, want: true},
		{name: "forbidden", status: http.StatusForbidden, want: true},
		{name: "rate limited", status: http.StatusTooManyRequests, want: true},
		{name: "linkedin 999", status: 999, want: true},
		{name: "server error", status: http.StatusBadGateway, want: true},
		{name: "not found", status: http.StatusNotFound, want: false},
		{name: "success", status: http.StatusOK, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var response *Response
			if test.status != 0 {
				response = &Response{StatusCode: test.status}
			}
			if got := IsProxyWorthy(response); got != test.want {
				t.Fatalf("IsProxyWorthy() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestRetryDelayClampsRetryAfter(t *testing.T) {
	client := NewClient(Options{
		RetryBaseSeconds:     1,
		RetryAfterCapSeconds: 5,
		Random:               func() float64 { return 1.5 },
	})
	for _, test := range []struct {
		name   string
		header string
		want   time.Duration
	}{
		{name: "minimum", header: "0.25", want: time.Second},
		{name: "maximum", header: "30", want: 5 * time.Second},
		{name: "invalid falls back", header: "soon", want: 3500 * time.Millisecond},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := &Response{Header: http.Header{}}
			response.Header.Set("Retry-After", test.header)
			if got := client.retryDelay(response, 1); got != test.want {
				t.Fatalf("retryDelay() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestProxyFallbackAfterForbidden(t *testing.T) {
	var directCalls atomic.Int32
	var proxyCalls atomic.Int32
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		directCalls.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer direct.Close()

	var proxyHeaders http.Header
	var proxyQuery url.Values
	var proxyPath string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyCalls.Add(1)
		proxyHeaders = r.Header.Clone()
		proxyQuery = r.URL.Query()
		proxyPath = r.URL.Path
		_, _ = io.WriteString(w, "proxied")
	}))
	defer proxy.Close()

	options := Options{
		Source:     "test",
		ProxyMode:  "fallback",
		ProxyHosts: []string{"127.0.0.1"},
		LookupEnv: envLookup(map[string]string{
			"JOB_PROXY_URL":   proxy.URL + "/",
			"JOB_PROXY_TOKEN": "test-token",
		}),
	}
	client, sleeper := newTestClient(t, direct, options)
	response, err := client.Fetch(context.Background(), Request{
		URL:    direct.URL + "/search",
		Params: map[string]string{"q": "python"},
	})
	if err != nil {
		t.Fatalf("Fetch() unexpected error: %v", err)
	}
	if got := response.Text(); got != "proxied" {
		t.Fatalf("body = %q, want proxied", got)
	}
	if directCalls.Load() != 1 || proxyCalls.Load() != 1 {
		t.Fatalf("calls = direct %d, proxy %d, want 1 and 1", directCalls.Load(), proxyCalls.Load())
	}
	if got := proxyHeaders.Get("X-Proxy-Token"); got != "test-token" {
		t.Errorf("X-Proxy-Token = %q, want test-token", got)
	}
	if got := proxyHeaders.Get("X-Target-Host"); got != "127.0.0.1" {
		t.Errorf("X-Target-Host = %q, want 127.0.0.1", got)
	}
	if proxyPath != "/search" {
		t.Errorf("proxy path = %q, want /search", proxyPath)
	}
	if got := proxyQuery.Get("q"); got != "python" {
		t.Errorf("proxy query q = %q, want python", got)
	}
	if len(sleeper.durations) != 0 {
		t.Errorf("sleep durations = %v, want none", sleeper.durations)
	}
	if client.CircuitOpen() {
		t.Error("circuit is open after a successful proxy response")
	}
}

func TestProxyAlwaysUsesOneAttempt(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	options := Options{
		Source:        "test",
		ProxyMode:     "always",
		ProxyHosts:    []string{"127.0.0.1"},
		SearchRetries: 4,
		LookupEnv: envLookup(map[string]string{
			"JOB_PROXY_URL":   server.URL + "/proxy",
			"JOB_PROXY_TOKEN": "test-token",
		}),
	}
	client, sleeper := newTestClient(t, server, options)
	_, err := client.Fetch(context.Background(), Request{
		URL:   server.URL + "/search",
		Label: "search",
	})
	if !errors.Is(err, ErrNoResponse) {
		t.Fatalf("Fetch() error = %v, want ErrNoResponse", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("server calls = %d, want 1", got)
	}
	if len(sleeper.durations) != 0 {
		t.Errorf("sleep durations = %v, want none", sleeper.durations)
	}
}

func TestProxyModeOffIgnoresProxyConfiguration(t *testing.T) {
	var directCalls atomic.Int32
	var proxyCalls atomic.Int32
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		directCalls.Add(1)
		_, _ = io.WriteString(w, "direct")
	}))
	defer direct.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyCalls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer proxy.Close()

	options := Options{
		Source:     "test",
		ProxyMode:  "off",
		ProxyHosts: []string{"127.0.0.1"},
		LookupEnv: envLookup(map[string]string{
			"JOB_PROXY_URL":   proxy.URL,
			"JOB_PROXY_TOKEN": "test-token",
			"JOB_PROXY_MODE":  "invalid",
		}),
	}
	client, _ := newTestClient(t, direct, options)
	response, err := client.Fetch(context.Background(), Request{URL: direct.URL + "/search"})
	if err != nil {
		t.Fatalf("Fetch() unexpected error: %v", err)
	}
	if got := response.Text(); got != "direct" {
		t.Fatalf("body = %q, want direct", got)
	}
	if directCalls.Load() != 1 || proxyCalls.Load() != 0 {
		t.Fatalf("calls = direct %d, proxy %d, want 1 and 0", directCalls.Load(), proxyCalls.Load())
	}
}

func TestProxyEnvFallbackAndModeOverride(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	client := NewClient(Options{
		Source:     "test",
		ProxyMode:  "always",
		ProxyHosts: []string{"WWW.EXAMPLE.COM"},
		LookupEnv: envLookup(map[string]string{
			"CHILETRABAJOS_PROXY_URL":   "https://proxy.test/",
			"CHILETRABAJOS_PROXY_TOKEN": "legacy-token",
			"JOB_PROXY_MODE":            "off",
		}),
	})
	if got := client.proxyMode(); got != "off" {
		t.Fatalf("proxyMode() = %q, want off", got)
	}
	if got := client.proxyBase(); got != "https://proxy.test" {
		t.Fatalf("proxyBase() = %q, want https://proxy.test", got)
	}
	target, headers := client.proxyRequest("https://www.example.com/path?q=1")
	if target != "https://proxy.test/path?q=1" {
		t.Errorf("proxy target = %q, want https://proxy.test/path?q=1", target)
	}
	if headers["X-Proxy-Token"] != "legacy-token" || headers["X-Target-Host"] != "www.example.com" {
		t.Errorf("proxy headers = %v, want legacy token and lowercased host", headers)
	}
	if target, _ := client.proxyRequest("https://other.test/path"); target != "" {
		t.Errorf("proxy target for unlisted host = %q, want empty", target)
	}
}

func TestCircuitBreakerSkipsRemainingRequests(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		connection, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		_ = connection.Close()
	}))
	defer server.Close()

	client, sleeper := newTestClient(t, server, Options{
		Source:                "test",
		SearchRetries:         5,
		TransportFailureLimit: 2,
	})
	request := Request{URL: server.URL + "/search"}

	_, err := client.Fetch(context.Background(), request)
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("first Fetch() error = %v, want ErrCircuitOpen", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("server calls = %d, want 2", got)
	}
	if len(sleeper.durations) != 1 {
		t.Fatalf("sleep durations = %v, want one delay", sleeper.durations)
	}

	_, err = client.Fetch(context.Background(), request)
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("second Fetch() error = %v, want ErrCircuitOpen", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("server calls after open circuit = %d, want 2", got)
	}

	client.BeginCycle()
	if client.CircuitOpen() {
		t.Fatal("BeginCycle() did not close the circuit")
	}
	_, _ = client.Fetch(context.Background(), request)
	if got := calls.Load(); got != 4 {
		t.Fatalf("server calls after BeginCycle = %d, want 4", got)
	}
}

func TestFetchHonorsCanceledContext(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	client, _ := newTestClient(t, server, Options{Source: "test"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := client.Fetch(ctx, Request{URL: server.URL + "/search"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Fetch() error = %v, want context.Canceled", err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("server calls = %d, want 0", got)
	}
}

func TestIsAuthwall(t *testing.T) {
	for _, test := range []struct {
		name     string
		response *Response
		want     bool
	}{
		{name: "nil", response: nil, want: false},
		{name: "authwall", response: &Response{FinalURL: "https://www.linkedin.com/AUTHWALL?x=1"}, want: true},
		{name: "login", response: &Response{FinalURL: "https://www.linkedin.com/login?x=1"}, want: true},
		{name: "normal", response: &Response{FinalURL: "https://www.linkedin.com/jobs/view/1"}, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := IsAuthwall(test.response); got != test.want {
				t.Fatalf("IsAuthwall() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestReplayFetcherMatchesParamsAndConsumesDuplicates(t *testing.T) {
	fixturesDir := t.TempDir()
	portalDir := filepath.Join(fixturesDir, "portal", "search")
	if err := os.MkdirAll(portalDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() unexpected error: %v", err)
	}
	entries := []RecordedRequest{
		{
			Seq:         1,
			Label:       "first",
			URL:         "https://example.test/search",
			Params:      map[string]string{"q": "python", "page": "1"},
			Status:      intPtr(http.StatusOK),
			ContentType: "text/html; charset=utf-8",
			File:        "001_first.html",
		},
		{
			Seq:         2,
			Label:       "second",
			URL:         "https://example.test/search",
			Params:      map[string]string{"q": "python", "page": "1"},
			Status:      intPtr(http.StatusOK),
			ContentType: "application/json",
			File:        "002_second.json",
		},
		{
			Seq:         3,
			Label:       "numeric params",
			URL:         "https://example.test/city",
			Params:      map[string]string{"region": "13"},
			Status:      intPtr(http.StatusOK),
			ContentType: "text/html",
			File:        "003_city.html",
		},
	}
	raw, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		t.Fatalf("json.MarshalIndent() unexpected error: %v", err)
	}
	// The real fixtures store numeric params without quotes.
	raw = []byte(strings.ReplaceAll(string(raw), `"region": "13"`, `"region": 13`))
	if err := os.WriteFile(filepath.Join(portalDir, "requests.json"), raw, 0o644); err != nil {
		t.Fatalf("WriteFile(requests.json) unexpected error: %v", err)
	}
	for name, body := range map[string]string{
		"001_first.html":  "<html>first</html>",
		"002_second.json": `{"second":true}`,
		"003_city.html":   "<html>city</html>",
	} {
		if err := os.WriteFile(filepath.Join(portalDir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) unexpected error: %v", name, err)
		}
	}

	fetcher, err := NewReplayFetcher(context.Background(), fixturesDir, "portal", "search")
	if err != nil {
		t.Fatalf("NewReplayFetcher() unexpected error: %v", err)
	}
	request := Request{
		URL:    "https://example.test/search",
		Params: map[string]string{"q": "python", "page": "1"},
	}
	first, err := fetcher.Fetch(context.Background(), request)
	if err != nil {
		t.Fatalf("first Fetch() unexpected error: %v", err)
	}
	if got := first.Text(); got != "<html>first</html>" {
		t.Fatalf("first body = %q, want first fixture", got)
	}
	second, err := fetcher.Fetch(context.Background(), request)
	if err != nil {
		t.Fatalf("second Fetch() unexpected error: %v", err)
	}
	if got := second.Text(); got != `{"second":true}` {
		t.Fatalf("second body = %q, want second fixture", got)
	}
	if got := fetcher.Remaining(); got != 1 {
		t.Fatalf("Remaining() = %d, want 1", got)
	}
	numeric, err := fetcher.Fetch(context.Background(), Request{
		URL:    "https://example.test/city",
		Params: map[string]string{"region": "13"},
	})
	if err != nil {
		t.Fatalf("numeric Fetch() unexpected error: %v", err)
	}
	if got := numeric.Text(); got != "<html>city</html>" {
		t.Fatalf("numeric body = %q, want city fixture", got)
	}
	_, err = fetcher.Fetch(context.Background(), request)
	if !errors.Is(err, ErrMissingFixture) {
		t.Fatalf("replayed Fetch() error = %v, want ErrMissingFixture", err)
	}
	if got := fetcher.CircuitOpen(); got {
		t.Error("CircuitOpen() = true, want false")
	}
	if got := len(fetcher.Calls()); got != 4 {
		t.Fatalf("Calls() = %d, want 4", got)
	}

	fetcher.BeginCycle()
	if got := fetcher.Remaining(); got != 3 {
		t.Fatalf("Remaining() after BeginCycle = %d, want 3", got)
	}
	if got := len(fetcher.Calls()); got != 0 {
		t.Fatalf("Calls() after BeginCycle = %d, want 0", got)
	}
}

func TestReplayFetcherWithRecordedFixture(t *testing.T) {
	fixturesDir := filepath.Join("..", "..", "fixtures")
	path := filepath.Join(fixturesDir, "firstjob", "python-santiago", "requests.json")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("fixtures are unavailable: %v", err)
	}
	fetcher, err := NewReplayFetcher(context.Background(), fixturesDir, "firstjob", "python-santiago")
	if err != nil {
		t.Fatalf("NewReplayFetcher() unexpected error: %v", err)
	}
	response, err := fetcher.Fetch(context.Background(), Request{
		URL: "https://firstjob.me/ofertas",
		Params: map[string]string{
			"title":     "python",
			"country[]": "43",
			"region[]":  "13",
		},
	})
	if err != nil {
		t.Fatalf("Fetch() unexpected error: %v", err)
	}
	if !response.IsSuccess() {
		t.Fatalf("status = %d, want success", response.StatusCode)
	}
	if len(response.Body) == 0 {
		t.Fatal("fixture body is empty")
	}
	if got := response.Header.Get("Content-Type"); !strings.Contains(got, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", got)
	}
	_, err = fetcher.Fetch(context.Background(), Request{URL: "https://firstjob.me/ofertas"})
	if !errors.Is(err, ErrMissingFixture) {
		t.Fatalf("second Fetch() error = %v, want ErrMissingFixture", err)
	}
}

func TestNewReplayFetcherMissingFixtureFile(t *testing.T) {
	_, err := NewReplayFetcher(context.Background(), t.TempDir(), "missing", "search")
	if err == nil {
		t.Fatal("NewReplayFetcher() error = nil, want an error")
	}
}
