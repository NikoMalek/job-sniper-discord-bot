package notifier

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/NikoMalek/job-sniper-discord-bot/internal/httpclient"
)

const (
	// LogoMaxBytes is the largest logo body that may be attached.
	LogoMaxBytes = 8 * 1024 * 1024
	// LogoConnectTimeout bounds connection establishment and TLS handshake.
	LogoConnectTimeout = 3 * time.Second
	// LogoReadTimeout bounds a response header wait and the whole download.
	LogoReadTimeout = 6 * time.Second

	logoMinBytes = 16
)

var prefetchLogoHosts = map[string]struct{}{
	"ii.ct-stc.com":   {},
	"firstjob.me":     {},
	"www.firstjob.me": {},
}

// LogoFetcher retrieves a company logo body. Tests inject a stub so no network
// access is needed.
type LogoFetcher interface {
	FetchLogo(ctx context.Context, rawURL string) (*httpclient.Response, error)
}

// LogoAttachment is a validated logo ready for a Discord file upload.
type LogoAttachment struct {
	Extension string
	Content   []byte
}

// Filename returns the fixed attachment name expected by Discord embeds.
func (a *LogoAttachment) Filename() string {
	return "company-logo." + a.Extension
}

// ContentType returns the MIME type associated with the detected format.
func (a *LogoAttachment) ContentType() string {
	switch a.Extension {
	case "png":
		return "image/png"
	case "jpg":
		return "image/jpeg"
	case "gif":
		return "image/gif"
	case "webp":
		return "image/webp"
	default:
		return ""
	}
}

// ShouldPrefetchLogo reports whether a logo must be downloaded and attached
// because Discord's image proxy is unreliable for its host.
func ShouldPrefetchLogo(raw string) bool {
	if raw == "" {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if !strings.EqualFold(parsed.Scheme, "https") {
		return false
	}
	_, supported := prefetchLogoHosts[strings.ToLower(parsed.Hostname())]
	return supported
}

// DownloadLogo fetches and validates one logo. Unsupported or invalid logos
// produce a nil attachment without an error, matching Python's "skip" path.
func DownloadLogo(
	ctx context.Context, rawURL string, fetcher LogoFetcher,
) (*LogoAttachment, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("download logo: %w", err)
	}
	if fetcher == nil {
		fetcher = NewLogoFetcher()
	}
	response, err := fetcher.FetchLogo(ctx, rawURL)
	if err != nil {
		return nil, fmt.Errorf("download logo: %w", err)
	}
	if response == nil || response.StatusCode != http.StatusOK {
		slog.Debug("company logo returned a non-success status", "url", rawURL)
		return nil, nil
	}
	if len(response.Body) < logoMinBytes || len(response.Body) > LogoMaxBytes {
		return nil, nil
	}
	contentType := ""
	if response.Header != nil {
		contentType = response.Header.Get("Content-Type")
	}
	extension := logoExtension(response.Body, contentType)
	if extension == "" {
		slog.Debug("company logo is not a supported image", "url", rawURL)
		return nil, nil
	}
	return &LogoAttachment{
		Extension: extension,
		Content:   append([]byte(nil), response.Body...),
	}, nil
}

func logoExtension(content []byte, contentType string) string {
	mediaType := strings.ToLower(strings.TrimSpace(
		strings.SplitN(contentType, ";", 2)[0],
	))
	if strings.HasPrefix(mediaType, "text/") ||
		strings.Contains(mediaType, "json") ||
		strings.Contains(mediaType, "xml") {
		return ""
	}
	if len(content) >= 8 && bytes.Equal(
		content[:8], []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a},
	) {
		return "png"
	}
	if len(content) >= 3 && bytes.Equal(content[:3], []byte{0xff, 0xd8, 0xff}) {
		return "jpg"
	}
	if len(content) >= 6 &&
		(bytes.Equal(content[:6], []byte("GIF87a")) ||
			bytes.Equal(content[:6], []byte("GIF89a"))) {
		return "gif"
	}
	if len(content) >= 12 && bytes.Equal(content[:4], []byte("RIFF")) &&
		bytes.Equal(content[8:12], []byte("WEBP")) {
		return "webp"
	}
	return ""
}

// NewLogoFetcher builds the HTTP logo downloader used in production.
func NewLogoFetcher() LogoFetcher {
	dialer := &net.Dialer{Timeout: LogoConnectTimeout}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   LogoConnectTimeout,
		ResponseHeaderTimeout: LogoReadTimeout,
	}
	return &logoHTTPFetcher{
		client: &http.Client{
			Transport: transport,
			Timeout:   LogoConnectTimeout + LogoReadTimeout,
		},
		maxBytes: LogoMaxBytes,
	}
}

type logoHTTPFetcher struct {
	client   *http.Client
	maxBytes int64
}

func (f *logoHTTPFetcher) FetchLogo(
	ctx context.Context, rawURL string,
) (*httpclient.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create logo request: %w", err)
	}
	response, err := f.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch logo: %w", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, f.maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read logo: %w", err)
	}
	return &httpclient.Response{
		StatusCode: response.StatusCode,
		Header:     response.Header,
		Body:       body,
		FinalURL:   response.Request.URL.String(),
	}, nil
}
