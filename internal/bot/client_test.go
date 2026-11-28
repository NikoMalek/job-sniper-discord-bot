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

type fakeMessageEdit struct {
	channelID  string
	messageID  string
	components []discordgo.MessageComponent
}

type fakeInteractionSender struct {
	err error

	mu           sync.Mutex
	responses    []*discordgo.InteractionResponse
	followups    []string
	edits        []*discordgo.WebhookEdit
	messageEdits []fakeMessageEdit
	originalID   string
	originals    int
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

func (s *fakeInteractionSender) EditOriginal(
	_ context.Context,
	_ *discordgo.Interaction,
	edit *discordgo.WebhookEdit,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.edits = append(s.edits, edit)
	return s.err
}

func (s *fakeInteractionSender) EditMessage(
	_ context.Context, channelID, messageID string, components []discordgo.MessageComponent,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.messageEdits = append(s.messageEdits, fakeMessageEdit{
		channelID: channelID, messageID: messageID, components: components,
	})
	return s.err
}

func (s *fakeInteractionSender) OriginalResponse(
	_ context.Context, _ *discordgo.Interaction,
) (*discordgo.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.originals++
	if s.err != nil {
		return nil, s.err
	}
	return &discordgo.Message{ID: s.originalID}, nil
}

func (s *fakeInteractionSender) snapshot() ([]*discordgo.InteractionResponse, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*discordgo.InteractionResponse(nil), s.responses...), append([]string(nil), s.followups...)
}

func (s *fakeInteractionSender) snapshotExperimental() (
	[]*discordgo.InteractionResponse, []*discordgo.WebhookEdit, int,
) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*discordgo.InteractionResponse(nil), s.responses...),
		append([]*discordgo.WebhookEdit(nil), s.edits...), s.originals
}

func (s *fakeInteractionSender) messageEditSnapshot() []fakeMessageEdit {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]fakeMessageEdit(nil), s.messageEdits...)
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

func TestOnReadyClosesReadyChannelOnce(t *testing.T) {
	client, _, _, _, _ := newTestClient(t)
	client.onReady(&discordgo.Session{}, &discordgo.Ready{})
	select {
	case <-client.Ready():
	default:
		t.Fatal("Ready() channel was not closed by onReady")
	}
	client.onReady(&discordgo.Session{}, &discordgo.Ready{})
	select {
	case <-client.Ready():
	default:
		t.Fatal("Ready() channel changed after the second ready event")
	}
}

func TestOnReadySkipsUnavailableGuildsForOnboarding(t *testing.T) {
	client, st, _, _, _ := newTestClient(t)
	client.onReady(&discordgo.Session{}, &discordgo.Ready{
		Guilds: []*discordgo.Guild{{ID: "10", Unavailable: true}},
	})
	if st.setupChecks != 0 {
		t.Fatalf("setup checks = %d, want 0 for unavailable guild", st.setupChecks)
	}
}

func TestOnReadyChecksAvailableGuildsForOnboarding(t *testing.T) {
	client, st, _, _, _ := newTestClient(t)
	client.onReady(&discordgo.Session{}, &discordgo.Ready{
		Guilds: []*discordgo.Guild{{ID: "10", Unavailable: false, OwnerID: "99"}},
	})
	if st.setupChecks != 1 {
		t.Fatalf("setup checks = %d, want 1 for available guild", st.setupChecks)
	}
}

func TestOnGuildJoinDeliversAfterReadySkippedUnavailableGuild(t *testing.T) {
	client, st, _, _, _ := newTestClient(t)
	client.onReady(&discordgo.Session{}, &discordgo.Ready{
		Guilds: []*discordgo.Guild{{ID: "10", Unavailable: true}},
	})
	gateway := client.onboarding.gateway.(*fakeOnboardingGateway)
	gateway.channels = map[int64]ChannelInfo{
		20: {ID: 20, GuildID: 10, CanSend: true, CanEmbed: true},
	}
	gateway.users = map[int64]UserInfo{99: {ID: 99, Available: true}}
	client.onboarding.OnGuildJoin(context.Background(), GuildSnapshot{
		ID:      10,
		OwnerID: 99,
		TextChannels: []ChannelInfo{
			{ID: 20, GuildID: 10, CanSend: true, CanEmbed: true},
		},
	})
	channels, _, _ := gateway.snapshot()
	if want := []int64{20}; !reflect.DeepEqual(channels, want) {
		t.Fatalf("sent channels = %v, want %v", channels, want)
	}
	if st.markCount != 1 {
		t.Fatalf("markCount = %d, want 1", st.markCount)
	}
}

func TestRegisterCommandsUsesResolvedAppIDAndGlobalScope(t *testing.T) {
	client, _, registrar, resolver, _ := newTestClient(t)
	if err := client.RegisterCommands(context.Background()); err != nil {
		t.Fatalf("RegisterCommands() error = %v", err)
	}
	if resolver.id != "app-1" || registrar.appID != "app-1" || registrar.guildID != "" {
		t.Fatalf("registrar call = %q, %q (resolver %q)", registrar.appID, registrar.guildID, resolver.id)
	}
	if got, want := len(registrar.commands), 22; got != want {
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

func TestInteractionHandlerSignatureMatchesDiscordGo(t *testing.T) {
	client, _, _, _, _ := newTestClient(t)
	var handler func(*discordgo.Session, *discordgo.InteractionCreate) = client.onInteractionCreate
	if handler == nil {
		t.Fatal("interaction handler is nil")
	}
}

func TestOnInteractionCreateDefersPermissionsToDiscord(t *testing.T) {
	client, _, _, _, sender := newTestClient(t)
	interaction := &discordgo.Interaction{
		ID:             "i-1",
		Type:           discordgo.InteractionApplicationCommand,
		GuildID:        "10",
		AppPermissions: 0,
		Data:           discordgo.ApplicationCommandInteractionData{Name: "help"},
	}
	client.onInteractionCreate(nil, &discordgo.InteractionCreate{Interaction: interaction})
	responses, followups := sender.snapshot()
	if len(responses) != 1 || len(followups) != 0 {
		t.Fatalf("responses = %#v, followups = %#v", responses, followups)
	}
	response := responses[0]
	if response.Type != discordgo.InteractionResponseChannelMessageWithSource {
		t.Fatalf("response type = %v", response.Type)
	}
	if len(response.Data.Embeds) != 1 || response.Data.Embeds[0].Title != "Ayuda de Job Sniper" {
		t.Fatalf("embeds = %#v", response.Data.Embeds)
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
	client.onInteractionCreate(nil, &discordgo.InteractionCreate{Interaction: interaction})
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
		AppPermissions: 0,
		Data: discordgo.ApplicationCommandInteractionData{
			Name: "add-keyword",
			Options: []*discordgo.ApplicationCommandInteractionDataOption{
				{Name: "keyword", Type: discordgo.ApplicationCommandOptionString, Value: "python"},
			},
		},
	}
	client.onInteractionCreate(nil, &discordgo.InteractionCreate{Interaction: interaction})
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
	client.onInteractionCreate(nil, &discordgo.InteractionCreate{Interaction: interaction})
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

func TestDeferredMessageUpdateResponse(t *testing.T) {
	response := deferredMessageUpdateResponse()
	if response.Type != discordgo.InteractionResponseDeferredMessageUpdate {
		t.Fatalf("response type = %v", response.Type)
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
	var _ ExperimentalStore = (*fakeStore)(nil)
	var _ PortalRegistry = (*fakeRegistry)(nil)
	var _ OnboardingGateway = (*fakeOnboardingGateway)(nil)
	var _ CommandRegistrar = (*fakeRegistrar)(nil)
	var _ ApplicationResolver = (*fakeResolver)(nil)
	var _ InteractionSender = (*fakeInteractionSender)(nil)
	if store.MaxMaxAgeHours <= store.MinMaxAgeHours {
		t.Fatal("invalid max-age bounds in store package")
	}
}

func TestSyncExperimentalCommandsUsesGuildScope(t *testing.T) {
	client, _, registrar, _, _ := newTestClient(t)
	ctx := context.Background()

	if err := client.SyncExperimentalCommands(ctx, 10, true); err != nil {
		t.Fatalf("SyncExperimentalCommands(enabled) error = %v", err)
	}
	if registrar.appID != "app-1" || registrar.guildID != "10" {
		t.Fatalf("registrar call = %q, %q", registrar.appID, registrar.guildID)
	}
	names := make([]string, 0, len(registrar.commands))
	for _, command := range registrar.commands {
		names = append(names, command.Name)
	}
	want := []string{"snipers", "applications", "debug"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("commands = %v, want %v", names, want)
	}

	if err := client.SyncExperimentalCommands(ctx, 10, false); err != nil {
		t.Fatalf("SyncExperimentalCommands(disabled) error = %v", err)
	}
	if len(registrar.commands) != 0 {
		t.Fatalf("disabled commands = %#v, want none", registrar.commands)
	}
}

func TestSyncEnabledExperimentalGuildsRunsOnce(t *testing.T) {
	client, st, registrar, _, _ := newTestClient(t)
	client.experimentalStore = st
	_ = st.SetExperimentalEnabled(context.Background(), 10, true)
	client.syncEnabledExperimentalGuilds(context.Background())
	if registrar.calls != 1 || registrar.guildID != "10" || len(registrar.commands) != 3 {
		t.Fatalf("registrar state = %d calls, guild %q, %d commands", registrar.calls, registrar.guildID, len(registrar.commands))
	}
	client.syncEnabledExperimentalGuilds(context.Background())
	if registrar.calls != 1 {
		t.Fatalf("registrar calls = %d, want 1", registrar.calls)
	}
}

func TestOnInteractionCreateRoutesExperimentalInteractions(t *testing.T) {
	client, st, _, _, sender := newTestClient(t)
	created, err := NewExperimental(ExperimentalOptions{
		Store:    st,
		Registry: client.registry,
		Messages: sender,
	})
	if err != nil {
		t.Fatalf("NewExperimental() error = %v", err)
	}
	client.experimental = created

	command := expCommandInteraction(10, 7, "experimental")
	client.onInteractionCreate(nil, &discordgo.InteractionCreate{Interaction: command})
	responses, _, _ := sender.snapshotExperimental()
	if len(responses) != 1 || len(responses[0].Data.Embeds) != 1 ||
		responses[0].Data.Embeds[0].Title != "🧪 Zona Experimental" {
		t.Fatalf("experimental responses = %#v", responses)
	}

	autocomplete := expCommandInteraction(10, 7, "experimental")
	expSetCommandOptions(autocomplete, []*discordgo.ApplicationCommandInteractionDataOption{
		{Name: "accion", Value: "", Focused: true},
	})
	autocomplete.Type = discordgo.InteractionApplicationCommandAutocomplete
	client.onInteractionCreate(nil, &discordgo.InteractionCreate{Interaction: autocomplete})
	responses, _, _ = sender.snapshotExperimental()
	if len(responses) != 2 || responses[1].Type != discordgo.InteractionApplicationCommandAutocompleteResult {
		t.Fatalf("autocomplete responses = %#v", responses)
	}
	want := []*discordgo.ApplicationCommandOptionChoice{{Name: "Activar", Value: "activar"}}
	if !reflect.DeepEqual(responses[1].Data.Choices, want) {
		t.Fatalf("choices = %#v, want %#v", responses[1].Data.Choices, want)
	}

	repository := st.experimentalStore()
	repository.enabled = map[int64]bool{10: true}
	repository.tracked = map[string]store.TrackedJob{
		"token-1": {
			Token: "token-1", GuildID: 10, JobID: "job-1", Source: "Test",
			JobURL: "https://example.test/job", JobTitle: "Go Developer", Company: "Acme",
		},
	}
	component := expComponentInteraction(10, 7, "message-1", jobTrackPrefix+"token-1")
	client.onInteractionCreate(nil, &discordgo.InteractionCreate{Interaction: component})
	responses, _, _ = sender.snapshotExperimental()
	if len(responses) != 3 || responses[2].Type != discordgo.InteractionResponseModal ||
		responses[2].Data.CustomID == "" {
		t.Fatalf("component responses = %#v", responses)
	}

	modalCustomID := responses[2].Data.CustomID
	modal := expModalInteraction(10, 7, modalCustomID, "yes", "Buena oferta")
	client.onInteractionCreate(nil, &discordgo.InteractionCreate{Interaction: modal})
	responses, edits, originals := sender.snapshotExperimental()
	if len(responses) != 4 || len(edits) != 0 || originals != 0 {
		t.Fatalf("modal sender state = %d responses, %d edits, %d originals",
			len(responses), len(edits), originals)
	}
	if responses[3].Type != discordgo.InteractionResponseDeferredMessageUpdate {
		t.Fatalf("modal ack type = %v", responses[3].Type)
	}
	interaction, err := repository.GetJobInteraction(context.Background(), 1)
	if err != nil || interaction == nil || interaction.Applied == nil || !*interaction.Applied {
		t.Fatalf("job interaction = %#v, error = %v", interaction, err)
	}
	if interaction.Notes == nil || *interaction.Notes != "Buena oferta" {
		t.Fatalf("notes = %v, want %q", interaction.Notes, "Buena oferta")
	}
}
