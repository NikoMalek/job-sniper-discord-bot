package notifier

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/NikoMalek/job-sniper-discord-bot/internal/job"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/store"
	"github.com/bwmarrin/discordgo"
)

type stubTrackingStore struct {
	enabled    bool
	config     store.GuildMonitorConfig
	token      string
	enabledErr error
	configErr  error
	tokenErr   error
	tokenCalls int
}

func (s *stubTrackingStore) IsExperimentalEnabled(context.Context, int64) (bool, error) {
	return s.enabled, s.enabledErr
}

func (s *stubTrackingStore) GetGuildConfig(context.Context, int64) (store.GuildMonitorConfig, error) {
	return s.config, s.configErr
}

func (s *stubTrackingStore) StoreTrackedJob(
	_ context.Context, guildID int64,
	jobID, source, jobURL, jobTitle, company string,
) (string, error) {
	s.tokenCalls++
	if s.tokenErr != nil {
		return "", s.tokenErr
	}
	if guildID == 0 || jobID == "" || source == "" || jobURL == "" || jobTitle == "" || company == "" {
		return "", errors.New("incomplete tracked job")
	}
	return s.token, nil
}

func trackingButtonFrom(components []discordgo.MessageComponent) *discordgo.Button {
	for _, component := range components {
		switch row := component.(type) {
		case discordgo.ActionsRow:
			for _, child := range row.Components {
				if button, ok := child.(discordgo.Button); ok {
					return &button
				}
			}
		case *discordgo.ActionsRow:
			for _, child := range row.Components {
				if button, ok := child.(discordgo.Button); ok {
					return &button
				}
			}
		}
	}
	return nil
}

func TestSendJobsForGuildExperimentalMode(t *testing.T) {
	yes := true
	no := false
	tests := []struct {
		name           string
		tracking       *stubTrackingStore
		guildID        int64
		experimental   *bool
		wantButton     bool
		wantLink       bool
		wantComponents int
		wantTokenCalls int
	}{
		{
			name: "enabled guild uses stored token",
			tracking: &stubTrackingStore{
				enabled: true,
				config:  store.GuildMonitorConfig{Languages: []string{"english"}},
				token:   "abc123",
			},
			guildID:        10,
			wantButton:     true,
			wantComponents: 1,
			wantTokenCalls: 1,
		},
		{
			name:           "disabled guild keeps the linked title",
			tracking:       &stubTrackingStore{},
			guildID:        10,
			wantTokenCalls: 0,
		},
		{
			name:           "missing guild keeps the linked title",
			tracking:       &stubTrackingStore{enabled: true},
			guildID:        0,
			wantTokenCalls: 0,
		},
		{
			name:           "explicit experimental mode without a guild uses a link button",
			tracking:       nil,
			guildID:        0,
			experimental:   &yes,
			wantButton:     true,
			wantLink:       true,
			wantComponents: 1,
		},
		{
			name: "explicit disabled mode overrides the guild flag",
			tracking: &stubTrackingStore{
				enabled: true,
				config:  store.GuildMonitorConfig{Languages: []string{"english"}},
				token:   "ignored",
			},
			guildID:      10,
			experimental: &no,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			messages := &stubMessageSender{}
			sender := NewSender(SenderOptions{
				Messages:     messages,
				Logos:        &stubLogoFetcher{},
				Now:          func() time.Time { return notifierTestNow() },
				Tracking:     test.tracking,
				Experimental: test.experimental,
			})
			record := notifierTestJob()
			record.Description = "A short description"

			ok, err := sender.SendJobsForGuild(
				context.Background(), test.guildID, "channel-1", []job.Job{record},
			)
			if err != nil || !ok {
				t.Fatalf("SendJobsForGuild() = %v, %v", ok, err)
			}
			if len(messages.sends) != 1 {
				t.Fatalf("sent %d messages, want 1", len(messages.sends))
			}
			message := messages.sends[0].message
			embed := message.Embeds[0]
			if test.wantButton {
				if embed.URL != "" {
					t.Errorf("experimental embed URL = %q, want empty", embed.URL)
				}
				if strings.Contains(embed.Description, "[Ver más...]") ||
					!strings.Contains(embed.Description, "A short description…") {
					t.Errorf("experimental description = %q", embed.Description)
				}
				button := trackingButtonFrom(message.Components)
				if button == nil {
					t.Fatalf("components = %#v, want a tracking button", message.Components)
				}
				if test.wantLink {
					if button.CustomID != "" || button.URL != record.URL {
						t.Errorf("link button = %#v, want job URL %q", button, record.URL)
					}
					if button.Label != "🔗 Ver oferta" {
						t.Errorf("button label = %q, want Spanish label", button.Label)
					}
					if button.Style != discordgo.LinkButton {
						t.Errorf("button style = %v, want link", button.Style)
					}
				} else {
					if button.CustomID != "job_track:abc123" {
						t.Errorf("custom ID = %q", button.CustomID)
					}
					if button.Label != "🔗 View Job" {
						t.Errorf("button label = %q, want localized label", button.Label)
					}
					if button.Style != discordgo.PrimaryButton {
						t.Errorf("button style = %v, want primary", button.Style)
					}
				}
			} else {
				if embed.URL != record.URL {
					t.Errorf("embed URL = %q, want %q", embed.URL, record.URL)
				}
				if !strings.Contains(embed.Description, "[Ver más...]") {
					t.Errorf("description = %q, want the linked suffix", embed.Description)
				}
				if len(message.Components) != 0 {
					t.Errorf("components = %#v, want none", message.Components)
				}
			}
			if test.tracking != nil && test.tracking.tokenCalls != test.wantTokenCalls {
				t.Errorf("token calls = %d, want %d", test.tracking.tokenCalls, test.wantTokenCalls)
			}
		})
	}
}

func TestSendJobsForGuildTrackingErrors(t *testing.T) {
	ctx := context.Background()
	wantErr := errors.New("token store failed")
	tests := []struct {
		name     string
		tracking *stubTrackingStore
	}{
		{
			"mode lookup",
			&stubTrackingStore{enabledErr: errors.New("mode failed")},
		},
		{
			"config lookup",
			&stubTrackingStore{enabled: true, configErr: errors.New("config failed")},
		},
		{
			"token storage",
			&stubTrackingStore{enabled: true, tokenErr: wantErr},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			messages := &stubMessageSender{}
			sender := NewSender(SenderOptions{
				Messages: messages,
				Logos:    &stubLogoFetcher{},
				Now:      func() time.Time { return notifierTestNow() },
				Tracking: test.tracking,
			})
			ok, err := sender.SendJobForGuild(ctx, 10, "channel-1", notifierTestJob())
			if ok || err == nil {
				t.Fatalf("SendJobForGuild() = %v, %v; want error", ok, err)
			}
			if test.name == "token storage" && !errors.Is(err, wantErr) {
				t.Errorf("error = %v, want wrapped token error", err)
			}
			if len(messages.sends) != 0 {
				t.Errorf("sent %d messages after a tracking error", len(messages.sends))
			}
		})
	}
}
