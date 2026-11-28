package notifier

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/NikoMalek/job-sniper-discord-bot/internal/job"
	"github.com/bwmarrin/discordgo"
)

// ErrMissingSender is returned when delivery is attempted without a channel
// sender.
var ErrMissingSender = errors.New("notifier: message sender is required")

// MessageSender is the narrow delivery contract used by Sender. Tests can
// implement it with a fake and never contact Discord.
type MessageSender interface {
	SendMessage(ctx context.Context, channelID string, message *discordgo.MessageSend) error
}

// DiscordMessageSender adapts discordgo's REST client to MessageSender.
type DiscordMessageSender struct {
	session *discordgo.Session
}

// NewDiscordMessageSender wraps a configured discordgo session.
func NewDiscordMessageSender(session *discordgo.Session) *DiscordMessageSender {
	return &DiscordMessageSender{session: session}
}

// SendMessage posts one prepared message to a channel.
func (s *DiscordMessageSender) SendMessage(
	ctx context.Context, channelID string, message *discordgo.MessageSend,
) error {
	if s == nil || s.session == nil {
		return errors.New("notifier: discordgo session is nil")
	}
	_, err := s.session.ChannelMessageSendComplex(
		channelID, message, discordgo.WithContext(ctx),
	)
	return err
}

// SenderOptions holds the deterministic dependencies of notification
// delivery. Logos default to the HTTP downloader and now defaults to UTC.
type SenderOptions struct {
	Messages     MessageSender
	Logos        LogoFetcher
	Now          func() time.Time
	Tracking     TrackingStore
	Experimental *bool
}

// Sender delivers job embeds with a single Forbidden retry for attached logos.
type Sender struct {
	messages     MessageSender
	logos        LogoFetcher
	now          func() time.Time
	tracking     TrackingStore
	experimental *bool
}

// NewSender builds a notification sender.
func NewSender(options SenderOptions) *Sender {
	logos := options.Logos
	if logos == nil {
		logos = NewLogoFetcher()
	}
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Sender{
		messages:     options.Messages,
		logos:        logos,
		now:          now,
		tracking:     options.Tracking,
		experimental: options.Experimental,
	}
}

// SendJobs delivers one non-experimental embed for a group of jobs.
func (s *Sender) SendJobs(
	ctx context.Context, channelID string, jobs []job.Job,
) (bool, error) {
	return s.SendJobsForGuild(ctx, 0, channelID, jobs)
}

// SendJobsForGuild delivers one group of jobs to a guild's channel. Experimental
// guilds get a clean embed with a stored tracking token behind the button.
func (s *Sender) SendJobsForGuild(
	ctx context.Context, guildID int64, channelID string, jobs []job.Job,
) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, fmt.Errorf("send jobs: %w", err)
	}
	if len(jobs) == 0 {
		return false, nil
	}
	if s == nil || s.messages == nil {
		return false, ErrMissingSender
	}
	first := jobs[0]

	experimental, language, token, err := s.trackingContext(ctx, guildID, first)
	if err != nil {
		return false, err
	}

	var attachment *LogoAttachment
	if ShouldPrefetchLogo(first.CompanyLogoURL) {
		logo, err := DownloadLogo(ctx, first.CompanyLogoURL, s.logos)
		if err != nil {
			slog.Debug(
				"could not prefetch company logo", "job", first.ID, "error", err,
			)
		}
		attachment = logo
	}

	attempts := make([]bool, 0, 2)
	if attachment != nil {
		attempts = append(attempts, true)
	}
	attempts = append(attempts, false)

	for _, withFile := range attempts {
		if err := ctx.Err(); err != nil {
			return false, fmt.Errorf("send jobs: %w", err)
		}
		message, err := s.prepareMessage(jobs, attachment, withFile, experimental, language, token)
		if err != nil {
			return false, err
		}
		err = s.messages.SendMessage(ctx, channelID, message)
		switch {
		case err == nil:
			return true, nil
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return false, fmt.Errorf("send jobs: %w", err)
		case discordStatusCode(err) == http.StatusForbidden && withFile:
			slog.Info(
				"cannot attach logo; retrying without it", "job", first.ID,
			)
			continue
		case discordStatusCode(err) == http.StatusForbidden,
			discordStatusCode(err) == http.StatusNotFound:
			slog.Warn("could not send job to channel", "job", first.ID, "error", err)
			return false, nil
		default:
			slog.Error(
				"Discord rejected notification", "job", first.ID, "error", err,
			)
			return false, nil
		}
	}
	return false, nil
}

// SendJob delivers a single non-experimental job notification.
func (s *Sender) SendJob(
	ctx context.Context, channelID string, record job.Job,
) (bool, error) {
	return s.SendJobs(ctx, channelID, []job.Job{record})
}

// SendJobForGuild delivers a single-job notification for one guild.
func (s *Sender) SendJobForGuild(
	ctx context.Context, guildID int64, channelID string, record job.Job,
) (bool, error) {
	return s.SendJobsForGuild(ctx, guildID, channelID, []job.Job{record})
}

func (s *Sender) prepareMessage(
	jobs []job.Job,
	attachment *LogoAttachment,
	withFile, experimental bool,
	language, token string,
) (*discordgo.MessageSend, error) {
	build := BuildJobsEmbed
	if experimental {
		build = BuildExperimentalJobsEmbed
	}
	embed, err := build(jobs, s.now())
	if err != nil {
		return nil, fmt.Errorf("prepare job message: %w", err)
	}
	message := &discordgo.MessageSend{
		Embeds: []*discordgo.MessageEmbed{embed},
	}
	if experimental {
		message.Components = []discordgo.MessageComponent{
			trackingButton(language, token, jobs[0].URL),
		}
	}
	if withFile && attachment != nil {
		filename := attachment.Filename()
		embed.Thumbnail = &discordgo.MessageEmbedThumbnail{
			URL: "attachment://" + filename,
		}
		message.Files = []*discordgo.File{{
			Name:        filename,
			ContentType: attachment.ContentType(),
			Reader:      bytes.NewReader(attachment.Content),
		}}
	}
	return message, nil
}

func discordStatusCode(err error) int {
	var pointer *discordgo.RESTError
	if errors.As(err, &pointer) && pointer != nil && pointer.Response != nil {
		return pointer.Response.StatusCode
	}
	var value discordgo.RESTError
	if errors.As(err, &value) && value.Response != nil {
		return value.Response.StatusCode
	}
	return 0
}
