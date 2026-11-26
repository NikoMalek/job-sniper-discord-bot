package httpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var (
	_ Fetcher = (*Client)(nil)
	_ Fetcher = (*ReplayFetcher)(nil)
)

// RecordedRequest is one entry of a fixture requests.json file. Params values
// are stored as strings even when the JSON value is a number.
type RecordedRequest struct {
	Seq         int               `json:"seq"`
	Label       string            `json:"label"`
	URL         string            `json:"url"`
	Params      map[string]string `json:"params"`
	Status      *int              `json:"status"`
	ContentType string            `json:"content_type"`
	File        string            `json:"file"`
}

// UnmarshalJSON preserves numeric params as their JSON text.
func (r *RecordedRequest) UnmarshalJSON(data []byte) error {
	var wire struct {
		Seq         int                        `json:"seq"`
		Label       string                     `json:"label"`
		URL         string                     `json:"url"`
		Params      map[string]json.RawMessage `json:"params"`
		Status      *int                       `json:"status"`
		ContentType string                     `json:"content_type"`
		File        string                     `json:"file"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return fmt.Errorf("decode recorded request: %w", err)
	}

	*r = RecordedRequest{
		Seq:         wire.Seq,
		Label:       wire.Label,
		URL:         wire.URL,
		Params:      make(map[string]string, len(wire.Params)),
		Status:      wire.Status,
		ContentType: wire.ContentType,
		File:        wire.File,
	}
	for name, raw := range wire.Params {
		var value string
		if err := json.Unmarshal(raw, &value); err == nil {
			r.Params[name] = value
			continue
		}
		r.Params[name] = strings.TrimSpace(string(raw))
	}
	return nil
}

// ReplayFetcher serves recorded fixture responses without any network access.
// It matches URL plus params and consumes each entry once so repeated
// requests with identical keys return the recorded responses in order.
type ReplayFetcher struct {
	dir     string
	entries []RecordedRequest

	mu    sync.Mutex
	used  []bool
	calls []Request
}

// NewReplayFetcher loads fixturesDir/portal/search/requests.json.
func NewReplayFetcher(
	ctx context.Context, fixturesDir, portal, search string,
) (*ReplayFetcher, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("new replay fetcher: %w", err)
	}

	dir := filepath.Join(fixturesDir, portal, search)
	path := filepath.Join(dir, "requests.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var entries []RecordedRequest
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("new replay fetcher: %w", err)
	}
	return &ReplayFetcher{
		dir:     dir,
		entries: entries,
		used:    make([]bool, len(entries)),
	}, nil
}

// Fetch returns the next unconsumed response with the same URL and params.
func (f *ReplayFetcher) Fetch(ctx context.Context, request Request) (*Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	key, err := canonicalRequest(request.URL, request.Params)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, cloneRequest(request))

	for index := range f.entries {
		if f.used[index] {
			continue
		}
		entry := f.entries[index]
		entryKey, err := canonicalRequest(entry.URL, entry.Params)
		if err != nil || entryKey != key {
			continue
		}
		f.used[index] = true
		if entry.Status == nil || entry.File == "" {
			return nil, fmt.Errorf(
				"%w: recorded request %q has no response", ErrNoResponse, entry.Label,
			)
		}
		path := filepath.Join(f.dir, filepath.Base(entry.File))
		body, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		header := make(http.Header)
		if entry.ContentType != "" {
			header.Set("Content-Type", entry.ContentType)
		}
		return &Response{
			StatusCode: *entry.Status,
			Header:     header,
			Body:       body,
			FinalURL:   entryKey,
		}, nil
	}
	return nil, fmt.Errorf("%w: %s", ErrMissingFixture, key)
}

// BeginCycle makes all recorded requests available again.
func (f *ReplayFetcher) BeginCycle() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for index := range f.used {
		f.used[index] = false
	}
	f.calls = nil
}

// CircuitOpen always reports false because replay never opens a circuit.
func (f *ReplayFetcher) CircuitOpen() bool { return false }

// Calls returns a copy of the requests received since the last BeginCycle.
func (f *ReplayFetcher) Calls() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]Request, len(f.calls))
	for index, call := range f.calls {
		result[index] = cloneRequest(call)
	}
	return result
}

// Remaining returns how many recorded responses have not been consumed.
func (f *ReplayFetcher) Remaining() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, used := range f.used {
		if !used {
			count++
		}
	}
	return count
}

func canonicalRequest(rawURL string, params map[string]string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse URL %q: %w", rawURL, err)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("URL %q must be absolute", rawURL)
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	query := parsed.Query()
	for name, value := range params {
		query.Set(name, value)
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func cloneRequest(request Request) Request {
	clone := request
	clone.Params = make(map[string]string, len(request.Params))
	for name, value := range request.Params {
		clone.Params[name] = value
	}
	clone.Headers = make(map[string]string, len(request.Headers))
	for name, value := range request.Headers {
		clone.Headers[name] = value
	}
	return clone
}
