package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NikoMalek/job-sniper-discord-bot/internal/languages"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/locations"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/scrapers"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/store"
	"github.com/bwmarrin/discordgo"
)

const (
	jobTrackPrefix       = "job_track:"
	jobModalPrefix       = "job_modal:"
	jobApplyChoiceID     = "job_apply_choice"
	jobNotesID           = "job_notes"
	experimentalPageSize = 5
	experimentalColor    = 0x9B59B6
	experimentalViewTTL  = 180 * time.Second
	experimentalModalTTL = 300 * time.Second
)

var (
	rankMedals         = []string{"🥇", "🥈", "🥉"}
	experimentalAction = map[string]bool{"activar": true, "desactivar": true}

	markdownURLPattern = regexp.MustCompile("(?m)(?P<url><[^: >]+:/[^ >]+>|(?:https?|steam)://[^\\s<]+[^<.,:;\"'\\]\\s])|(?P<markdown>[_~|*`]|^>(?:>>)?\\s|\\[.+\\]\\(.+\\)|^#{1,3}|^\\s*-)")
	timestampLayouts   = []string{
		time.RFC3339,
		"2006-01-02T15:04:05.999999999Z07:00",
		"2006-01-02T15:04:05Z07:00",
		"2006-01-02 15:04:05Z07:00",
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
	}
)

// ExperimentalStore is the persistence surface used by the experimental zone.
// It is deliberately narrow so tests can supply an in-memory fake.
type ExperimentalStore interface {
	GetGuildConfig(ctx context.Context, guildID int64) (store.GuildMonitorConfig, error)
	IsExperimentalEnabled(ctx context.Context, guildID int64) (bool, error)
	SetExperimentalEnabled(ctx context.Context, guildID int64, enabled bool) error
	GetExperimentalEnabledGuildIDs(ctx context.Context) ([]int64, error)
	GetTrackedJob(ctx context.Context, token string) (*store.TrackedJob, error)
	RecordJobClick(
		ctx context.Context, guildID, userID int64,
		jobURL, jobTitle, company, source string,
	) (int64, error)
	GetJobInteraction(ctx context.Context, recordID int64) (*store.JobInteraction, error)
	UpdateApplicationStatus(
		ctx context.Context, recordID int64, applied *bool, notes *string,
	) (bool, error)
	GetGuildSnipersRanking(ctx context.Context, guildID int64, limit int) ([]store.SniperRanking, error)
	GetGuildSnipersTotals(ctx context.Context, guildID int64) (int64, int64, error)
	GetUserApplicationSummary(ctx context.Context, guildID, userID int64) (store.ApplicationSummary, error)
	GetUserApplications(
		ctx context.Context, guildID, userID int64, limit, offset int,
	) ([]store.ApplicationRecord, error)
	GetDebugReportCount(ctx context.Context, guildID int64, source *string) (int64, error)
	GetDebugReports(
		ctx context.Context, guildID int64, source *string, limit, offset int,
	) ([]store.DebugReport, error)
}

// CommandResult is a Discord-facing interaction outcome. At most one of Respond,
// Edit and Defer is normally used; After runs once after the initial response.
type CommandResult struct {
	Respond  *discordgo.InteractionResponse
	Edit     *discordgo.WebhookEdit
	Defer    bool
	Followup string
	After    func(context.Context) error
}

// ExperimentalOptions configures the experimental-zone handler.
type ExperimentalOptions struct {
	Store              ExperimentalStore
	Registry           PortalRegistry
	ParseLocationOrNil func(string) (*locations.LocationIntent, error)
	Messages           InteractionSender
	Sync               func(context.Context, int64, bool) error
	Now                func() time.Time
	AfterFunc          func(time.Duration, func())
}

type viewSession struct {
	ChannelID  string
	MessageID  string
	Kind       string
	GuildID    int64
	Language   string
	ViewerID   int64
	TargetID   int64
	TargetName string
	TargetTag  string
	Source     *string
	Summary    store.ApplicationSummary
	Total      int64
	Page       int
	Pages      int
	Expires    time.Time
}

type modalSession struct {
	GuildID  int64
	UserID   int64
	RecordID int64
	Language string
	Token    string
	Expires  time.Time
}

// Experimental implements the experimental commands, tracking button, modal
// and paginators without creating Discord requests by itself.
type Experimental struct {
	store     ExperimentalStore
	registry  PortalRegistry
	parse     func(string) (*locations.LocationIntent, error)
	messages  InteractionSender
	sync      func(context.Context, int64, bool) error
	now       func() time.Time
	after     func(time.Duration, func())
	mu        sync.Mutex
	views     map[string]*viewSession
	modalData map[string]*modalSession
}

// NewExperimental builds the experimental handler with catalog-backed defaults.
func NewExperimental(options ExperimentalOptions) (*Experimental, error) {
	if options.Store == nil {
		return nil, errors.New("experimental commands require a store")
	}
	parse := options.ParseLocationOrNil
	if parse == nil {
		parse = locations.ParseOrNone
	}
	registry := options.Registry
	if registry == nil {
		registry = scrapers.DefaultRegistry()
	}
	now := options.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	after := options.AfterFunc
	if after == nil {
		after = func(delay time.Duration, run func()) {
			time.AfterFunc(delay, run)
		}
	}
	return &Experimental{
		store:     options.Store,
		registry:  registry,
		parse:     parse,
		messages:  options.Messages,
		sync:      options.Sync,
		now:       now,
		after:     after,
		views:     make(map[string]*viewSession),
		modalData: make(map[string]*modalSession),
	}, nil
}

// GlobalCommandDefinitions returns the always-registered experimental command.
func GlobalCommandDefinitions() []*discordgo.ApplicationCommand {
	option := stringOption(
		"accion", "Elige solo la acción disponible según el estado actual.", false,
	)
	option.Autocomplete = true
	return []*discordgo.ApplicationCommand{adminCommand(
		"experimental",
		"Muestra información y estado; usa accion para activar o desactivar la Zona Experimental.",
		option,
	)}
}

// ExperimentalGuildCommandDefinitions returns the commands synced only while
// experimental mode is enabled in a guild.
func ExperimentalGuildCommandDefinitions() []*discordgo.ApplicationCommand {
	source := stringOption(
		"source", "Filtra por un portal activo en este servidor.", false,
	)
	source.Autocomplete = true
	return []*discordgo.ApplicationCommand{
		adminCommand("snipers", "Muestra el ranking de postulaciones de este servidor."),
		adminCommand(
			"applications", "Muestra tus postulaciones o las de un usuario mencionado.",
			&discordgo.ApplicationCommandOption{
				Type:        discordgo.ApplicationCommandOptionUser,
				Name:        "user",
				Description: "Usuario del que quieres ver el historial (omítelo para ver el tuyo).",
				Required:    false,
			},
		),
		adminCommand("debug", "Muestra las observaciones dejadas al registrar postulaciones.", source),
	}
}

func isExperimentalCommand(name string) bool {
	switch name {
	case "experimental", "snipers", "applications", "debug":
		return true
	default:
		return false
	}
}

// Command handles one experimental slash command.
func (e *Experimental) Command(
	ctx context.Context, interaction *discordgo.Interaction,
) (*CommandResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data := interaction.ApplicationCommandData()
	guildID := parseSnowflake(interaction.GuildID)
	language, err := e.language(ctx, guildID)
	if err != nil {
		return nil, err
	}
	switch data.Name {
	case "experimental":
		return e.experimentalStatus(ctx, interaction, guildID, language, optionString(data, "accion"))
	case "snipers":
		return e.snipers(ctx, guildID, language)
	case "applications":
		return e.applications(ctx, interaction, guildID, language, data)
	case "debug":
		return e.debug(ctx, interaction, guildID, language, optionString(data, "source"))
	default:
		return nil, fmt.Errorf("unknown experimental command %q", data.Name)
	}
}

// Autocomplete returns choices for the experimental command options.
func (e *Experimental) Autocomplete(
	ctx context.Context, interaction *discordgo.Interaction,
) []Choice {
	data := interaction.ApplicationCommandData()
	_, current := focusedOption(data)
	switch data.Name {
	case "experimental":
		guildID := parseSnowflake(interaction.GuildID)
		enabled, err := e.store.IsExperimentalEnabled(ctx, guildID)
		if err != nil {
			slog.Warn("Could not read experimental mode for autocomplete", "error", err)
			return []Choice{}
		}
		available := []string{"activar"}
		if enabled {
			available = []string{"desactivar"}
		}
		needle := strings.ToLower(strings.TrimSpace(current))
		choices := make([]Choice, 0, 1)
		for _, action := range available {
			if strings.Contains(action, needle) {
				choices = append(choices, Choice{Name: titleLabel(action), Value: action})
			}
		}
		return choices
	case "debug":
		guildID := parseSnowflake(interaction.GuildID)
		config, err := e.store.GetGuildConfig(ctx, guildID)
		if err != nil {
			slog.Warn("Could not read guild config for debug autocomplete", "error", err)
			return []Choice{}
		}
		needle := strings.ToLower(strings.TrimSpace(current))
		choices := make([]Choice, 0)
		for _, source := range e.activePortalSources(config) {
			if strings.Contains(strings.ToLower(source), needle) {
				choices = append(choices, Choice{Name: source, Value: source})
			}
		}
		if len(choices) > 25 {
			choices = choices[:25]
		}
		return choices
	default:
		return []Choice{}
	}
}

// ErrorResult maps an experimental command error to its localized user text.
func (e *Experimental) ErrorResult(
	ctx context.Context, guildID int64, err error,
) *CommandResult {
	language, languageErr := e.language(ctx, guildID)
	if languageErr != nil {
		language = "spanish"
	}
	key := "command_error"
	if errors.Is(err, ErrPermissionDenied) {
		key = "no_permission"
	}
	if err != nil && !errors.Is(err, ErrPermissionDenied) {
		slog.Error("Experimental command failed", "guild_id", guildID, "error", err)
	}
	return contentResult(trackingText(key, language, nil), true)
}

// HandleComponent handles the tracking button and the two paginator views.
func (e *Experimental) HandleComponent(
	ctx context.Context, interaction *discordgo.Interaction,
) (*CommandResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data := interaction.MessageComponentData()
	customID := data.CustomID
	switch {
	case strings.HasPrefix(customID, jobTrackPrefix):
		return e.trackingButton(ctx, interaction, strings.TrimPrefix(customID, jobTrackPrefix))
	case customID == "applications_previous", customID == "applications_next",
		customID == "debug_previous", customID == "debug_next":
		return e.paginatorButton(ctx, interaction, customID)
	default:
		return &CommandResult{}, nil
	}
}

// HandleModalSubmit handles the private application modal.
func (e *Experimental) HandleModalSubmit(
	ctx context.Context, interaction *discordgo.Interaction,
) (*CommandResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data := interaction.ModalSubmitData()
	session := e.getModal(data.CustomID)
	if session == nil {
		return contentResult(trackingText("expired", "spanish", nil), true), nil
	}
	guildID := parseSnowflake(interaction.GuildID)
	userID := interactionUserID(interaction)
	if session.GuildID != guildID || session.UserID != userID {
		return contentResult(trackingText("not_allowed", session.Language, nil), true), nil
	}
	enabled, err := e.store.IsExperimentalEnabled(ctx, guildID)
	if err != nil {
		return nil, fmt.Errorf("read experimental mode: %w", err)
	}
	if !enabled {
		return contentResult(trackingText("disabled", session.Language, nil), true), nil
	}
	applied := findSelectValue(data.Components, jobApplyChoiceID) == "yes"
	notesValue := strings.TrimSpace(findTextValue(data.Components, jobNotesID))
	var notes *string
	if notesValue != "" {
		notes = &notesValue
	}
	if _, err := e.store.UpdateApplicationStatus(ctx, session.RecordID, &applied, notes); err != nil {
		return nil, fmt.Errorf("update application status: %w", err)
	}
	e.deleteModal(data.CustomID)
	return &CommandResult{Defer: true}, nil
}

func (e *Experimental) language(ctx context.Context, guildID int64) (string, error) {
	if guildID == 0 {
		return "spanish", nil
	}
	config, err := e.store.GetGuildConfig(ctx, guildID)
	if err != nil {
		return "spanish", fmt.Errorf("read guild language: %w", err)
	}
	return languages.PreferredUILanguage(config.Languages), nil
}

func (e *Experimental) experimentalStatus(
	ctx context.Context,
	interaction *discordgo.Interaction,
	guildID int64,
	language, action string,
) (*CommandResult, error) {
	enabled, err := e.store.IsExperimentalEnabled(ctx, guildID)
	if err != nil {
		return nil, fmt.Errorf("read experimental mode: %w", err)
	}
	if strings.TrimSpace(action) == "" {
		return messageResult(e.statusEmbed(enabled, language), true, nil), nil
	}
	value := strings.ToLower(strings.TrimSpace(action))
	if !experimentalAction[value] {
		return contentResult(trackingText("command_error", language, nil), true), nil
	}
	target := value == "activar"
	embed := e.statusEmbed(target, language)
	if target == enabled {
		return messageResult(embed, true, nil), nil
	}
	if err := e.store.SetExperimentalEnabled(ctx, guildID, target); err != nil {
		return nil, fmt.Errorf("set experimental mode: %w", err)
	}
	result := messageResult(embed, true, nil)
	if e.sync == nil {
		return result, nil
	}
	result.After = func(ctx context.Context) error {
		if err := e.sync(ctx, guildID, target); err != nil {
			slog.Error("Could not sync experimental commands", "guild_id", guildID, "error", err)
			if e.messages == nil {
				return err
			}
			failed := *embed
			failed.Footer = &discordgo.MessageEmbedFooter{
				Text: trackingText("sync_fallback", language, nil),
			}
			embeds := []*discordgo.MessageEmbed{&failed}
			return e.messages.EditOriginal(ctx, interaction, &discordgo.WebhookEdit{Embeds: &embeds})
		}
		slog.Info("Synchronized experimental commands", "guild_id", guildID, "enabled", target)
		return nil
	}
	return result, nil
}

func (e *Experimental) statusEmbed(enabled bool, language string) *discordgo.MessageEmbed {
	stateKey := "experimental_disabled"
	if enabled {
		stateKey = "experimental_enabled"
	}
	return &discordgo.MessageEmbed{
		Title:       trackingText("experimental_title", language, nil),
		Description: trackingText("experimental_desc", language, nil),
		Color:       experimentalColor,
		Fields: []*discordgo.MessageEmbedField{{
			Name:   trackingText("status_label", language, nil),
			Value:  "**" + trackingText(stateKey, language, nil) + "**",
			Inline: false,
		}},
	}
}

func (e *Experimental) snipers(
	ctx context.Context, guildID int64, language string,
) (*CommandResult, error) {
	rows, err := e.store.GetGuildSnipersRanking(ctx, guildID, 10)
	if err != nil {
		return nil, fmt.Errorf("read snipers ranking: %w", err)
	}
	applied, viewed, err := e.store.GetGuildSnipersTotals(ctx, guildID)
	if err != nil {
		return nil, fmt.Errorf("read snipers totals: %w", err)
	}
	summary := trackingText("snipers_summary", language, map[string]string{
		"total_applied": formatInt(applied),
		"total_views":   formatInt(viewed),
	})
	embed := &discordgo.MessageEmbed{
		Title:       trackingText("snipers_title", language, nil),
		Description: summary + "\n\n" + rankingLines(rows, language),
		Color:       experimentalColor,
	}
	return messageResult(embed, false, nil), nil
}

func (e *Experimental) applications(
	ctx context.Context,
	interaction *discordgo.Interaction,
	guildID int64,
	language string,
	data discordgo.ApplicationCommandInteractionData,
) (*CommandResult, error) {
	targetID, targetName, targetTag := resolvedUser(interaction, data)
	summary, err := e.store.GetUserApplicationSummary(ctx, guildID, targetID)
	if err != nil {
		return nil, fmt.Errorf("read application summary: %w", err)
	}
	session := &viewSession{
		Kind:       "applications",
		GuildID:    guildID,
		Language:   language,
		ViewerID:   interactionUserID(interaction),
		TargetID:   targetID,
		TargetName: targetName,
		TargetTag:  targetTag,
		Summary:    summary,
		Pages:      pageCount(summary.Clicked),
	}
	embed, err := session.buildEmbed(ctx, e.store)
	if err != nil {
		return nil, err
	}
	var components []discordgo.MessageComponent
	result := messageResult(embed, false, nil)
	if session.Pages > 1 {
		components = paginationComponents(language, session.Kind, session.Page, session.Pages)
		result = messageResult(embed, false, components)
		result.After = e.registerViewAfter(interaction, session)
	}
	return result, nil
}

func (e *Experimental) debug(
	ctx context.Context,
	interaction *discordgo.Interaction,
	guildID int64,
	language, rawSource string,
) (*CommandResult, error) {
	cleaned := truncateText(rawSource, "", 50)
	var source *string
	if cleaned != "" {
		source = &cleaned
	}
	total, err := e.store.GetDebugReportCount(ctx, guildID, source)
	if err != nil {
		return nil, fmt.Errorf("count debug reports: %w", err)
	}
	session := &viewSession{
		Kind:     "debug",
		GuildID:  guildID,
		Language: language,
		Source:   source,
		Total:    total,
		Pages:    pageCount(total),
	}
	embed, err := session.buildEmbed(ctx, e.store)
	if err != nil {
		return nil, err
	}
	result := messageResult(embed, true, nil)
	if session.Pages > 1 {
		components := paginationComponents(language, session.Kind, session.Page, session.Pages)
		result = messageResult(embed, true, components)
		result.After = e.registerViewAfter(interaction, session)
	}
	return result, nil
}

func (e *Experimental) registerViewAfter(
	interaction *discordgo.Interaction, session *viewSession,
) func(context.Context) error {
	return func(ctx context.Context) error {
		if e.messages == nil {
			return nil
		}
		message, err := e.messages.OriginalResponse(ctx, interaction)
		if err != nil {
			slog.Warn("Could not register paginator", "error", err)
			return nil
		}
		if message == nil || message.ID == "" {
			return nil
		}
		session.ChannelID = interaction.ChannelID
		session.MessageID = message.ID
		e.putView(message.ID, session)
		return nil
	}
}

func (e *Experimental) trackingButton(
	ctx context.Context, interaction *discordgo.Interaction, token string,
) (*CommandResult, error) {
	guildID := parseSnowflake(interaction.GuildID)
	if guildID == 0 {
		return &CommandResult{}, nil
	}
	language, err := e.language(ctx, guildID)
	if err != nil {
		return nil, err
	}
	tracked, err := e.store.GetTrackedJob(ctx, strings.TrimSpace(token))
	if err != nil {
		return nil, fmt.Errorf("read tracked job: %w", err)
	}
	if tracked == nil || tracked.GuildID != guildID {
		return contentResult(trackingText("expired", language, nil), true), nil
	}
	enabled, err := e.store.IsExperimentalEnabled(ctx, guildID)
	if err != nil {
		return nil, fmt.Errorf("read experimental mode: %w", err)
	}
	if !enabled {
		message := trackingText("disabled", language, nil) + "\n\n" +
			"[" + trackingText("open_link", language, nil) + "](" + tracked.JobURL + ")"
		return contentResult(message, true), nil
	}
	userID := interactionUserID(interaction)
	recordID, err := e.store.RecordJobClick(
		ctx, guildID, userID, tracked.JobURL, tracked.JobTitle, tracked.Company, tracked.Source,
	)
	if err != nil {
		return nil, fmt.Errorf("record job click: %w", err)
	}
	customID := jobModalPrefix + tracked.Token + ":" + strconv.FormatInt(userID, 10)
	e.putModal(customID, &modalSession{
		GuildID:  guildID,
		UserID:   userID,
		RecordID: recordID,
		Language: language,
		Token:    tracked.Token,
	})
	data := &discordgo.InteractionResponseData{
		CustomID:   customID,
		Title:      truncateText(trackingText("modal_title", language, nil), "", 45),
		Components: modalComponents(language, tracked),
	}
	return &CommandResult{Respond: &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseModal,
		Data: data,
	}}, nil
}

func (e *Experimental) paginatorButton(
	ctx context.Context, interaction *discordgo.Interaction, customID string,
) (*CommandResult, error) {
	messageID := ""
	if interaction.Message != nil {
		messageID = interaction.Message.ID
	}
	session := e.getView(messageID)
	if session == nil || parseSnowflake(interaction.GuildID) != session.GuildID {
		return contentResult(trackingText("expired", "spanish", nil), true), nil
	}
	if (customID == "applications_previous" || customID == "applications_next") && session.Kind != "applications" {
		return contentResult(trackingText("expired", session.Language, nil), true), nil
	}
	if (customID == "debug_previous" || customID == "debug_next") && session.Kind != "debug" {
		return contentResult(trackingText("expired", session.Language, nil), true), nil
	}
	switch customID {
	case "applications_previous", "debug_previous":
		session.Page = max(0, session.Page-1)
	case "applications_next", "debug_next":
		session.Page = min(session.Pages-1, session.Page+1)
	default:
		return &CommandResult{}, nil
	}
	session.ViewerID = interactionUserID(interaction)
	embed, err := session.buildEmbed(ctx, e.store)
	if err != nil {
		return nil, err
	}
	components := paginationComponents(session.Language, session.Kind, session.Page, session.Pages)
	return &CommandResult{Edit: &discordgo.WebhookEdit{
		Embeds:     &[]*discordgo.MessageEmbed{embed},
		Components: &components,
	}}, nil
}

func (s *viewSession) buildEmbed(
	ctx context.Context, repository ExperimentalStore,
) (*discordgo.MessageEmbed, error) {
	if s.Kind == "applications" {
		return s.buildApplicationsEmbed(ctx, repository)
	}
	return s.buildDebugEmbed(ctx, repository)
}

func (s *viewSession) buildApplicationsEmbed(
	ctx context.Context, repository ExperimentalStore,
) (*discordgo.MessageEmbed, error) {
	records, err := repository.GetUserApplications(
		ctx, s.GuildID, s.TargetID, experimentalPageSize, s.Page*experimentalPageSize,
	)
	if err != nil {
		return nil, fmt.Errorf("read user applications: %w", err)
	}
	title := trackingText("applications_self_title", s.Language, nil)
	description := trackingText("applications_self_desc", s.Language, nil)
	if s.TargetID != s.ViewerID {
		title = trackingText("applications_other_title", s.Language, map[string]string{
			"user": s.TargetName,
		})
		description = trackingText("applications_other_desc", s.Language, map[string]string{
			"user": s.TargetTag,
		})
	}
	lines := make([]string, 0, len(records))
	for _, record := range records {
		lines = append(lines, applicationLine(record, s.Language))
	}
	list := strings.Join(lines, "\n")
	if list == "" {
		list = trackingText("no_data", s.Language, nil)
	}
	totals := trackingText("totals_value", s.Language, map[string]string{
		"clicked": formatInt(s.Summary.Clicked),
		"applied": formatInt(s.Summary.Applied),
		"skipped": formatInt(s.Summary.Skipped),
		"viewed":  formatInt(s.Summary.Viewed),
	})
	body := strings.Join([]string{
		description,
		"**" + trackingText("totals_label", s.Language, nil) + "**\n" + totals,
		list,
	}, "\n\n")
	return &discordgo.MessageEmbed{
		Title:       title,
		Description: body,
		Color:       experimentalColor,
		Footer:      &discordgo.MessageEmbedFooter{Text: s.pageFooter()},
	}, nil
}

func (s *viewSession) buildDebugEmbed(
	ctx context.Context, repository ExperimentalStore,
) (*discordgo.MessageEmbed, error) {
	reports, err := repository.GetDebugReports(
		ctx, s.GuildID, s.Source, experimentalPageSize, s.Page*experimentalPageSize,
	)
	if err != nil {
		return nil, fmt.Errorf("read debug reports: %w", err)
	}
	lines := make([]string, 0, len(reports))
	for _, report := range reports {
		title := escapeMarkdown(truncateText(report.JobTitle,
			trackingText("untitled", s.Language, nil), 90))
		source := escapeMarkdown(truncateText(report.Source, "—", 30))
		notes := escapeMarkdown(truncateText(report.Notes, "", 250))
		lines = append(lines, fmt.Sprintf(
			"`%s` · <@%d> · %s · [%s](%s) · %s\n> %s",
			formatClickedDate(report.ClickedAt), report.UserID, source, title,
			report.JobURL, statusText(report.Applied, s.Language), notes,
		))
	}
	description := trackingText("debug_desc", s.Language, nil)
	if s.Source != nil && *s.Source != "" {
		description += "\n" + trackingText("debug_filter", s.Language, map[string]string{
			"source": escapeMarkdown(*s.Source),
		})
	}
	list := strings.Join(lines, "\n\n")
	if list == "" {
		list = trackingText("debug_empty", s.Language, nil)
	}
	return &discordgo.MessageEmbed{
		Title:       trackingText("debug_title", s.Language, nil),
		Description: description + "\n\n" + list,
		Color:       experimentalColor,
		Footer:      &discordgo.MessageEmbedFooter{Text: s.pageFooter()},
	}, nil
}

func (s *viewSession) pageFooter() string {
	return trackingText("page_footer", s.Language, map[string]string{
		"page":  strconv.Itoa(s.Page + 1),
		"pages": strconv.Itoa(s.Pages),
		"total": formatInt(s.TotalForCount()),
	})
}

// TotalForCount returns the counter shown in the page footer for this view.
func (s *viewSession) TotalForCount() int64 {
	if s.Kind == "applications" {
		return s.Summary.Clicked
	}
	return s.Total
}

func (e *Experimental) activePortalSources(config store.GuildMonitorConfig) []string {
	values := config.Locations
	if len(values) == 0 {
		values = []string{store.DefaultLocation}
	}
	intents := make([]*locations.LocationIntent, 0, len(values))
	for _, value := range values {
		intent, err := e.parse(value)
		if err == nil && intent != nil {
			intents = append(intents, intent)
		}
	}
	sources := make([]string, 0)
	seen := make(map[string]struct{})
	for _, slug := range config.Portals {
		scraper := e.registry.Get(slug)
		if scraper == nil {
			continue
		}
		active := false
		for _, intent := range intents {
			if scraper.Plan(intent).Supported {
				active = true
				break
			}
		}
		if !active {
			continue
		}
		source := scraper.Source()
		if source == "" {
			continue
		}
		if _, exists := seen[source]; exists {
			continue
		}
		seen[source] = struct{}{}
		sources = append(sources, source)
	}
	return sources
}

func (e *Experimental) putView(key string, session *viewSession) {
	session.Expires = e.now().Add(experimentalViewTTL)
	e.mu.Lock()
	e.views[key] = session
	e.mu.Unlock()
	e.after(experimentalViewTTL, func() {
		e.mu.Lock()
		current := e.views[key]
		expired := current == session && e.now().After(session.Expires)
		if expired {
			delete(e.views, key)
		}
		e.mu.Unlock()
		if expired {
			e.disableViewComponents(session)
		}
	})
}

func (e *Experimental) disableViewComponents(session *viewSession) {
	if e.messages == nil || session.ChannelID == "" || session.MessageID == "" {
		return
	}
	components := paginationComponents(session.Language, session.Kind, session.Page, session.Pages)
	for index, component := range components[0].(discordgo.ActionsRow).Components {
		if button, ok := component.(discordgo.Button); ok {
			button.Disabled = true
			components[0].(discordgo.ActionsRow).Components[index] = button
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := e.messages.EditMessage(
		ctx, session.ChannelID, session.MessageID, components,
	); err != nil {
		slog.Warn(
			"Could not disable expired paginator", "message_id", session.MessageID, "error", err,
		)
	}
}

func (e *Experimental) getView(key string) *viewSession {
	e.mu.Lock()
	defer e.mu.Unlock()
	session := e.views[key]
	if session == nil {
		return nil
	}
	if e.now().After(session.Expires) {
		delete(e.views, key)
		return nil
	}
	session.Expires = e.now().Add(experimentalViewTTL)
	return session
}

func (e *Experimental) putModal(key string, session *modalSession) {
	session.Expires = e.now().Add(experimentalModalTTL)
	e.mu.Lock()
	e.modalData[key] = session
	e.mu.Unlock()
	e.after(experimentalModalTTL, func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.modalData[key] == session && e.now().After(session.Expires) {
			delete(e.modalData, key)
		}
	})
}

func (e *Experimental) getModal(key string) *modalSession {
	e.mu.Lock()
	defer e.mu.Unlock()
	session := e.modalData[key]
	if session == nil {
		return nil
	}
	if e.now().After(session.Expires) {
		delete(e.modalData, key)
		return nil
	}
	return session
}

func (e *Experimental) deleteModal(key string) {
	e.mu.Lock()
	delete(e.modalData, key)
	e.mu.Unlock()
}

func modalComponents(language string, tracked *store.TrackedJob) []discordgo.MessageComponent {
	title := escapeMarkdown(truncateText(tracked.JobTitle,
		trackingText("untitled", language, nil), 120))
	company := escapeMarkdown(truncateText(tracked.Company,
		trackingText("unknown_company", language, nil), 100))
	prompt := "[" + trackingText("open_link", language, nil) + "](" + tracked.JobURL + ")\n\n" +
		trackingText("applied_question", language, map[string]string{
			"title": title, "company": company,
		})
	minValues := 1
	selectMenu := discordgo.SelectMenu{
		CustomID:    jobApplyChoiceID,
		Placeholder: trackingText("choice_placeholder", language, nil),
		MinValues:   &minValues,
		MaxValues:   1,
		Required:    boolPointer(true),
		Options: []discordgo.SelectMenuOption{
			{Label: trackingText("yes_button", language, nil), Value: "yes"},
			{Label: trackingText("no_button", language, nil), Value: "no"},
		},
	}
	notes := discordgo.TextInput{
		CustomID:    jobNotesID,
		Style:       discordgo.TextInputParagraph,
		Placeholder: trackingText("observation_placeholder", language, nil),
		Required:    boolPointer(false),
		MaxLength:   250,
	}
	return []discordgo.MessageComponent{
		discordgo.TextDisplay{Content: prompt},
		discordgo.Label{
			Label:     trackingText("choice_placeholder", language, nil),
			Component: selectMenu,
		},
		discordgo.Label{
			Label:     trackingText("observation_label", language, nil),
			Component: notes,
		},
	}
}

func paginationComponents(
	language, kind string, page, pages int,
) []discordgo.MessageComponent {
	previousID, nextID := "applications_previous", "applications_next"
	if kind == "debug" {
		previousID, nextID = "debug_previous", "debug_next"
	}
	return []discordgo.MessageComponent{discordgo.ActionsRow{
		Components: []discordgo.MessageComponent{
			discordgo.Button{
				Label:    trackingText("previous_button", language, nil),
				Style:    discordgo.SecondaryButton,
				CustomID: previousID,
				Disabled: page == 0,
			},
			discordgo.Button{
				Label:    trackingText("next_button", language, nil),
				Style:    discordgo.SecondaryButton,
				CustomID: nextID,
				Disabled: page >= pages-1,
			},
		},
	}}
}

func findSelectValue(components []discordgo.MessageComponent, customID string) string {
	value := ""
	walkComponents(components, func(component discordgo.MessageComponent) {
		if value != "" {
			return
		}
		switch typed := component.(type) {
		case *discordgo.SelectMenu:
			if typed.CustomID == customID && len(typed.Values) > 0 {
				value = typed.Values[0]
			}
		case discordgo.SelectMenu:
			if typed.CustomID == customID && len(typed.Values) > 0 {
				value = typed.Values[0]
			}
		}
	})
	return value
}

func findTextValue(components []discordgo.MessageComponent, customID string) string {
	value := ""
	walkComponents(components, func(component discordgo.MessageComponent) {
		if value != "" {
			return
		}
		switch typed := component.(type) {
		case *discordgo.TextInput:
			if typed.CustomID == customID {
				value = typed.Value
			}
		case discordgo.TextInput:
			if typed.CustomID == customID {
				value = typed.Value
			}
		}
	})
	return value
}

func walkComponents(
	components []discordgo.MessageComponent, visit func(discordgo.MessageComponent),
) {
	for _, component := range components {
		if component == nil {
			continue
		}
		visit(component)
		switch typed := component.(type) {
		case *discordgo.ActionsRow:
			walkComponents(typed.Components, visit)
		case discordgo.ActionsRow:
			walkComponents(typed.Components, visit)
		case *discordgo.Label:
			if typed.Component != nil {
				walkComponents([]discordgo.MessageComponent{typed.Component}, visit)
			}
		case discordgo.Label:
			if typed.Component != nil {
				walkComponents([]discordgo.MessageComponent{typed.Component}, visit)
			}
		}
	}
}

func messageResult(
	embed *discordgo.MessageEmbed, ephemeral bool, components []discordgo.MessageComponent,
) *CommandResult {
	data := &discordgo.InteractionResponseData{
		Embeds:     []*discordgo.MessageEmbed{embed},
		Components: components,
	}
	if ephemeral {
		data.Flags = discordgo.MessageFlagsEphemeral
	}
	return &CommandResult{Respond: &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: data,
	}}
}

func contentResult(content string, ephemeral bool) *CommandResult {
	data := &discordgo.InteractionResponseData{Content: content}
	if ephemeral {
		data.Flags = discordgo.MessageFlagsEphemeral
	}
	return &CommandResult{Respond: &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: data,
	}}
}

func trackingText(key, language string, values map[string]string) string {
	return languages.TrackingText(key, language, values)
}

func formatInt(value int64) string { return strconv.FormatInt(value, 10) }

func pageCount(total int64) int {
	return int(math.Max(1, math.Ceil(float64(total)/float64(experimentalPageSize))))
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func truncateText(value, fallback string, limit int) string {
	cleaned := strings.Join(strings.Fields(value), " ")
	if cleaned == "" {
		cleaned = fallback
	}
	runes := []rune(cleaned)
	if len(runes) > limit {
		return string(runes[:limit])
	}
	return cleaned
}

func rankingLines(rows []store.SniperRanking, language string) string {
	if len(rows) == 0 {
		return trackingText("no_data", language, nil)
	}
	lines := make([]string, 0, len(rows))
	for index, row := range rows {
		rank := fmt.Sprintf("**`%d.`**", index+1)
		if index < len(rankMedals) {
			rank = rankMedals[index]
		}
		applicationsKey := "snipers_applications_many"
		if row.Applications == 1 {
			applicationsKey = "snipers_applications_one"
		}
		viewsKey := "snipers_views_many"
		if row.Viewed == 1 {
			viewsKey = "snipers_views_one"
		}
		applications := trackingText(applicationsKey, language, map[string]string{
			"count": formatInt(row.Applications),
		})
		views := trackingText(viewsKey, language, map[string]string{
			"count": formatInt(row.Viewed),
		})
		lines = append(lines, fmt.Sprintf(
			"%s <@%d>\n-# → %s · %s", rank, row.UserID, applications, views,
		))
	}
	return strings.Join(lines, "\n\n")
}

func applicationLine(record store.ApplicationRecord, language string) string {
	title := escapeMarkdown(truncateText(record.JobTitle,
		trackingText("untitled", language, nil), 90))
	company := escapeMarkdown(truncateText(record.Company,
		trackingText("unknown_company", language, nil), 60))
	portal := escapeMarkdown(truncateText(record.Source, "—", 30))
	return fmt.Sprintf(
		"%s [%s](%s) · %s · %s · %s",
		formatClickedAt(record.ClickedAt), title, record.JobURL, company, portal,
		statusText(record.Applied, language),
	)
}

func statusText(applied *bool, language string) string {
	key := "status_viewed"
	if applied != nil && *applied {
		key = "status_applied"
	} else if applied != nil {
		key = "status_skipped"
	}
	return trackingText(key, language, nil)
}

func formatClickedAt(value string) string {
	parsed, ok := parseTimestamp(value)
	if !ok {
		return "—"
	}
	epoch := parsed.Unix()
	return fmt.Sprintf("<t:%d:d> · <t:%d:t>", epoch, epoch)
}

func formatClickedDate(value string) string {
	parsed, ok := parseTimestamp(value)
	if !ok {
		return truncateText(value, "", 10)
	}
	return parsed.UTC().Format("02/01/2006")
}

func parseTimestamp(value string) (time.Time, bool) {
	for _, layout := range timestampLayouts {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}

func escapeMarkdown(value string) string {
	var result strings.Builder
	last := 0
	for _, match := range markdownURLPattern.FindAllStringSubmatchIndex(value, -1) {
		result.WriteString(value[last:match[0]])
		if match[2] >= 0 {
			result.WriteString(value[match[0]:match[1]])
		} else {
			result.WriteByte('\\')
			result.WriteString(value[match[0]:match[1]])
		}
		last = match[1]
	}
	result.WriteString(value[last:])
	return result.String()
}

func resolvedUser(
	interaction *discordgo.Interaction, data discordgo.ApplicationCommandInteractionData,
) (id int64, name, mention string) {
	option := data.GetOption("user")
	if option != nil {
		raw, _ := option.Value.(string)
		id = parseSnowflake(raw)
		if data.Resolved != nil {
			if member := data.Resolved.Members[raw]; member != nil && member.User != nil {
				name = displayMemberName(member)
			}
			if user := data.Resolved.Users[raw]; user != nil && name == "" {
				name = displayUserName(user)
			}
		}
	}
	if id == 0 {
		id = interactionUserID(interaction)
	}
	if name == "" {
		name = strconv.FormatInt(id, 10)
	}
	return id, name, "<@" + strconv.FormatInt(id, 10) + ">"
}

func displayUserName(user *discordgo.User) string {
	if user == nil {
		return ""
	}
	if user.GlobalName != "" {
		return user.GlobalName
	}
	return user.Username
}

func displayMemberName(member *discordgo.Member) string {
	if member == nil {
		return ""
	}
	if member.Nick != "" {
		return member.Nick
	}
	return displayUserName(member.User)
}

func interactionUserID(interaction *discordgo.Interaction) int64 {
	if interaction == nil {
		return 0
	}
	if interaction.Member != nil && interaction.Member.User != nil {
		return parseSnowflake(interaction.Member.User.ID)
	}
	if interaction.User != nil {
		return parseSnowflake(interaction.User.ID)
	}
	return 0
}
