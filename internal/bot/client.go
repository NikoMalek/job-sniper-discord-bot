package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/NikoMalek/job-sniper-discord-bot/internal/config"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/data"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/matching"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/scrapers"
	"github.com/bwmarrin/discordgo"
)

// CommandRegistrar registers application commands with Discord.
type CommandRegistrar interface {
	RegisterCommands(ctx context.Context, appID, guildID string, commands []*discordgo.ApplicationCommand) error
}

// ApplicationResolver resolves the bot application id from its token.
type ApplicationResolver interface {
	ApplicationID(ctx context.Context) (string, error)
}

// InteractionSender sends initial interaction responses and followups.
type InteractionSender interface {
	Respond(ctx context.Context, interaction *discordgo.Interaction, response *discordgo.InteractionResponse) error
	Followup(ctx context.Context, interaction *discordgo.Interaction, content string) error
}

// ClientOptions configures a Discord client.
type ClientOptions struct {
	Token             string
	AppID             string
	Settings          config.Settings
	Store             Store
	Registry          PortalRegistry
	Catalog           *data.Catalog
	Matcher           *matching.Matcher
	Admin             *Admin
	Onboarding        *Onboarding
	Session           *discordgo.Session
	CommandRegistrar  CommandRegistrar
	ApplicationLookup ApplicationResolver
	InteractionSender InteractionSender
}

// Client owns the Discord session and dispatches guild events and commands.
type Client struct {
	session       *discordgo.Session
	admin         *Admin
	onboarding    *Onboarding
	registry      PortalRegistry
	applicationID string
	registrar     CommandRegistrar
	appLookup     ApplicationResolver
	sender        InteractionSender

	mu        sync.Mutex
	ctx       context.Context
	responded map[string]bool
	closeOnce sync.Once
	closeErr  error
	readyOnce sync.Once
	ready     chan struct{}
}

// NewClient builds a client and registers its event handlers.
func NewClient(options ClientOptions) (*Client, error) {
	if options.Store == nil && options.Admin == nil {
		return nil, errors.New("client requires a store or an admin")
	}
	session := options.Session
	if session == nil {
		created, err := newSession(options.Token)
		if err != nil {
			return nil, fmt.Errorf("create Discord session: %w", err)
		}
		session = created
	}
	registry := options.Registry
	if registry == nil {
		registry = scrapers.DefaultRegistry()
	}
	admin := options.Admin
	if admin == nil {
		created, err := NewAdmin(context.Background(), AdminOptions{
			Store:    options.Store,
			Registry: registry,
			Catalog:  options.Catalog,
			Matcher:  options.Matcher,
			Settings: options.Settings,
			Latency:  func() time.Duration { return session.HeartbeatLatency() },
		})
		if err != nil {
			return nil, fmt.Errorf("create admin commands: %w", err)
		}
		admin = created
	}
	gateway := &discordOnboardingGateway{session: session}
	onboarding := options.Onboarding
	if onboarding == nil {
		created, err := NewOnboarding(options.Store, gateway)
		if err != nil {
			return nil, fmt.Errorf("create onboarding: %w", err)
		}
		onboarding = created
	}
	registrar := options.CommandRegistrar
	if registrar == nil {
		registrar = &discordCommandRegistrar{session: session}
	}
	appLookup := options.ApplicationLookup
	if appLookup == nil {
		appLookup = &discordApplicationResolver{session: session}
	}
	sender := options.InteractionSender
	if sender == nil {
		sender = &discordInteractionSender{session: session}
	}
	client := &Client{
		session:       session,
		admin:         admin,
		onboarding:    onboarding,
		registry:      registry,
		applicationID: options.AppID,
		registrar:     registrar,
		appLookup:     appLookup,
		sender:        sender,
		ctx:           context.Background(),
		responded:     make(map[string]bool),
		ready:         make(chan struct{}),
	}
	session.AddHandler(client.onReady)
	session.AddHandler(client.onGuildCreate)
	session.AddHandler(client.onInteractionCreate)
	return client, nil
}

func newSession(token string) (*discordgo.Session, error) {
	if token == "" {
		return nil, errors.New("discord token is empty")
	}
	if len(token) < 4 || token[:4] != "Bot " {
		token = "Bot " + token
	}
	session, err := discordgo.New(token)
	if err != nil {
		return nil, fmt.Errorf("discordgo.New: %w", err)
	}
	session.Identify.Intents = discordgo.IntentsGuilds
	session.StateEnabled = true
	return session, nil
}

// Session exposes the underlying discordgo session for shutdown and wiring.
func (c *Client) Session() *discordgo.Session { return c.session }

// Ready returns a channel that closes after the first READY event.
func (c *Client) Ready() <-chan struct{} {
	return c.readyChannel()
}

func (c *Client) readyChannel() chan struct{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ready == nil {
		c.ready = make(chan struct{})
	}
	return c.ready
}

// Run registers global commands, opens the gateway and waits for cancellation.
func (c *Client) Run(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.ctx = ctx
	c.mu.Unlock()
	if err := c.RegisterCommands(ctx); err != nil {
		return fmt.Errorf("register commands: %w", err)
	}
	if err := c.session.Open(); err != nil {
		return fmt.Errorf("open Discord session: %w", err)
	}
	<-ctx.Done()
	return c.Close()
}

// Close closes the Discord session exactly once.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.closeErr = c.session.Close()
	})
	return c.closeErr
}

// RegisterCommands overwrites the global command set with the admin commands.
func (c *Client) RegisterCommands(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	appID := c.applicationID
	if appID == "" {
		resolved, err := c.appLookup.ApplicationID(ctx)
		if err != nil {
			return fmt.Errorf("resolve application id: %w", err)
		}
		appID = resolved
		c.applicationID = resolved
	}
	commands := AdminCommandDefinitions(PortalChoices(c.registry))
	if err := c.registrar.RegisterCommands(ctx, appID, "", commands); err != nil {
		return fmt.Errorf("bulk overwrite commands: %w", err)
	}
	slog.Info("Synchronized application commands", "count", len(commands))
	return nil
}

func (c *Client) baseContext() context.Context {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ctx == nil {
		return context.Background()
	}
	return c.ctx
}

func (c *Client) onReady(session *discordgo.Session, ready *discordgo.Ready) {
	c.readyOnce.Do(func() { close(c.readyChannel()) })
	if session.State != nil && session.State.User != nil {
		slog.Info(fmt.Sprintf("Connected as %s (%s)", session.State.User.String(), session.State.User.ID))
	}
	guilds := make([]GuildSnapshot, 0)
	if session.State != nil {
		for _, guild := range session.State.Guilds {
			guilds = append(guilds, c.guildSnapshot(c.baseContext(), guild))
		}
	}
	if len(guilds) == 0 && ready != nil {
		for _, guild := range ready.Guilds {
			guilds = append(guilds, c.guildSnapshot(c.baseContext(), guild))
		}
	}
	c.onboarding.OnReady(c.baseContext(), guilds)
}

func (c *Client) onGuildCreate(session *discordgo.Session, created *discordgo.GuildCreate) {
	if created == nil || created.Guild == nil {
		return
	}
	c.onboarding.OnGuildJoin(c.baseContext(), c.guildSnapshot(c.baseContext(), created.Guild))
}

func (c *Client) guildSnapshot(ctx context.Context, guild *discordgo.Guild) GuildSnapshot {
	snapshot := GuildSnapshot{
		ID:              parseSnowflake(guild.ID),
		SystemChannelID: optionalSnowflake(guild.SystemChannelID),
		OwnerID:         parseSnowflake(guild.OwnerID),
	}
	for _, channel := range guild.Channels {
		if channel == nil || channel.Type != discordgo.ChannelTypeGuildText {
			continue
		}
		snapshot.TextChannels = append(snapshot.TextChannels, c.channelInfo(ctx, channel))
	}
	return snapshot
}

func parseSnowflake(value string) int64 {
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0
	}
	return id
}

func optionalSnowflake(value string) *int64 {
	id := parseSnowflake(value)
	if id == 0 {
		return nil
	}
	return &id
}

func (c *Client) channelInfo(ctx context.Context, channel *discordgo.Channel) ChannelInfo {
	info := ChannelInfo{
		ID:      parseSnowflake(channel.ID),
		Name:    channel.Name,
		GuildID: parseSnowflake(channel.GuildID),
	}
	if err := ctx.Err(); err != nil {
		return info
	}
	userID := c.currentUserID()
	if userID == "" {
		return info
	}
	permissions, err := c.session.UserChannelPermissions(userID, channel.ID)
	if err != nil {
		slog.Warn("Could not read channel permissions", "channel_id", channel.ID, "error", err)
		return info
	}
	info.CanSend = permissions&discordgo.PermissionSendMessages != 0
	info.CanEmbed = permissions&discordgo.PermissionEmbedLinks != 0
	return info
}

func (c *Client) currentUserID() string {
	if c.session.State != nil && c.session.State.User != nil {
		return c.session.State.User.ID
	}
	user, err := c.session.User("@me")
	if err != nil || user == nil {
		return ""
	}
	return user.ID
}

func (c *Client) onInteractionCreate(_ *discordgo.Session, event *discordgo.InteractionCreate) {
	if event == nil || event.Interaction == nil {
		return
	}
	interaction := event.Interaction
	ctx := c.baseContext()
	if interaction.Type == discordgo.InteractionApplicationCommandAutocomplete {
		c.handleAutocomplete(ctx, interaction)
		return
	}
	if interaction.Type != discordgo.InteractionApplicationCommand {
		return
	}
	if parseSnowflake(interaction.GuildID) == 0 {
		c.respond(ctx, interaction, c.admin.ErrorResponse(ErrPermissionDenied))
		return
	}
	response, err := c.dispatch(ctx, parseSnowflake(interaction.GuildID), interaction.ApplicationCommandData())
	if err != nil {
		response = c.admin.ErrorResponse(err)
	}
	c.respond(ctx, interaction, response)
}

func (c *Client) handleAutocomplete(ctx context.Context, interaction *discordgo.Interaction) {
	data := interaction.ApplicationCommandData()
	name, current := focusedOption(data)
	var choices []Choice
	var err error
	guildID := parseSnowflake(interaction.GuildID)
	switch data.Name {
	case "remove-location":
		choices, err = c.admin.RemoveLocationAutocomplete(ctx, guildID, current)
	case "remove-keyword":
		choices, err = c.admin.RemoveKeywordAutocomplete(ctx, guildID, current)
	case "remove-blacklist-keyword":
		choices, err = c.admin.RemoveBlacklistKeywordAutocomplete(ctx, guildID, current)
	case "remove-portal":
		choices, err = c.admin.RemovePortalAutocomplete(ctx, guildID, current)
	}
	if err != nil {
		slog.Warn("Autocomplete failed", "command", data.Name, "option", name, "error", err)
		choices = []Choice{}
	}
	response := &discordgo.InteractionResponse{
		Type: discordgo.InteractionApplicationCommandAutocompleteResult,
		Data: &discordgo.InteractionResponseData{Choices: autocompleteChoices(choices)},
	}
	if err := c.sender.Respond(ctx, interaction, response); err != nil {
		slog.Warn("Could not answer autocomplete", "command", data.Name, "error", err)
	}
}

func focusedOption(data discordgo.ApplicationCommandInteractionData) (name, value string) {
	for _, option := range data.Options {
		if option == nil {
			continue
		}
		if option.Focused {
			return option.Name, optionStringValue(option)
		}
	}
	if len(data.Options) > 0 && data.Options[0] != nil {
		return data.Options[0].Name, optionStringValue(data.Options[0])
	}
	return "", ""
}

func optionStringValue(option *discordgo.ApplicationCommandInteractionDataOption) string {
	if option == nil {
		return ""
	}
	value, _ := option.Value.(string)
	return value
}

func optionString(data discordgo.ApplicationCommandInteractionData, name string) string {
	return optionStringValue(data.GetOption(name))
}

func optionFloat(data discordgo.ApplicationCommandInteractionData, name string) float64 {
	option := data.GetOption(name)
	if option == nil {
		return 0
	}
	value, _ := option.Value.(float64)
	return value
}

func optionInt(data discordgo.ApplicationCommandInteractionData, name string) int64 {
	option := data.GetOption(name)
	if option == nil {
		return 0
	}
	value, _ := option.Value.(float64)
	return int64(value)
}

func (c *Client) dispatch(
	ctx context.Context,
	guildID int64,
	data discordgo.ApplicationCommandInteractionData,
) (Response, error) {
	switch data.Name {
	case "set-channel":
		option := data.GetOption("channel")
		if option == nil {
			return Response{}, errors.New("missing channel option")
		}
		channelID := optionStringValue(option)
		id := parseSnowflake(channelID)
		if id == 0 {
			return Response{}, errors.New("unresolved channel option")
		}
		return c.admin.SetChannel(ctx, guildID, ChannelRef{ID: id, Mention: fmt.Sprintf("<#%d>", id)})
	case "set-location":
		return c.admin.SetLocation(ctx, guildID, optionString(data, "location"))
	case "add-location":
		return c.admin.AddLocation(ctx, guildID, optionString(data, "location"))
	case "remove-location":
		return c.admin.RemoveLocation(ctx, guildID, optionString(data, "location"))
	case "set-max-age":
		return c.admin.SetMaxAge(ctx, guildID, optionFloat(data, "hours"))
	case "add-keyword":
		return c.admin.AddKeyword(ctx, guildID, optionString(data, "keyword"))
	case "remove-keyword":
		return c.admin.RemoveKeyword(ctx, guildID, optionString(data, "keyword"))
	case "add-blacklist-company":
		return c.admin.AddBlacklistCompany(ctx, guildID, optionString(data, "company"))
	case "remove-blacklist-company":
		return c.admin.RemoveBlacklistCompany(ctx, guildID, optionString(data, "company"))
	case "add-blacklist-keyword":
		includeAliases := optionInt(data, "incluir_alias") == 1
		return c.admin.AddBlacklistKeyword(ctx, guildID, optionString(data, "keyword"), includeAliases)
	case "remove-blacklist-keyword":
		return c.admin.RemoveBlacklistKeyword(ctx, guildID, optionString(data, "keyword"))
	case "help":
		return c.admin.Help(), nil
	case "add-language":
		return c.admin.AddLanguage(ctx, guildID, optionString(data, "language"))
	case "remove-language":
		return c.admin.RemoveLanguage(ctx, guildID, optionString(data, "language"))
	case "add-portal":
		slug := optionString(data, "portal")
		return c.admin.AddPortal(ctx, guildID, slug, c.admin.portalLabel(slug))
	case "remove-portal":
		return c.admin.RemovePortal(ctx, guildID, optionString(data, "portal"))
	case "list-languages":
		return c.admin.ListLanguages(ctx, guildID)
	case "list-portals":
		return c.admin.ListPortals(ctx, guildID)
	case "list-locations":
		return c.admin.ListLocations(ctx, guildID)
	case "list-keywords":
		return c.admin.ListKeywords(ctx, guildID)
	case "status":
		return c.admin.Status(ctx, guildID)
	default:
		return Response{}, fmt.Errorf("unknown command %q", data.Name)
	}
}

func (c *Client) respond(ctx context.Context, interaction *discordgo.Interaction, response Response) {
	c.mu.Lock()
	done := c.responded[interaction.ID]
	c.responded[interaction.ID] = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.responded, interaction.ID)
		c.mu.Unlock()
	}()
	if done {
		if err := c.sender.Followup(ctx, interaction, response.Content); err != nil {
			slog.Warn("Could not send followup", "interaction_id", interaction.ID, "error", err)
		}
		return
	}
	if err := c.sender.Respond(ctx, interaction, commandResponse(response)); err != nil {
		slog.Warn("Could not answer interaction", "interaction_id", interaction.ID, "error", err)
	}
}

func commandResponse(response Response) *discordgo.InteractionResponse {
	data := &discordgo.InteractionResponseData{
		Content: response.Content,
	}
	if response.Ephemeral {
		data.Flags = discordgo.MessageFlagsEphemeral
	}
	if response.Embed != nil {
		data.Embeds = []*discordgo.MessageEmbed{toDiscordEmbed(response.Embed)}
	}
	return &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: data,
	}
}

func autocompleteChoices(choices []Choice) []*discordgo.ApplicationCommandOptionChoice {
	result := make([]*discordgo.ApplicationCommandOptionChoice, 0, len(choices))
	for _, choice := range choices {
		result = append(result, &discordgo.ApplicationCommandOptionChoice{
			Name:  choice.Name,
			Value: choice.Value,
		})
	}
	return result
}

func toDiscordEmbed(embed *Embed) *discordgo.MessageEmbed {
	fields := make([]*discordgo.MessageEmbedField, 0, len(embed.Fields))
	for _, field := range embed.Fields {
		fields = append(fields, &discordgo.MessageEmbedField{
			Name:   field.Name,
			Value:  field.Value,
			Inline: field.Inline,
		})
	}
	return &discordgo.MessageEmbed{
		Title:       embed.Title,
		Description: embed.Description,
		Color:       embed.Color,
		Fields:      fields,
	}
}

type discordCommandRegistrar struct {
	session *discordgo.Session
}

func (r *discordCommandRegistrar) RegisterCommands(
	ctx context.Context,
	appID, guildID string,
	commands []*discordgo.ApplicationCommand,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := r.session.ApplicationCommandBulkOverwrite(appID, guildID, commands)
	if err != nil {
		return fmt.Errorf("bulk overwrite: %w", err)
	}
	return nil
}

type discordApplicationResolver struct {
	session *discordgo.Session
}

func (r *discordApplicationResolver) ApplicationID(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	application, err := r.session.Application("@me")
	if err != nil {
		return "", fmt.Errorf("fetch application: %w", err)
	}
	if application == nil || application.ID == "" {
		return "", errors.New("application id is empty")
	}
	return application.ID, nil
}

type discordInteractionSender struct {
	session *discordgo.Session
}

func (s *discordInteractionSender) Respond(
	ctx context.Context,
	interaction *discordgo.Interaction,
	response *discordgo.InteractionResponse,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.session.InteractionRespond(interaction, response); err != nil {
		return fmt.Errorf("interaction respond: %w", err)
	}
	return nil
}

func (s *discordInteractionSender) Followup(ctx context.Context, interaction *discordgo.Interaction, content string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := s.session.FollowupMessageCreate(interaction, false, &discordgo.WebhookParams{
		Content: content,
		Flags:   discordgo.MessageFlagsEphemeral,
	})
	if err != nil {
		return fmt.Errorf("followup message: %w", err)
	}
	return nil
}

type discordOnboardingGateway struct {
	session *discordgo.Session
}

func (g *discordOnboardingGateway) Channel(ctx context.Context, channelID int64) (ChannelInfo, error) {
	if err := ctx.Err(); err != nil {
		return ChannelInfo{}, err
	}
	id := strconv.FormatInt(channelID, 10)
	var channel *discordgo.Channel
	if g.session.State != nil {
		channel, _ = g.session.State.Channel(id)
	}
	if channel == nil {
		fetched, err := g.session.Channel(id)
		if err != nil {
			return ChannelInfo{}, fmt.Errorf("fetch channel: %w", err)
		}
		channel = fetched
	}
	if channel == nil {
		return ChannelInfo{}, errors.New("channel is nil")
	}
	if channel.Type != discordgo.ChannelTypeGuildText {
		return ChannelInfo{ID: channelID, GuildID: parseSnowflake(channel.GuildID), Name: channel.Name}, nil
	}
	info := ChannelInfo{
		ID:      channelID,
		Name:    channel.Name,
		GuildID: parseSnowflake(channel.GuildID),
	}
	userID := g.currentUserID()
	if userID == "" {
		return info, nil
	}
	permissions, err := g.session.UserChannelPermissions(userID, channel.ID)
	if err != nil {
		return info, fmt.Errorf("read channel permissions: %w", err)
	}
	info.CanSend = permissions&discordgo.PermissionSendMessages != 0
	info.CanEmbed = permissions&discordgo.PermissionEmbedLinks != 0
	return info, nil
}

func (g *discordOnboardingGateway) User(ctx context.Context, guildID, userID int64) (UserInfo, error) {
	if err := ctx.Err(); err != nil {
		return UserInfo{}, err
	}
	if g.session.State != nil {
		member, err := g.session.State.Member(
			strconv.FormatInt(guildID, 10),
			strconv.FormatInt(userID, 10),
		)
		if err == nil && member != nil && member.User != nil {
			return UserInfo{ID: parseSnowflake(member.User.ID), Available: true}, nil
		}
	}
	user, err := g.session.User(strconv.FormatInt(userID, 10))
	if err != nil {
		return UserInfo{}, fmt.Errorf("fetch user: %w", err)
	}
	if user == nil {
		return UserInfo{}, errors.New("user is nil")
	}
	return UserInfo{ID: parseSnowflake(user.ID), Available: true}, nil
}

func (g *discordOnboardingGateway) SendChannel(ctx context.Context, channelID int64, embed *Embed) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := g.session.ChannelMessageSendComplex(strconv.FormatInt(channelID, 10), &discordgo.MessageSend{
		Embeds: []*discordgo.MessageEmbed{toDiscordEmbed(embed)},
	})
	if err != nil {
		return fmt.Errorf("send channel message: %w", err)
	}
	return nil
}

func (g *discordOnboardingGateway) SendUser(ctx context.Context, userID int64, embed *Embed) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	channel, err := g.session.UserChannelCreate(strconv.FormatInt(userID, 10))
	if err != nil {
		return fmt.Errorf("create DM channel: %w", err)
	}
	_, err = g.session.ChannelMessageSendComplex(channel.ID, &discordgo.MessageSend{
		Embeds: []*discordgo.MessageEmbed{toDiscordEmbed(embed)},
	})
	if err != nil {
		return fmt.Errorf("send direct message: %w", err)
	}
	return nil
}

func (g *discordOnboardingGateway) currentUserID() string {
	if g.session.State != nil && g.session.State.User != nil {
		return g.session.State.User.ID
	}
	user, err := g.session.User("@me")
	if err != nil || user == nil {
		return ""
	}
	return user.ID
}
