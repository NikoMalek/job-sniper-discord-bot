package bot

import (
	"context"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestMessageableChannelType(t *testing.T) {
	tests := []struct {
		name  string
		type_ discordgo.ChannelType
		want  bool
	}{
		{"guild text", discordgo.ChannelTypeGuildText, true},
		{"guild news", discordgo.ChannelTypeGuildNews, true},
		{"voice", discordgo.ChannelTypeGuildVoice, true},
		{"stage", discordgo.ChannelTypeGuildStageVoice, true},
		{"public thread", discordgo.ChannelTypeGuildPublicThread, true},
		{"private thread", discordgo.ChannelTypeGuildPrivateThread, true},
		{"direct message", discordgo.ChannelTypeDM, true},
		{"group direct message", discordgo.ChannelTypeGroupDM, true},
		{"category", discordgo.ChannelTypeGuildCategory, false},
		{"forum", discordgo.ChannelTypeGuildForum, false},
		{"directory", discordgo.ChannelTypeGuildDirectory, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MessageableChannelType(tt.type_); got != tt.want {
				t.Fatalf("MessageableChannelType(%d) = %v, want %v", tt.type_, got, tt.want)
			}
		})
	}
}

func TestResolveChannelUsesCachedChannel(t *testing.T) {
	state := discordgo.NewState()
	if err := state.GuildAdd(&discordgo.Guild{ID: "1"}); err != nil {
		t.Fatalf("GuildAdd() error = %v", err)
	}
	if err := state.ChannelAdd(&discordgo.Channel{
		ID:      "10",
		Type:    discordgo.ChannelTypeGuildText,
		GuildID: "1",
	}); err != nil {
		t.Fatalf("ChannelAdd() error = %v", err)
	}
	resolver := NewDiscordChannelResolver(&discordgo.Session{State: state})
	available, err := resolver.ResolveChannel(context.Background(), 10)
	if err != nil {
		t.Fatalf("ResolveChannel() error = %v", err)
	}
	if !available {
		t.Fatal("ResolveChannel() = false, want true")
	}
}

func TestResolveChannelRejectsCachedCategory(t *testing.T) {
	state := discordgo.NewState()
	if err := state.GuildAdd(&discordgo.Guild{ID: "1"}); err != nil {
		t.Fatalf("GuildAdd() error = %v", err)
	}
	if err := state.ChannelAdd(&discordgo.Channel{
		ID:      "20",
		Type:    discordgo.ChannelTypeGuildCategory,
		GuildID: "1",
	}); err != nil {
		t.Fatalf("ChannelAdd() error = %v", err)
	}
	resolver := NewDiscordChannelResolver(&discordgo.Session{State: state})
	available, err := resolver.ResolveChannel(context.Background(), 20)
	if err != nil {
		t.Fatalf("ResolveChannel() error = %v", err)
	}
	if available {
		t.Fatal("ResolveChannel() = true, want false")
	}
}

func TestResolveChannelRequiresSessionAndLiveContext(t *testing.T) {
	resolver := NewDiscordChannelResolver(nil)
	if _, err := resolver.ResolveChannel(context.Background(), 10); err == nil {
		t.Fatal("ResolveChannel() with nil session error = nil, want error")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := resolver.ResolveChannel(ctx, 10); err == nil {
		t.Fatal("ResolveChannel() with canceled context error = nil, want error")
	}
}
