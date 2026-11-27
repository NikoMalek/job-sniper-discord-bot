package notifier

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/NikoMalek/job-sniper-discord-bot/internal/httpclient"
)

type stubLogoFetcher struct {
	response *httpclient.Response
	err      error
	calls    []string
}

func (f *stubLogoFetcher) FetchLogo(
	ctx context.Context, rawURL string,
) (*httpclient.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.calls = append(f.calls, rawURL)
	return f.response, f.err
}

func pngLogo() []byte {
	return append(
		[]byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a},
		make([]byte, 16)...,
	)
}

func pngLogoResponse(body []byte, contentType string) *httpclient.Response {
	return &httpclient.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Content-Type": []string{contentType},
		},
		Body: body,
	}
}

func TestShouldPrefetchLogo(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"https://ii.ct-stc.com/logo.png", true},
		{"https://II.CT-STC.com:443/logo.png", true},
		{"https://user:pass@ii.ct-stc.com/logo.png", true},
		{"https://firstjob.me/logo.png", true},
		{"https://www.firstjob.me/logo.png", true},
		{"http://ii.ct-stc.com/logo.png", false},
		{"https://example.com/logo.png", false},
		{"https://ii.ct-stc.com.evil.test/logo.png", false},
		{"ftp://ii.ct-stc.com/logo.png", false},
		{"not a url", false},
	}
	for _, test := range tests {
		t.Run(test.url, func(t *testing.T) {
			if got := ShouldPrefetchLogo(test.url); got != test.want {
				t.Errorf("ShouldPrefetchLogo(%q) = %v, want %v", test.url, got, test.want)
			}
		})
	}
}

func TestLogoExtension(t *testing.T) {
	png := pngLogo()
	jpeg := append([]byte{0xff, 0xd8, 0xff}, make([]byte, 16)...)
	gif87 := append([]byte("GIF87a"), make([]byte, 16)...)
	gif89 := append([]byte("GIF89a"), make([]byte, 16)...)
	webp := append(
		append([]byte("RIFF"), make([]byte, 4)...),
		append([]byte("WEBP"), make([]byte, 8)...)...,
	)
	unknown := bytes.Repeat([]byte("x"), 32)

	tests := []struct {
		name        string
		content     []byte
		contentType string
		want        string
	}{
		{"png", png, "image/png", "png"},
		{"png with charset", png, "image/png; charset=binary", "png"},
		{"jpeg", jpeg, "image/jpeg", "jpg"},
		{"gif87", gif87, "image/gif", "gif"},
		{"gif89", gif89, "image/gif", "gif"},
		{"webp", webp, "image/webp", "webp"},
		{"text content type", png, "text/html; charset=utf-8", ""},
		{"json content type", png, "application/json", ""},
		{"xml content type", png, "application/xml", ""},
		{"short content", []byte{0x89, 0x50}, "image/png", ""},
		{"unknown content", unknown, "image/png", ""},
		{"empty content", nil, "", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := logoExtension(test.content, test.contentType); got != test.want {
				t.Errorf("logoExtension() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestDownloadLogo(t *testing.T) {
	const rawURL = "https://ii.ct-stc.com/logo.png"
	t.Run("valid", func(t *testing.T) {
		fetcher := &stubLogoFetcher{response: pngLogoResponse(pngLogo(), "image/png")}
		attachment, err := DownloadLogo(context.Background(), rawURL, fetcher)
		if err != nil {
			t.Fatalf("download logo: %v", err)
		}
		if attachment == nil {
			t.Fatal("logo was not attached")
		}
		if attachment.Filename() != "company-logo.png" ||
			attachment.ContentType() != "image/png" ||
			!bytes.Equal(attachment.Content, fetcher.response.Body) {
			t.Errorf("unexpected attachment: %+v", attachment)
		}
		if len(fetcher.calls) != 1 || fetcher.calls[0] != rawURL {
			t.Errorf("fetcher calls = %v", fetcher.calls)
		}
	})

	tests := []struct {
		name     string
		response *httpclient.Response
	}{
		{"nil response", nil},
		{"non-200", &httpclient.Response{StatusCode: http.StatusPartialContent, Body: pngLogo()}},
		{"too small", pngLogoResponse([]byte("short"), "image/png")},
		{"wrong content type", pngLogoResponse(pngLogo(), "application/json")},
		{"wrong magic bytes", pngLogoResponse(bytes.Repeat([]byte("x"), 32), "image/png")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			attachment, err := DownloadLogo(
				context.Background(), rawURL,
				&stubLogoFetcher{response: test.response},
			)
			if err != nil {
				t.Fatalf("download logo: %v", err)
			}
			if attachment != nil {
				t.Errorf("attachment = %+v, want nil", attachment)
			}
		})
	}

	t.Run("fetch error", func(t *testing.T) {
		want := errors.New("boom")
		_, err := DownloadLogo(
			context.Background(), rawURL,
			&stubLogoFetcher{err: want},
		)
		if !errors.Is(err, want) {
			t.Errorf("error = %v, want %v", err, want)
		}
	})

	t.Run("canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		fetcher := &stubLogoFetcher{response: pngLogoResponse(pngLogo(), "image/png")}
		_, err := DownloadLogo(ctx, rawURL, fetcher)
		if !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v, want context.Canceled", err)
		}
		if len(fetcher.calls) != 0 {
			t.Errorf("fetcher was called %d times", len(fetcher.calls))
		}
	})
}
