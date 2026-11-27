package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
)

// ChannelInfo is the onboarding view of a guild text channel.
type ChannelInfo struct {
	ID       int64
	Name     string
	GuildID  int64
	CanSend  bool
	CanEmbed bool
}

// UserInfo is the onboarding view of a guild owner.
type UserInfo struct {
	ID        int64
	Available bool
}

// GuildSnapshot is the Discord-independent guild data needed by onboarding.
type GuildSnapshot struct {
	ID              int64
	SystemChannelID *int64
	TextChannels    []ChannelInfo
	OwnerID         int64
}

// OnboardingGateway isolates Discord reads and message sends.
type OnboardingGateway interface {
	Channel(ctx context.Context, channelID int64) (ChannelInfo, error)
	User(ctx context.Context, guildID, userID int64) (UserInfo, error)
	SendChannel(ctx context.Context, channelID int64, embed *Embed) error
	SendUser(ctx context.Context, userID int64, embed *Embed) error
}

// Onboarding delivers the one-time setup guide to guilds.
type Onboarding struct {
	store   Store
	gateway OnboardingGateway

	mu      sync.Mutex
	checked bool
	sending map[int64]struct{}
}

// NewOnboarding builds an onboarding service.
func NewOnboarding(store Store, gateway OnboardingGateway) (*Onboarding, error) {
	if store == nil {
		return nil, errors.New("onboarding requires a store")
	}
	if gateway == nil {
		return nil, errors.New("onboarding requires a gateway")
	}
	return &Onboarding{store: store, gateway: gateway, sending: make(map[int64]struct{})}, nil
}

// OnReady checks existing guilds once.
func (o *Onboarding) OnReady(ctx context.Context, guilds []GuildSnapshot) {
	o.mu.Lock()
	if o.checked {
		o.mu.Unlock()
		return
	}
	o.checked = true
	o.mu.Unlock()
	for _, guild := range guilds {
		if err := o.SendIfNeeded(ctx, guild); err != nil {
			slog.Warn("Could not deliver setup guide", "guild_id", guild.ID, "error", err)
		}
	}
}

// OnGuildJoin tries to deliver the guide to a newly joined guild.
func (o *Onboarding) OnGuildJoin(ctx context.Context, guild GuildSnapshot) {
	if err := o.SendIfNeeded(ctx, guild); err != nil {
		slog.Warn("Could not deliver setup guide", "guild_id", guild.ID, "error", err)
	}
}

// SendIfNeeded delivers and records the guide unless it was already sent or is
// being sent concurrently for the same guild.
func (o *Onboarding) SendIfNeeded(ctx context.Context, guild GuildSnapshot) error {
	o.mu.Lock()
	if _, ok := o.sending[guild.ID]; ok {
		o.mu.Unlock()
		return nil
	}
	o.sending[guild.ID] = struct{}{}
	o.mu.Unlock()
	defer func() {
		o.mu.Lock()
		delete(o.sending, guild.ID)
		o.mu.Unlock()
	}()

	sent, err := o.store.HasSetupMessage(ctx, guild.ID)
	if err != nil {
		return fmt.Errorf("check setup message: %w", err)
	}
	if sent {
		return nil
	}
	delivered, err := o.SendGuide(ctx, guild)
	if err != nil {
		return fmt.Errorf("send setup guide: %w", err)
	}
	if delivered {
		if err := o.store.MarkSetupMessageSent(ctx, guild.ID); err != nil {
			return fmt.Errorf("mark setup message sent: %w", err)
		}
	}
	return nil
}

// SendGuide tries every eligible channel, then the guild owner by DM.
func (o *Onboarding) SendGuide(ctx context.Context, guild GuildSnapshot) (bool, error) {
	embed := BuildSetupEmbed()
	channels, err := o.candidateChannels(ctx, guild)
	if err != nil {
		return false, fmt.Errorf("list candidate channels: %w", err)
	}
	for _, channel := range channels {
		if err := o.gateway.SendChannel(ctx, channel.ID, embed); err != nil {
			slog.Warn("Could not send setup guide to channel",
				"channel_id", channel.ID, "channel_name", channel.Name,
				"guild_id", guild.ID, "error", err)
			continue
		}
		slog.Info("Sent setup guide to channel",
			"channel_id", channel.ID, "channel_name", channel.Name, "guild_id", guild.ID)
		return true, nil
	}

	user, err := o.gateway.User(ctx, guild.ID, guild.OwnerID)
	if err != nil || !user.Available {
		slog.Warn("No channel or owner DM available for setup guide", "guild_id", guild.ID)
		return false, nil
	}
	if err := o.gateway.SendUser(ctx, user.ID, embed); err != nil {
		slog.Warn("Could not DM setup guide to owner", "guild_id", guild.ID, "error", err)
		return false, nil
	}
	slog.Info("Sent setup guide by DM to owner", "guild_id", guild.ID)
	return true, nil
}

// candidateChannels mirrors the Python ordering, deduplication and permission
// filtering. Channels without permission still consume their id, as in Python.
func (o *Onboarding) candidateChannels(ctx context.Context, guild GuildSnapshot) ([]ChannelInfo, error) {
	config, err := o.store.GetGuildConfig(ctx, guild.ID)
	if err != nil {
		return nil, fmt.Errorf("read guild config: %w", err)
	}
	candidates := make([]ChannelInfo, 0, len(guild.TextChannels)+2)
	seen := make(map[int64]struct{})
	add := func(channel ChannelInfo, requireGuild bool) {
		if channel.ID == 0 {
			return
		}
		if _, ok := seen[channel.ID]; ok {
			return
		}
		seen[channel.ID] = struct{}{}
		if requireGuild && channel.GuildID != guild.ID {
			return
		}
		if channel.CanSend && channel.CanEmbed {
			candidates = append(candidates, channel)
		}
	}

	if config.ChannelID != nil {
		channel, err := o.gateway.Channel(ctx, *config.ChannelID)
		if err == nil {
			add(channel, true)
		} else {
			slog.Warn("Could not load configured setup channel", "channel_id", *config.ChannelID, "error", err)
		}
	}
	if guild.SystemChannelID != nil {
		channel, err := o.gateway.Channel(ctx, *guild.SystemChannelID)
		if err == nil {
			add(channel, false)
		} else {
			slog.Warn("Could not load system channel", "channel_id", *guild.SystemChannelID, "error", err)
		}
	}
	for _, channel := range guild.TextChannels {
		add(channel, false)
	}
	return candidates, nil
}
