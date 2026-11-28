package notifier

import (
	"context"
	"fmt"

	"github.com/NikoMalek/job-sniper-discord-bot/internal/job"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/languages"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/store"
	"github.com/bwmarrin/discordgo"
)

const trackingButtonPrefix = "job_track:"

// TrackingStore is the notification-side persistence surface used to decide
// whether a guild is in experimental mode and to store the button token.
type TrackingStore interface {
	IsExperimentalEnabled(ctx context.Context, guildID int64) (bool, error)
	GetGuildConfig(ctx context.Context, guildID int64) (store.GuildMonitorConfig, error)
	StoreTrackedJob(
		ctx context.Context, guildID int64,
		jobID, source, jobURL, jobTitle, company string,
	) (string, error)
}

func (s *Sender) trackingContext(
	ctx context.Context, guildID int64, first job.Job,
) (bool, string, string, error) {
	experimental := false
	if s.experimental != nil {
		experimental = *s.experimental
	} else if s.tracking != nil && guildID != 0 {
		enabled, err := s.tracking.IsExperimentalEnabled(ctx, guildID)
		if err != nil {
			return false, "", "", fmt.Errorf("read experimental mode: %w", err)
		}
		experimental = enabled
	}
	if !experimental {
		return false, "spanish", "", nil
	}
	if s.tracking == nil || guildID == 0 {
		return true, "spanish", "", nil
	}
	config, err := s.tracking.GetGuildConfig(ctx, guildID)
	if err != nil {
		return false, "", "", fmt.Errorf("read guild languages: %w", err)
	}
	language := languages.PreferredUILanguage(config.Languages)
	token, err := s.tracking.StoreTrackedJob(
		ctx, guildID, first.ID, first.Source, first.URL, first.Title, first.Company,
	)
	if err != nil {
		return false, "", "", fmt.Errorf("store tracking token: %w", err)
	}
	return true, language, token, nil
}

func trackingButton(language, token, jobURL string) discordgo.ActionsRow {
	button := discordgo.Button{
		Label: languages.TrackingText("view_job", language, nil),
		Style: discordgo.PrimaryButton,
	}
	if token == "" {
		button.Style = discordgo.LinkButton
		button.URL = jobURL
	} else {
		button.CustomID = trackingButtonPrefix + token
	}
	return discordgo.ActionsRow{Components: []discordgo.MessageComponent{button}}
}
