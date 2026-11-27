package bot

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/NikoMalek/job-sniper-discord-bot/internal/store"
	"github.com/bwmarrin/discordgo"
)

type fakeRegistrar struct {
	appID    string
	guildID  string
	commands []*discordgo.ApplicationCommand
	err      error
	calls    int
}

func (r *fakeRegistrar) RegisterCommands(
	_ context.Context,
	appID, guildID string,
	commands []*discordgo.ApplicationCommand,
) error {
	r.calls++
	r.appID = appID
	r.guildID = guildID
	r.commands = commands
	return r.err
}

type fakeResolver struct {
	id  string
	err error
}

func (r *fakeResolver) ApplicationID(context.Context) (string, error) {
	return r.id, r.err
}

type fakeInteractionSender struct {
	err error

	mu        sync.Mutex
	responses []*discordgo.InteractionResponse
	followups []string
}

func (s *fakeInteractionSender) Respond(
	_ context.Context,
	_ *discordgo.Interaction,
	response *discordgo.InteractionResponse,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.responses = append(s.responses, response)
	return s.err
}

func (s *fakeInteractionSender) Followup(_ context.Context, _ *discordgo.Interaction, content string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.followups = append(s.followups, content)
	return s.err
}

func (s *fakeInteractionSender) snapshot() ([]*discordgo.InteractionResponse, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*discordgo.InteractionResponse(nil), s.responses...), append([]string(nil), s.followups...)
}

func newTestClient(t *testing.T) (*Client, *fakeStore, *fakeRegistrar, *fakeResolver, *fakeInteractionSender) {
	t.Helper()
	st := &fakeStore{}
	admin := newTestAdmin(t, st, basicRegistry())
	gateway := &fakeOnboardingGateway{}
	onboarding, err := NewOnboarding(st, gateway)
	if err != nil {
		t.Fatalf("NewOnboarding() error = %v", err)
	}
	registrar := &fakeRegistrar{}
	resolver := &fakeResolver{id: "app-1"}
	sender := &fakeInteractionSender{}
	client := &Client{
		session:    &discordgo.Session{},
		admin:      admin,
		onboarding: onboarding,
		registry:   basicRegistry(),
		registrar:  registrar,
		appLookup:  resolver,
		sender:     sender,
		ctx:        context.Background(),
		responded:  make(map[string]bool),
	}
	return client, st, registrar, resolver, sender
}

func TestRegisterCommandsUsesResolvedAppIDAndGlobalScope(t *testing.T) {
	client, _, registrar, resolver, _ := newTestClient(t)
	if err := client.RegisterCommands(context.Background()); err != nil {
		t.Fatalf("RegisterCommands() error = %v", err)
	}
	if resolver.id != "app-1" || registrar.appID != "app-1" || registrar.guildID != "" {
		t.Fatalf("registrar call = %q, %q (resolver %q)", registrar.appID, registrar.guildID, resolver.id)
	}
	if got, want := len(registrar.commands), 21; got != want {
		t.Fatalf("registered %d commands, want %d", got, want)
	}
	if client.applicationID != "app-1" {
		t.Fatalf("applicationID = %q, want app-1", client.applicationID)
	}
	resolver.err = errors.New("app lookup failed")
	if err := client.RegisterCommands(context.Background()); err != nil {
		t.Fatalf("second RegisterCommands() error = %v", err)
	}
	if registrar.calls != 2 {
		t.Fatalf("registrar calls = %d, want 2", registrar.calls)
	}
}

func TestRegisterCommandsPropagatesErrors(t *testing.T) {
	client, _, registrar, resolver, _ := newTestClient(t)
	resolver.err = errors.New("lookup failed")
	err := client.RegisterCommands(context.Background())
	if err == nil || !errors.Is(err, resolver.err) {
		t.Fatalf("RegisterCommands() error = %v, want wrapped lookup error", err)
	}

	client, _, registrar, resolver, _ = newTestClient(t)
	registrar.err = errors.New("sync failed")
	err = client.RegisterCommands(context.Background())
	if err == nil || !errors.Is(err, registrar.err) {
		t.Fatalf("RegisterCommands() error = %v, want wrapped sync error", err)
	}
	if resolver.id == "" {
		t.Fatal("resolver was not called")
	}
}

func TestOnInteractionCreateRejectsMissingPermissions(t *testing.T) {
	client, _, _, _, sender := newTestClient(t)
	interaction := &discordgo.Interaction{
		ID:             "i-1",
		Type:           discordgo.InteractionApplicationCommand,
		GuildID:        "10",
		AppPermissions: 0,
		Data:           discordgo.ApplicationCommandInteractionData{Name: "help"},
	}
	client.onInteractionCreate(nil, interaction)
	responses, followups := sender.snapshot()
	if len(responses) != 1 || len(followups) != 0 {
		t.Fatalf("responses = %#v, followups = %#v", responses, followups)
	}
	response := responses[0]
	if response.Type != discordgo.InteractionResponseChannelMessageWithSource {
		t.Fatalf("response type = %v", response.Type)
	}
	if response.Data.Content != "No tienes permiso para usar este comando." {
		t.Fatalf("content = %q", response.Data.Content)
	}
	if response.Data.Flags != discordgo.MessageFlagsEphemeral {
		t.Fatalf("flags = %v, want ephemeral", response.Data.Flags)
	}
}

func TestOnInteractionCreateRejectsDMs(t *testing.T) {
	client, _, _, _, sender := newTestClient(t)
	interaction := &discordgo.Interaction{
		ID:             "i-2",
		Type:           discordgo.InteractionApplicationCommand,
		AppPermissions: int64(discordgo.PermissionManageGuild),
		Data:           discordgo.ApplicationCommandInteractionData{Name: "help"},
	}
	client.onInteractionCreate(nil, interaction)
	responses, _ := sender.snapshot()
	if len(responses) != 1 || responses[0].Data.Content != "No tienes permiso para usar este comando." {
		t.Fatalf("responses = %#v", responses)
	}
}

func TestOnInteractionCreateDispatchesCommand(t *testing.T) {
	client, st, _, _, sender := newTestClient(t)
	interaction := &discordgo.Interaction{
		ID:             "i-3",
		Type:           discordgo.InteractionApplicationCommand,
		GuildID:        "10",
		AppPermissions: int64(discordgo.PermissionManageGuild),
		Data: discordgo.ApplicationCommandInteractionData{
			Name: "add-keyword",
			Options: []*discordgo.ApplicationCommandInteractionDataOption{
				{Name: "keyword", Type: discordgo.ApplicationCommandOptionString, Value: "python"},
			},
		},
	}
	client.onInteractionCreate(nil, interaction)
	responses, _ := sender.snapshot()
	if len(responses) != 1 {
		t.Fatalf("responses = %#v", responses)
	}
	if want := "Palabras clave agregadas: **python**."; responses[0].Data.Content != want {
		t.Fatalf("content = %q, want %q", responses[0].Data.Content, want)
	}
	if !reflect.DeepEqual(st.config.Keywords, []string{"python"}) {
		t.Fatalf("keywords = %#v", st.config.Keywords)
	}
}

func TestOnInteractionCreateAnswersAutocomplete(t *testing.T) {
	client, st, _, _, sender := newTestClient(t)
	st.config.Keywords = []string{"Python", "Backend"}
	interaction := &discordgo.Interaction{
		ID:      "i-4",
		Type:    discordgo.InteractionApplicationCommandAutocomplete,
		GuildID: "10",
		Data: discordgo.ApplicationCommandInteractionData{
			Name: "remove-keyword",
			Options: []*discordgo.ApplicationCommandInteractionDataOption{
				{Name: "keyword", Type: discordgo.ApplicationCommandOptionString, Value: "py", Focused: true},
			},
		},
	}
	client.onInteractionCreate(nil, interaction)
	responses, _ := sender.snapshot()
	if len(responses) != 1 {
		t.Fatalf("responses = %#v", responses)
	}
	response := responses[0]
	if response.Type != discordgo.InteractionApplicationCommandAutocompleteResult {
		t.Fatalf("response type = %v", response.Type)
	}
	want := []*discordgo.ApplicationCommandOptionChoice{{Name: "Python", Value: "Python"}}
	if !reflect.DeepEqual(response.Data.Choices, want) {
		t.Fatalf("choices = %#v, want %#v", response.Data.Choices, want)
	}
}

func TestCommandResponseCarriesEphemeralEmbed(t *testing.T) {
	response := commandResponse(embedResponse(BuildHelpEmbed()))
	if response.Type != discordgo.InteractionResponseChannelMessageWithSource {
		t.Fatalf("response type = %v", response.Type)
	}
	if response.Data.Flags != discordgo.MessageFlagsEphemeral {
		t.Fatalf("flags = %v, want ephemeral", response.Data.Flags)
	}
	if len(response.Data.Embeds) != 1 || response.Data.Embeds[0].Title != "Ayuda de Job Sniper" {
		t.Fatalf("embeds = %#v", response.Data.Embeds)
	}
	fields := response.Data.Embeds[0].Fields
	if len(fields) != 4 || fields[0].Inline {
		t.Fatalf("embed fields = %#v", fields)
	}
}

func TestNewSessionUsesOnlyGuildsIntent(t *testing.T) {
	session, err := newSession("test-token")
	if err != nil {
		t.Fatalf("newSession() error = %v", err)
	}
	if session.Identify.Intents != discordgo.IntentsGuilds {
		t.Fatalf("intents = %v, want %v", session.Identify.Intents, discordgo.IntentsGuilds)
	}
	if !session.StateEnabled {
		t.Fatal("state is disabled")
	}
	if _, err := newSession(""); err == nil {
		t.Fatal("newSession(empty) error = nil, want error")
	}
}

func TestHasManageGuild(t *testing.T) {
	tests := []struct {
		name        string
		permissions int64
		want        bool
	}{
		{name: "manage guild", permissions: int64(discordgo.PermissionManageGuild), want: true},
		{name: "administrator", permissions: int64(discordgo.PermissionAdministrator), want: true},
		{name: "send messages", permissions: int64(discordgo.PermissionSendMessages), want: false},
		{name: "none", permissions: 0, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := hasManageGuild(test.permissions); got != test.want {
				t.Fatalf("hasManageGuild(%d) = %v, want %v", test.permissions, got, test.want)
			}
		})
	}
}

func TestSnowflakeHelpers(t *testing.T) {
	if got := parseSnowflake("123"); got != 123 {
		t.Fatalf("parseSnowflake() = %d, want 123", got)
	}
	if got := parseSnowflake("nope"); got != 0 {
		t.Fatalf("parseSnowflake(invalid) = %d, want 0", got)
	}
	if got := optionalSnowflake(""); got != nil {
		t.Fatalf("optionalSnowflake(empty) = %v, want nil", got)
	}
	if got := optionalSnowflake("45"); got == nil || *got != 45 {
		t.Fatalf("optionalSnowflake(45) = %v, want 45", got)
	}
}

func TestDispatchUsesGuildFromInteraction(t *testing.T) {
	client, st, _, _, _ := newTestClient(t)
	response, err := client.dispatch(context.Background(), 77, discordgo.ApplicationCommandInteractionData{
		Name: "set-channel",
		Options: []*discordgo.ApplicationCommandInteractionDataOption{
			{Name: "channel", Type: discordgo.ApplicationCommandOptionChannel, Value: "88"},
		},
	})
	if err != nil {
		t.Fatalf("dispatch() error = %v", err)
	}
	if want := "Las vacantes se enviarán en <#88>."; response.Content != want {
		t.Fatalf("content = %q, want %q", response.Content, want)
	}
	if st.config.ChannelID == nil || *st.config.ChannelID != 88 {
		t.Fatalf("channel id = %v, want 88", st.config.ChannelID)
	}
}

func TestDispatchUnknownCommand(t *testing.T) {
	client, _, _, _, _ := newTestClient(t)
	_, err := client.dispatch(context.Background(), 1, discordgo.ApplicationCommandInteractionData{Name: "missing"})
	if err == nil {
		t.Fatal("dispatch(unknown) error = nil, want error")
	}
}

func TestStoreInterfaceSatisfaction(t *testing.T) {
	var _ Store = (*fakeStore)(nil)
	var _ PortalRegistry = (*fakeRegistry)(nil)
	var _ OnboardingGateway = (*fakeOnboardingGateway)(nil)
	var _ CommandRegistrar = (*fakeRegistrar)(nil)
	var _ ApplicationResolver = (*fakeResolver)(nil)
	var _ InteractionSender = (*fakeInteractionSender)(nil)
	if store.MaxMaxAgeHours <= store.MinMaxAgeHours {
		t.Fatal("invalid max-age bounds in store package")
	}
}
