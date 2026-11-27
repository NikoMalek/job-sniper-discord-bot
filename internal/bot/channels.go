package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"github.com/bwmarrin/discordgo"
)

// DiscordChannelResolver checks configured notification channels with the
// Discord session cache before falling back to a REST lookup.
type DiscordChannelResolver struct {
	session *discordgo.Session
}

// NewDiscordChannelResolver builds a resolver backed by a Discord session.
func NewDiscordChannelResolver(session *discordgo.Session) *DiscordChannelResolver {
	return &DiscordChannelResolver{session: session}
}

// ResolveChannel reports whether a configured channel can receive messages.
func (r *DiscordChannelResolver) ResolveChannel(
	ctx context.Context, channelID int64,
) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	id := strconv.FormatInt(channelID, 10)
	var channel *discordgo.Channel
	if r != nil && r.session != nil && r.session.State != nil {
		channel, _ = r.session.State.Channel(id)
	}
	if channel == nil {
		if r == nil || r.session == nil {
			return false, errors.New("discord session is nil")
		}
		fetched, err := r.session.Channel(id, discordgo.WithContext(ctx))
		if err != nil {
			return false, fmt.Errorf("fetch channel: %w", err)
		}
		channel = fetched
	}
	if channel == nil {
		return false, errors.New("channel is nil")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !MessageableChannelType(channel.Type) {
		slog.Warn(fmt.Sprintf("Configured channel %s cannot receive messages", id))
		return false, nil
	}
	return true, nil
}

// MessageableChannelType mirrors channels that implement discord.py's
// Messageable interface.
func MessageableChannelType(channelType discordgo.ChannelType) bool {
	switch channelType {
	case discordgo.ChannelTypeGuildText,
		discordgo.ChannelTypeDM,
		discordgo.ChannelTypeGuildVoice,
		discordgo.ChannelTypeGroupDM,
		discordgo.ChannelTypeGuildNews,
		discordgo.ChannelTypeGuildNewsThread,
		discordgo.ChannelTypeGuildPublicThread,
		discordgo.ChannelTypeGuildPrivateThread,
		discordgo.ChannelTypeGuildStageVoice:
		return true
	default:
		return false
	}
}
