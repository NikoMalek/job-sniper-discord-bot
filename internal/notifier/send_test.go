package notifier

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/NikoMalek/job-sniper-discord-bot/internal/job"
	"github.com/bwmarrin/discordgo"
)

type recordedSend struct {
	channelID string
	message   *discordgo.MessageSend
}

type stubMessageSender struct {
	sends []recordedSend
	errs  []error
}

func (s *stubMessageSender) SendMessage(
	ctx context.Context, channelID string, message *discordgo.MessageSend,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.sends = append(s.sends, recordedSend{channelID: channelID, message: message})
	if len(s.errs) == 0 {
		return nil
	}
	err := s.errs[0]
	s.errs = s.errs[1:]
	return err
}

func readAllFile(file *discordgo.File) ([]byte, error) {
	return io.ReadAll(file.Reader)
}

func discordRESTError(status int, text string) error {
	return &discordgo.RESTError{
		Response: &http.Response{
			StatusCode: status,
			Status:     text,
		},
		ResponseBody: []byte(text),
	}
}

func notifierTestJob() job.Job {
	return job.Job{
		ID:             "job-one",
		Title:          "Developer",
		Company:        "Acme",
		Location:       "Santiago",
		URL:            "https://example.com/job-one",
		Source:         "LinkedIn",
		PublishedAt:    "hace 1 hora",
		CompanyLogoURL: "https://ii.ct-stc.com/logo.png",
	}
}

func TestSendJobRetriesForbiddenWithLogo(t *testing.T) {
	ctx := context.Background()
	now := notifierTestNow()
	messages := &stubMessageSender{
		errs: []error{
			discordRESTError(http.StatusForbidden, "403 Forbidden"),
			nil,
		},
	}
	logos := &stubLogoFetcher{response: pngLogoResponse(pngLogo(), "image/png")}
	sender := NewSender(SenderOptions{
		Messages: messages,
		Logos:    logos,
		Now:      func() time.Time { return now },
	})

	ok, err := sender.SendJob(ctx, "channel-123", notifierTestJob())
	if err != nil {
		t.Fatalf("send job: %v", err)
	}
	if !ok {
		t.Fatal("send job reported failure")
	}
	if len(logos.calls) != 1 {
		t.Fatalf("logo fetched %d times, want 1", len(logos.calls))
	}
	if len(messages.sends) != 2 {
		t.Fatalf("sent %d messages, want 2", len(messages.sends))
	}
	for _, send := range messages.sends {
		if send.channelID != "channel-123" {
			t.Errorf("channel ID = %q", send.channelID)
		}
		if len(send.message.Embeds) != 1 {
			t.Fatalf("message has %d embeds", len(send.message.Embeds))
		}
	}

	first := messages.sends[0].message
	if first.Embeds[0].Thumbnail == nil ||
		first.Embeds[0].Thumbnail.URL != "attachment://company-logo.png" {
		t.Errorf("first thumbnail = %+v", first.Embeds[0].Thumbnail)
	}
	if len(first.Files) != 1 {
		t.Fatalf("first message has %d files", len(first.Files))
	}
	file := first.Files[0]
	if file.Name != "company-logo.png" || file.ContentType != "image/png" {
		t.Errorf("unexpected file: %+v", file)
	}
	content, err := readAllFile(file)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if !bytes.Equal(content, logos.response.Body) {
		t.Error("attached logo content differs from the downloaded body")
	}

	second := messages.sends[1].message
	if second.Embeds[0].Thumbnail == nil ||
		second.Embeds[0].Thumbnail.URL != "https://ii.ct-stc.com/logo.png" {
		t.Errorf("second thumbnail = %+v", second.Embeds[0].Thumbnail)
	}
	if len(second.Files) != 0 {
		t.Errorf("retry attached %d files, want 0", len(second.Files))
	}
}

func TestSendJobDiscordFailures(t *testing.T) {
	ctx := context.Background()
	now := notifierTestNow()
	generic := errors.New("rate limited")
	tests := []struct {
		name      string
		logoURL   string
		err       error
		wantSends int
	}{
		{
			"forbidden without attachment",
			"",
			discordRESTError(http.StatusForbidden, "403 Forbidden"),
			1,
		},
		{
			"not found with attachment",
			"https://ii.ct-stc.com/logo.png",
			discordRESTError(http.StatusNotFound, "404 Not Found"),
			1,
		},
		{
			"generic error",
			"",
			generic,
			1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			messages := &stubMessageSender{errs: []error{test.err}}
			logos := &stubLogoFetcher{response: pngLogoResponse(pngLogo(), "image/png")}
			sender := NewSender(SenderOptions{
				Messages: messages,
				Logos:    logos,
				Now:      func() time.Time { return now },
			})
			record := notifierTestJob()
			record.CompanyLogoURL = test.logoURL

			ok, err := sender.SendJob(ctx, "channel-123", record)
			if err != nil {
				t.Fatalf("send job: %v", err)
			}
			if ok {
				t.Fatal("send job reported success")
			}
			if len(messages.sends) != test.wantSends {
				t.Errorf("sent %d messages, want %d", len(messages.sends), test.wantSends)
			}
		})
	}
}

func TestSendJobsInputAndContext(t *testing.T) {
	now := notifierTestNow()
	messages := &stubMessageSender{}
	sender := NewSender(SenderOptions{
		Messages: messages,
		Logos:    &stubLogoFetcher{},
		Now:      func() time.Time { return now },
	})

	ok, err := sender.SendJobs(context.Background(), "channel-123", nil)
	if err != nil || ok {
		t.Fatalf("empty group = %v, %v", ok, err)
	}
	if len(messages.sends) != 0 {
		t.Fatalf("empty group sent %d messages", len(messages.sends))
	}

	missing := NewSender(SenderOptions{Logos: &stubLogoFetcher{}})
	ok, err = missing.SendJob(context.Background(), "channel-123", notifierTestJob())
	if ok || !errors.Is(err, ErrMissingSender) {
		t.Fatalf("missing sender = %v, %v", ok, err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	ok, err = sender.SendJob(canceled, "channel-123", notifierTestJob())
	if ok || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context = %v, %v", ok, err)
	}
	if len(messages.sends) != 0 {
		t.Fatalf("canceled context sent %d messages", len(messages.sends))
	}
}

func TestSendJobContinuesWhenLogoFails(t *testing.T) {
	ctx := context.Background()
	now := notifierTestNow()
	messages := &stubMessageSender{}
	logos := &stubLogoFetcher{err: errors.New("download failed")}
	sender := NewSender(SenderOptions{
		Messages: messages,
		Logos:    logos,
		Now:      func() time.Time { return now },
	})

	ok, err := sender.SendJob(ctx, "channel-123", notifierTestJob())
	if err != nil || !ok {
		t.Fatalf("send job = %v, %v", ok, err)
	}
	if len(messages.sends) != 1 || len(messages.sends[0].message.Files) != 0 {
		t.Fatalf("unexpected sends: %+v", messages.sends)
	}
}
