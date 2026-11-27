package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/NikoMalek/job-sniper-discord-bot/internal/config"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/data"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/languages"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/locations"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/matching"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/scrapers"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/store"
)

// PAUSE_HELP is the exact coverage explanation appended to portal responses.
const PAUSE_HELP = "**Cobertura y pausa**\n" +
	"Cada portal tiene su propia cobertura. Si **todas** tus ubicaciones " +
	"quedan fuera de la cobertura de un portal, ese portal queda " +
	"**pausado (⏸️)** y se reactiva automáticamente cuando vuelvas a una " +
	"ubicación compatible."

// ErrPermissionDenied is returned when an interaction lacks manage_guild.
var ErrPermissionDenied = errors.New("permission denied")

// Response is the Discord-independent result of one command.
type Response struct {
	Content   string
	Embed     *Embed
	Ephemeral bool
}

// Choice is one string autocomplete result.
type Choice struct {
	Name  string
	Value string
}

// ChannelRef identifies the channel selected by /set-channel.
type ChannelRef struct {
	ID      int64
	Mention string
}

func (c ChannelRef) mention() string {
	if c.Mention != "" {
		return c.Mention
	}
	return fmt.Sprintf("<#%d>", c.ID)
}

// Store is the persistence surface used by onboarding and admin commands.
type Store interface {
	HasSetupMessage(ctx context.Context, guildID int64) (bool, error)
	MarkSetupMessageSent(ctx context.Context, guildID int64) error
	SetChannel(ctx context.Context, guildID, channelID int64) error
	SetLocation(ctx context.Context, guildID int64, location string) error
	GetLocations(ctx context.Context, guildID int64) ([]string, error)
	AddLocation(ctx context.Context, guildID int64, location string) (bool, error)
	RemoveLocation(ctx context.Context, guildID int64, location string) (bool, error)
	SetMaxAgeHours(ctx context.Context, guildID int64, hours float64) error
	AddKeyword(ctx context.Context, guildID int64, keyword string) (bool, error)
	RemoveKeyword(ctx context.Context, guildID int64, keyword string) (bool, error)
	AddBlacklistedCompany(ctx context.Context, guildID int64, company string) (bool, error)
	RemoveBlacklistedCompany(ctx context.Context, guildID int64, company string) (bool, error)
	AddBlacklistedKeyword(ctx context.Context, guildID int64, keyword string) (bool, error)
	RemoveBlacklistedKeyword(ctx context.Context, guildID int64, keyword string) (bool, error)
	AddLanguage(ctx context.Context, guildID int64, language string) (bool, error)
	RemoveLanguage(ctx context.Context, guildID int64, language string) (bool, error)
	AddPortal(ctx context.Context, guildID int64, source string) (bool, error)
	RemovePortal(ctx context.Context, guildID int64, source string) (bool, error)
	GetGuildConfig(ctx context.Context, guildID int64) (store.GuildMonitorConfig, error)
	CountNotified(ctx context.Context, guildID int64) (int64, error)
}

// PortalRegistry is the scraper lookup surface used by admin commands.
type PortalRegistry interface {
	Get(slug string) scrapers.Scraper
	Scrapers() map[string]scrapers.Scraper
	PortalNames(slugs []string) []string
}

// AdminOptions configures an Admin instance. Defaults come from the embedded
// catalogs and the shared scraper registry.
type AdminOptions struct {
	Store              Store
	Registry           PortalRegistry
	Catalog            *data.Catalog
	Matcher            *matching.Matcher
	Settings           config.Settings
	Latency            func() time.Duration
	ParseLocation      func(string) (locations.LocationIntent, error)
	ParseLocationOrNil func(string) (*locations.LocationIntent, error)
	Normalize          func(string) string
	CountryByCode      func(string) (data.Country, bool)
	PortalOrder        []string
	MaxLocations       int
	MinMaxAgeHours     float64
	MaxMaxAgeHours     float64
}

// Admin implements every admin command without depending on discordgo types.
type Admin struct {
	store         Store
	portals       PortalRegistry
	matcher       *matching.Matcher
	interval      float64
	latency       func() time.Duration
	parseLocation func(string) (locations.LocationIntent, error)
	parseOptional func(string) (*locations.LocationIntent, error)
	normalize     func(string) string
	countryByCode func(string) (data.Country, bool)
	portalOrder   []string
	maxLocations  int
	minAge        float64
	maxAge        float64
}

// NewAdmin builds an Admin with catalog-backed defaults.
func NewAdmin(ctx context.Context, options AdminOptions) (*Admin, error) {
	if options.Store == nil {
		return nil, errors.New("admin requires a store")
	}
	catalog := options.Catalog
	if catalog == nil {
		loaded, err := data.Load(ctx)
		if err != nil {
			return nil, fmt.Errorf("load admin catalogs: %w", err)
		}
		catalog = loaded
	}
	matcher := options.Matcher
	if matcher == nil {
		matcher = matching.NewWithCatalog(catalog, nil)
	}
	registry := options.Registry
	if registry == nil {
		registry = scrapers.DefaultRegistry()
	}
	parseLocation := options.ParseLocation
	if parseLocation == nil {
		parseLocation = locations.Parse
	}
	parseOptional := options.ParseLocationOrNil
	if parseOptional == nil {
		parseOptional = locations.ParseOrNone
	}
	normalize := options.Normalize
	if normalize == nil {
		normalize = locations.Normalize
	}
	countryByCode := options.CountryByCode
	if countryByCode == nil {
		countryByCode = catalog.CountryByCode
	}
	order := append([]string(nil), options.PortalOrder...)
	if len(order) == 0 {
		order = defaultPortalOrder()
	}
	maxLocations := options.MaxLocations
	if maxLocations == 0 {
		maxLocations = locations.MaxLocations
	}
	minAge := options.MinMaxAgeHours
	if minAge == 0 {
		minAge = store.MinMaxAgeHours
	}
	maxAge := options.MaxMaxAgeHours
	if maxAge == 0 {
		maxAge = store.MaxMaxAgeHours
	}
	latency := options.Latency
	if latency == nil {
		latency = func() time.Duration { return 0 }
	}
	return &Admin{
		store:         options.Store,
		portals:       registry,
		matcher:       matcher,
		interval:      options.Settings.CheckIntervalMinutes,
		latency:       latency,
		parseLocation: parseLocation,
		parseOptional: parseOptional,
		normalize:     normalize,
		countryByCode: countryByCode,
		portalOrder:   order,
		maxLocations:  maxLocations,
		minAge:        minAge,
		maxAge:        maxAge,
	}, nil
}

func textResponse(content string) Response {
	return Response{Content: content, Ephemeral: true}
}

func embedResponse(embed *Embed) Response {
	return Response{Embed: embed, Ephemeral: true}
}

// ErrorResponse maps handler errors to the exact user-facing texts.
func (a *Admin) ErrorResponse(err error) Response {
	if errors.Is(err, ErrPermissionDenied) {
		return textResponse("No tienes permiso para usar este comando.")
	}
	slog.Error("Admin command failed", "error", err)
	return textResponse("No se pudo completar el comando. Revisa los logs del bot.")
}

func clean(value, field string) (string, error) {
	cleaned := strings.Join(strings.Fields(value), " ")
	if cleaned == "" {
		return "", fmt.Errorf("%s no puede estar vacío.", field)
	}
	return truncateRunes(cleaned, 200), nil
}

func trimWhiteSpace(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func truncateRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}

func unique(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func formatAge(hours float64) string {
	minutes := roundHalfEven(hours * 60)
	if minutes < 60 {
		suffix := "s"
		if minutes == 1 {
			suffix = ""
		}
		return fmt.Sprintf("%d minuto%s", minutes, suffix)
	}
	whole, rest := minutes/60, minutes%60
	label := fmt.Sprintf("%d hora", whole)
	if whole != 1 {
		label += "s"
	}
	if rest != 0 {
		return fmt.Sprintf("%s %d min", label, rest)
	}
	return label
}

func roundHalfEven(value float64) int {
	floor := math.Floor(value)
	fraction := value - floor
	switch {
	case fraction > 0.5:
		return int(floor) + 1
	case fraction < 0.5:
		return int(floor)
	case int(floor)%2 == 0:
		return int(floor)
	default:
		return int(floor) + 1
	}
}

func limitChoices(values []Choice) []Choice {
	if len(values) > 25 {
		return values[:25]
	}
	return values
}

func (a *Admin) intents(values []string) []*locations.LocationIntent {
	if len(values) == 0 {
		values = []string{store.DefaultLocation}
	}
	result := make([]*locations.LocationIntent, 0, len(values))
	for _, value := range values {
		intent, err := a.parseOptional(value)
		if err == nil && intent != nil {
			result = append(result, intent)
		}
	}
	return result
}

func (a *Admin) locationKey(value string) string {
	intent, err := a.parseOptional(value)
	if err == nil && intent != nil {
		return a.normalize(intent.Label())
	}
	return a.normalize(value)
}

func (a *Admin) scraperPlans(scraper scrapers.Scraper, intents []*locations.LocationIntent) (searches, notes, reasons []string) {
	searches = make([]string, 0)
	notes = make([]string, 0)
	reasons = make([]string, 0)
	for _, intent := range intents {
		plan := scraper.Plan(intent)
		if !plan.Supported {
			if plan.Reason != "" && !containsString(reasons, plan.Reason) {
				reasons = append(reasons, plan.Reason)
			}
			continue
		}
		if plan.SearchLocation != "" && !containsString(searches, plan.SearchLocation) {
			searches = append(searches, plan.SearchLocation)
		}
		if plan.Note != "" && !containsString(notes, plan.Note) {
			notes = append(notes, plan.Note)
		}
	}
	return searches, notes, reasons
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func (a *Admin) planLines(portals []string, intents []*locations.LocationIntent) (lines, warnings []string) {
	lines = make([]string, 0)
	warnings = make([]string, 0)
	for _, slug := range portals {
		scraper := a.portals.Get(slug)
		if scraper == nil {
			continue
		}
		searches, notes, reasons := a.scraperPlans(scraper, intents)
		if len(searches) > 0 {
			lines = append(lines, fmt.Sprintf("• **%s** → %s", scraper.Source(), strings.Join(searches, " · ")))
			for _, note := range notes {
				lines = append(lines, "   ↳ "+note)
			}
			continue
		}
		lines = append(lines, fmt.Sprintf("• **%s** → pausado (⏸️)", scraper.Source()))
		for _, reason := range reasons {
			warnings = append(warnings, fmt.Sprintf("**%s**: %s", scraper.Source(), reason))
		}
	}
	return lines, warnings
}

func (a *Admin) portalSummary(portals []string, intents []*locations.LocationIntent) string {
	labels := make([]string, 0, len(portals))
	for _, slug := range portals {
		scraper := a.portals.Get(slug)
		if scraper == nil {
			labels = append(labels, slug)
			continue
		}
		searches, _, _ := a.scraperPlans(scraper, intents)
		state := "⏸️"
		if len(searches) > 0 {
			state = "✅"
		}
		labels = append(labels, scraper.Source()+" "+state)
	}
	if len(labels) == 0 {
		return "Ninguno"
	}
	return strings.Join(labels, ", ")
}

func (a *Admin) pauseWarnings(portals []string, intents []*locations.LocationIntent) []string {
	warnings := make([]string, 0)
	for _, slug := range portals {
		scraper := a.portals.Get(slug)
		if scraper == nil {
			continue
		}
		searches, _, reasons := a.scraperPlans(scraper, intents)
		if len(searches) > 0 {
			continue
		}
		reason := "sin ubicaciones compatibles."
		if len(reasons) > 0 {
			reason = reasons[0]
		}
		warnings = append(warnings, fmt.Sprintf("**%s** está en pausa: %s", scraper.Source(), reason))
	}
	return warnings
}

func (a *Admin) portalSupported(slug string, intents []*locations.LocationIntent) bool {
	scraper := a.portals.Get(slug)
	if scraper == nil {
		return false
	}
	for _, intent := range intents {
		if scraper.Plan(intent).Supported {
			return true
		}
	}
	return false
}

func (a *Admin) coveredCountries(scraper scrapers.Scraper) string {
	codes := append([]string(nil), scraper.CoveredCountries()...)
	if codes == nil {
		return "cualquier país"
	}
	sort.Strings(codes)
	names := make([]string, 0, len(codes))
	for _, code := range codes {
		country, ok := a.countryByCode(code)
		if !ok {
			names = append(names, code)
			continue
		}
		names = append(names, country.NameES)
	}
	return strings.Join(names, ", ")
}

func (a *Admin) portalLabel(slug string) string {
	if scraper := a.portals.Get(slug); scraper != nil {
		return scraper.Source()
	}
	return slug
}

func (a *Admin) orderedPortals() []string {
	return orderedPortalSlugs(a.portals, a.portalOrder)
}

func defaultPortalOrder() []string {
	return []string{
		scrapers.LinkedInSlug,
		scrapers.ChileTrabajosSlug,
		scrapers.ComputrabajoSlug,
		scrapers.FirstJobSlug,
		scrapers.GetOnBoardSlug,
	}
}

func orderedPortalSlugs(registry PortalRegistry, order []string) []string {
	result := make([]string, 0)
	seen := make(map[string]struct{})
	for _, slug := range order {
		if _, ok := seen[slug]; ok || registry.Get(slug) == nil {
			continue
		}
		seen[slug] = struct{}{}
		result = append(result, slug)
	}
	extra := make([]string, 0)
	for slug := range registry.Scrapers() {
		if _, ok := seen[slug]; ok {
			continue
		}
		extra = append(extra, slug)
	}
	sort.Strings(extra)
	return append(result, extra...)
}

// SetChannel stores the notification channel.
func (a *Admin) SetChannel(ctx context.Context, guildID int64, channel ChannelRef) (Response, error) {
	if err := a.store.SetChannel(ctx, guildID, channel.ID); err != nil {
		return Response{}, fmt.Errorf("set channel: %w", err)
	}
	return textResponse(fmt.Sprintf("Las vacantes se enviarán en %s.", channel.mention())), nil
}

// SetLocation replaces all saved locations with one parsed intent.
func (a *Admin) SetLocation(ctx context.Context, guildID int64, value string) (Response, error) {
	intent, err := a.parseLocation(value)
	if err != nil {
		return textResponse(err.Error()), nil
	}
	if err := a.store.SetLocation(ctx, guildID, intent.Label()); err != nil {
		return Response{}, fmt.Errorf("set location: %w", err)
	}
	config, err := a.store.GetGuildConfig(ctx, guildID)
	if err != nil {
		return Response{}, fmt.Errorf("read guild config: %w", err)
	}
	intents := a.intents(config.Locations)
	lines, warnings := a.planLines(config.Portals, intents)
	parts := []string{fmt.Sprintf("Ubicación configurada: **%s**", intent.Label())}
	if intent.InferredCountry {
		parts = append(parts, fmt.Sprintf(
			"Interpreté **%s** como ciudad de Chile. El formato recomendado es `ciudad, país`.",
			intent.City,
		))
	}
	parts = append(parts, strings.Join(lines, "\n"))
	if len(warnings) > 0 {
		parts = append(parts, strings.Join(warnings, "\n"))
	}
	supported := false
	for _, slug := range config.Portals {
		if a.portalSupported(slug, intents) {
			supported = true
			break
		}
	}
	if !supported {
		parts = append(parts, "⚠️ Ningún portal activo cubre esta ubicación. Activa LinkedIn o cambia la ubicación.")
	}
	return textResponse(strings.Join(parts, "\n\n")), nil
}

// AddLocation appends one parsed location unless it is duplicated or the guild
// already has the maximum number of locations.
func (a *Admin) AddLocation(ctx context.Context, guildID int64, value string) (Response, error) {
	intent, err := a.parseLocation(value)
	if err != nil {
		return textResponse(err.Error()), nil
	}
	config, err := a.store.GetGuildConfig(ctx, guildID)
	if err != nil {
		return Response{}, fmt.Errorf("read guild config: %w", err)
	}
	key := a.locationKey(intent.Label())
	for _, existing := range config.Locations {
		if a.locationKey(existing) == key {
			return textResponse(fmt.Sprintf("**%s** ya estaba configurada.", intent.Label())), nil
		}
	}
	if len(config.Locations) >= a.maxLocations {
		return textResponse(fmt.Sprintf(
			"Ya tienes **%d** ubicaciones, el máximo permitido. Quita una con `/remove-location` antes de agregar otra.",
			a.maxLocations,
		)), nil
	}
	if _, err := a.store.AddLocation(ctx, guildID, intent.Label()); err != nil {
		return Response{}, fmt.Errorf("add location: %w", err)
	}
	config, err = a.store.GetGuildConfig(ctx, guildID)
	if err != nil {
		return Response{}, fmt.Errorf("reload guild config: %w", err)
	}
	intents := a.intents(config.Locations)
	lines, warnings := a.planLines(config.Portals, intents)
	parts := []string{fmt.Sprintf(
		"Ubicación agregada: **%s** (%d/%d).", intent.Label(), len(config.Locations), a.maxLocations,
	)}
	if intent.InferredCountry {
		parts = append(parts, fmt.Sprintf(
			"Interpreté **%s** como ciudad de Chile. El formato recomendado es `ciudad, país`.",
			intent.City,
		))
	}
	parts = append(parts, strings.Join(lines, "\n"))
	if len(warnings) > 0 {
		parts = append(parts, strings.Join(warnings, "\n"))
	}
	return textResponse(strings.Join(parts, "\n\n")), nil
}

// RemoveLocation removes the saved location matching the supplied text.
func (a *Admin) RemoveLocation(ctx context.Context, guildID int64, value string) (Response, error) {
	config, err := a.store.GetGuildConfig(ctx, guildID)
	if err != nil {
		return Response{}, fmt.Errorf("read guild config: %w", err)
	}
	key := a.locationKey(value)
	target := ""
	for _, existing := range config.Locations {
		if a.locationKey(existing) == key {
			target = existing
			break
		}
	}
	if target == "" {
		return textResponse(fmt.Sprintf("No encontré **%s** entre tus ubicaciones.", value)), nil
	}
	wasLast := len(config.Locations) <= 1
	if _, err := a.store.RemoveLocation(ctx, guildID, target); err != nil {
		return Response{}, fmt.Errorf("remove location: %w", err)
	}
	config, err = a.store.GetGuildConfig(ctx, guildID)
	if err != nil {
		return Response{}, fmt.Errorf("reload guild config: %w", err)
	}
	intents := a.intents(config.Locations)
	lines, warnings := a.planLines(config.Portals, intents)
	parts := []string{fmt.Sprintf("Ubicación eliminada: **%s**.", target)}
	if wasLast {
		parts = append(parts, "No quedan ubicaciones guardadas; se usará **Worldwide** por defecto.")
	}
	parts = append(parts, strings.Join(lines, "\n"))
	if len(warnings) > 0 {
		parts = append(parts, strings.Join(warnings, "\n"))
	}
	return textResponse(strings.Join(parts, "\n\n")), nil
}

// RemoveLocationAutocomplete filters saved locations by normalized text.
func (a *Admin) RemoveLocationAutocomplete(ctx context.Context, guildID int64, current string) ([]Choice, error) {
	if guildID == 0 {
		return []Choice{}, nil
	}
	values, err := a.store.GetLocations(ctx, guildID)
	if err != nil {
		return nil, fmt.Errorf("get locations: %w", err)
	}
	needle := a.normalize(current)
	choices := make([]Choice, 0)
	for _, value := range values {
		if strings.Contains(a.normalize(value), needle) {
			choices = append(choices, Choice{Name: value, Value: value})
		}
	}
	return limitChoices(choices), nil
}

// SetMaxAge updates the maximum publication age.
func (a *Admin) SetMaxAge(ctx context.Context, guildID int64, hours float64) (Response, error) {
	if hours < a.minAge || hours > a.maxAge {
		return textResponse("La antigüedad debe estar entre 10 minutos y 24 horas (por ejemplo 0,5 = 30 minutos, 8 = 8 horas)."), nil
	}
	if err := a.store.SetMaxAgeHours(ctx, guildID, hours); err != nil {
		return Response{}, fmt.Errorf("set max age: %w", err)
	}
	return textResponse(fmt.Sprintf(
		"Buscaré vacantes publicadas en los últimos **%s**.", formatAge(hours),
	)), nil
}

// AddKeyword adds comma-separated keywords, deduplicating exact values.
func (a *Admin) AddKeyword(ctx context.Context, guildID int64, value string) (Response, error) {
	keywords := make([]string, 0)
	for _, part := range strings.Split(value, ",") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		cleaned, err := clean(part, "La palabra clave")
		if err != nil {
			return textResponse(err.Error()), nil
		}
		keywords = append(keywords, cleaned)
	}
	if len(keywords) == 0 {
		return textResponse("Debes indicar al menos una palabra clave."), nil
	}
	added := make([]string, 0)
	for _, keyword := range unique(keywords) {
		ok, err := a.store.AddKeyword(ctx, guildID, keyword)
		if err != nil {
			return Response{}, fmt.Errorf("add keyword: %w", err)
		}
		if ok {
			added = append(added, keyword)
		}
	}
	if len(added) > 0 {
		return textResponse(fmt.Sprintf("Palabras clave agregadas: **%s**.", strings.Join(added, ", "))), nil
	}
	return textResponse("Todas las palabras clave ya estaban configuradas."), nil
}

// RemoveKeyword removes comma-separated keywords.
func (a *Admin) RemoveKeyword(ctx context.Context, guildID int64, value string) (Response, error) {
	keywords := make([]string, 0)
	for _, part := range strings.Split(value, ",") {
		if trimmed := trimWhiteSpace(part); trimmed != "" {
			keywords = append(keywords, trimmed)
		}
	}
	if len(keywords) == 0 {
		return textResponse("Debes indicar al menos una palabra clave."), nil
	}
	removed := make([]string, 0)
	for _, keyword := range unique(keywords) {
		ok, err := a.store.RemoveKeyword(ctx, guildID, keyword)
		if err != nil {
			return Response{}, fmt.Errorf("remove keyword: %w", err)
		}
		if ok {
			removed = append(removed, keyword)
		}
	}
	if len(removed) > 0 {
		return textResponse(fmt.Sprintf("Palabras clave eliminadas: **%s**.", strings.Join(removed, ", "))), nil
	}
	return textResponse("No encontré ninguna de esas palabras clave."), nil
}

// RemoveKeywordAutocomplete filters configured keywords by casefolded text.
func (a *Admin) RemoveKeywordAutocomplete(ctx context.Context, guildID int64, current string) ([]Choice, error) {
	if guildID == 0 {
		return []Choice{}, nil
	}
	config, err := a.store.GetGuildConfig(ctx, guildID)
	if err != nil {
		return nil, fmt.Errorf("read guild config: %w", err)
	}
	needle := strings.ToLower(strings.TrimSpace(current))
	choices := make([]Choice, 0)
	for _, keyword := range config.Keywords {
		if strings.Contains(strings.ToLower(keyword), needle) {
			choices = append(choices, Choice{Name: keyword, Value: keyword})
		}
	}
	return limitChoices(choices), nil
}

// AddBlacklistCompany ignores jobs from one company.
func (a *Admin) AddBlacklistCompany(ctx context.Context, guildID int64, value string) (Response, error) {
	company, err := clean(value, "La empresa")
	if err != nil {
		return textResponse(err.Error()), nil
	}
	added, err := a.store.AddBlacklistedCompany(ctx, guildID, company)
	if err != nil {
		return Response{}, fmt.Errorf("add blacklisted company: %w", err)
	}
	if added {
		return textResponse(fmt.Sprintf("Las vacantes de **%s** serán ignoradas.", company)), nil
	}
	return textResponse("Esa empresa ya estaba en la blacklist."), nil
}

// RemoveBlacklistCompany stops ignoring jobs from one company.
func (a *Admin) RemoveBlacklistCompany(ctx context.Context, guildID int64, value string) (Response, error) {
	company := trimWhiteSpace(value)
	removed, err := a.store.RemoveBlacklistedCompany(ctx, guildID, company)
	if err != nil {
		return Response{}, fmt.Errorf("remove blacklisted company: %w", err)
	}
	if removed {
		return textResponse("Empresa eliminada de la blacklist."), nil
	}
	return textResponse("No encontré esa empresa."), nil
}

// AddBlacklistKeyword excludes comma-separated words from job titles and can
// add common aliases from the embedded catalog.
func (a *Admin) AddBlacklistKeyword(ctx context.Context, guildID int64, value string, includeAliases bool) (Response, error) {
	keywords := make([]string, 0)
	for _, part := range strings.Split(value, ",") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		cleaned, err := clean(part, "La palabra clave a excluir")
		if err != nil {
			return textResponse(err.Error()), nil
		}
		keywords = append(keywords, cleaned)
	}
	if len(keywords) == 0 {
		return textResponse("Debes indicar al menos una palabra clave para excluir."), nil
	}
	expanded, autoAliases := a.matcher.ExpandBlacklistKeywords(keywords, includeAliases)
	autoSet := make(map[string]struct{}, len(autoAliases))
	for _, alias := range autoAliases {
		autoSet[alias] = struct{}{}
	}
	addedMain := make([]string, 0)
	addedAliases := make([]string, 0)
	for _, keyword := range expanded {
		added, err := a.store.AddBlacklistedKeyword(ctx, guildID, keyword)
		if err != nil {
			return Response{}, fmt.Errorf("add blacklisted keyword: %w", err)
		}
		if !added {
			continue
		}
		if _, ok := autoSet[keyword]; ok {
			addedAliases = append(addedAliases, keyword)
		} else {
			addedMain = append(addedMain, keyword)
		}
	}
	parts := make([]string, 0, 2)
	if len(addedMain) > 0 {
		parts = append(parts, fmt.Sprintf(
			"Palabras excluidas en títulos agregadas: **%s**.", strings.Join(addedMain, ", "),
		))
	}
	if len(addedAliases) > 0 {
		parts = append(parts, fmt.Sprintf(
			"Alias comunes incluidos automáticamente: **%s**.", strings.Join(addedAliases, ", "),
		))
	}
	if len(parts) == 0 {
		parts = append(parts, "Todas esas palabras ya estaban en la lista de exclusión.")
	}
	return textResponse(strings.Join(parts, "\n")), nil
}

// RemoveBlacklistKeyword removes comma-separated excluded words.
func (a *Admin) RemoveBlacklistKeyword(ctx context.Context, guildID int64, value string) (Response, error) {
	keywords := make([]string, 0)
	for _, part := range strings.Split(value, ",") {
		if trimmed := trimWhiteSpace(part); trimmed != "" {
			keywords = append(keywords, trimmed)
		}
	}
	if len(keywords) == 0 {
		return textResponse("Debes indicar al menos una palabra clave."), nil
	}
	removed := make([]string, 0)
	for _, keyword := range unique(keywords) {
		ok, err := a.store.RemoveBlacklistedKeyword(ctx, guildID, keyword)
		if err != nil {
			return Response{}, fmt.Errorf("remove blacklisted keyword: %w", err)
		}
		if ok {
			removed = append(removed, keyword)
		}
	}
	if len(removed) > 0 {
		return textResponse(fmt.Sprintf("Palabras excluidas eliminadas: **%s**.", strings.Join(removed, ", "))), nil
	}
	return textResponse("No encontré ninguna de esas palabras en la lista de exclusión."), nil
}

// RemoveBlacklistKeywordAutocomplete filters excluded words by normalized text.
func (a *Admin) RemoveBlacklistKeywordAutocomplete(ctx context.Context, guildID int64, current string) ([]Choice, error) {
	if guildID == 0 {
		return []Choice{}, nil
	}
	config, err := a.store.GetGuildConfig(ctx, guildID)
	if err != nil {
		return nil, fmt.Errorf("read guild config: %w", err)
	}
	needle := a.normalize(current)
	choices := make([]Choice, 0)
	for _, keyword := range config.BlacklistedKeywords {
		if strings.Contains(a.normalize(keyword), needle) {
			choices = append(choices, Choice{Name: keyword, Value: keyword})
		}
	}
	return limitChoices(choices), nil
}

// Help returns the full configuration guide embed.
func (a *Admin) Help() Response {
	return embedResponse(BuildHelpEmbed())
}

// AddLanguage enables one canonical language.
func (a *Admin) AddLanguage(ctx context.Context, guildID int64, value string) (Response, error) {
	values := unique(languages.ParseLanguages(value))
	if len(values) == 0 {
		return textResponse("Debes indicar al menos un idioma."), nil
	}
	unsupported := make([]string, 0)
	for _, language := range values {
		if !languages.IsSupported(language) {
			unsupported = append(unsupported, language)
		}
	}
	if len(unsupported) > 0 {
		return textResponse(fmt.Sprintf(
			"Idiomas no soportados: **%s**. Usa español, inglés, portugués, francés o alemán.",
			strings.Join(unsupported, ", "),
		)), nil
	}
	added := make([]string, 0)
	for _, language := range values {
		ok, err := a.store.AddLanguage(ctx, guildID, language)
		if err != nil {
			return Response{}, fmt.Errorf("add language: %w", err)
		}
		if ok {
			added = append(added, language)
		}
	}
	if len(added) > 0 {
		return textResponse(fmt.Sprintf("Idiomas agregados: **%s**.", strings.Join(added, ", "))), nil
	}
	return textResponse("Esos idiomas ya estaban configurados."), nil
}

// RemoveLanguage disables one canonical language.
func (a *Admin) RemoveLanguage(ctx context.Context, guildID int64, value string) (Response, error) {
	values := unique(languages.ParseLanguages(value))
	if len(values) == 0 {
		return textResponse("Debes indicar al menos un idioma."), nil
	}
	removed := make([]string, 0)
	for _, language := range values {
		ok, err := a.store.RemoveLanguage(ctx, guildID, language)
		if err != nil {
			return Response{}, fmt.Errorf("remove language: %w", err)
		}
		if ok {
			removed = append(removed, language)
		}
	}
	if len(removed) > 0 {
		return textResponse(fmt.Sprintf("Idiomas eliminados: **%s**.", strings.Join(removed, ", "))), nil
	}
	return textResponse("No encontré esos idiomas."), nil
}

// AddPortal activates a portal when its coverage matches the saved locations.
func (a *Admin) AddPortal(ctx context.Context, guildID int64, slug, name string) (Response, error) {
	config, err := a.store.GetGuildConfig(ctx, guildID)
	if err != nil {
		return Response{}, fmt.Errorf("read guild config: %w", err)
	}
	if containsString(config.Portals, slug) {
		return textResponse(fmt.Sprintf("El portal **%s** ya estaba activo.", name)), nil
	}
	scraper := a.portals.Get(slug)
	intents := a.intents(config.Locations)
	if scraper != nil && !a.portalSupported(slug, intents) {
		locationsText := strings.Join(config.Locations, ", ")
		if locationsText == "" {
			locationsText = "ninguna"
		}
		return textResponse(fmt.Sprintf(
			"No activé **%s**: solo cubre %s y tus ubicaciones actuales son **%s**.\n\n"+
				"Cambia la ubicación con `/set-location` o agrega una compatible con `/add-location`, y vuelve a intentarlo.",
			name, a.coveredCountries(scraper), locationsText,
		)), nil
	}
	if _, err := a.store.AddPortal(ctx, guildID, slug); err != nil {
		return Response{}, fmt.Errorf("add portal: %w", err)
	}
	lines, warnings := a.planLines([]string{slug}, intents)
	parts := []string{fmt.Sprintf("Portal activado: **%s**.", name)}
	if len(lines) > 0 {
		parts = append(parts, strings.Join(lines, "\n"))
	}
	if len(warnings) > 0 {
		parts = append(parts, strings.Join(warnings, "\n"))
	}
	return textResponse(strings.Join(parts, "\n\n")), nil
}

// RemovePortal deactivates a portal but never the last active one.
func (a *Admin) RemovePortal(ctx context.Context, guildID int64, slug string) (Response, error) {
	config, err := a.store.GetGuildConfig(ctx, guildID)
	if err != nil {
		return Response{}, fmt.Errorf("read guild config: %w", err)
	}
	label := a.portalLabel(slug)
	if !containsString(config.Portals, slug) {
		return textResponse(fmt.Sprintf("El portal **%s** no estaba activo.", label)), nil
	}
	if len(config.Portals) <= 1 {
		return textResponse("No puedes desactivar el último portal. Activa otro antes de quitarlo."), nil
	}
	removed, err := a.store.RemovePortal(ctx, guildID, slug)
	if err != nil {
		return Response{}, fmt.Errorf("remove portal: %w", err)
	}
	if removed {
		return textResponse(fmt.Sprintf("Portal desactivado: **%s**.", label)), nil
	}
	return textResponse(fmt.Sprintf("El portal **%s** no estaba activo.", label)), nil
}

// RemovePortalAutocomplete filters active portals by label or slug.
func (a *Admin) RemovePortalAutocomplete(ctx context.Context, guildID int64, current string) ([]Choice, error) {
	if guildID == 0 {
		return []Choice{}, nil
	}
	config, err := a.store.GetGuildConfig(ctx, guildID)
	if err != nil {
		return nil, fmt.Errorf("read guild config: %w", err)
	}
	needle := strings.ToLower(strings.TrimSpace(current))
	choices := make([]Choice, 0)
	for _, slug := range config.Portals {
		label := a.portalLabel(slug)
		if strings.Contains(strings.ToLower(label), needle) || strings.Contains(strings.ToLower(slug), needle) {
			choices = append(choices, Choice{Name: label, Value: slug})
		}
	}
	return limitChoices(choices), nil
}

// ListLanguages reports all supported languages and the active ones.
func (a *Admin) ListLanguages(ctx context.Context, guildID int64) (Response, error) {
	config, err := a.store.GetGuildConfig(ctx, guildID)
	if err != nil {
		return Response{}, fmt.Errorf("read guild config: %w", err)
	}
	available := make([]string, 0, len(languages.LanguageLabelOrder))
	for _, code := range languages.LanguageLabelOrder {
		available = append(available, fmt.Sprintf("• **%s** — `%s`", languages.Label(code), code))
	}
	active := make([]string, 0, len(config.Languages))
	for _, code := range config.Languages {
		active = append(active, languages.Label(code))
	}
	activeText := strings.Join(active, ", ")
	if activeText == "" {
		activeText = "Todos (sin filtro)"
	}
	return textResponse(fmt.Sprintf(
		"**Idiomas disponibles**\n%s\n\n**Activos en este servidor:** %s\n\n"+
			"Puedes usar el nombre o el código con `/add-language` y `/remove-language`.",
		strings.Join(available, "\n"), activeText,
	)), nil
}

// ListPortals reports portal coverage, activation and pause state.
func (a *Admin) ListPortals(ctx context.Context, guildID int64) (Response, error) {
	config, err := a.store.GetGuildConfig(ctx, guildID)
	if err != nil {
		return Response{}, fmt.Errorf("read guild config: %w", err)
	}
	intents := a.intents(config.Locations)
	active := make(map[string]struct{}, len(config.Portals))
	for _, slug := range config.Portals {
		active[slug] = struct{}{}
	}
	lines := []string{"**Portales disponibles**"}
	running := 0
	for _, slug := range a.orderedPortals() {
		scraper := a.portals.Get(slug)
		if scraper == nil {
			continue
		}
		coverage := " · cobertura: " + a.coveredCountries(scraper)
		if _, ok := active[slug]; !ok {
			lines = append(lines, fmt.Sprintf("• **%s** (`%s`) — no activo%s", scraper.Source(), slug, coverage))
			continue
		}
		searches, notes, reasons := a.scraperPlans(scraper, intents)
		if len(searches) > 0 {
			running++
			lines = append(lines, fmt.Sprintf("• **%s** (`%s`) — ✅ activo%s", scraper.Source(), slug, coverage))
			lines = append(lines, "   ↳ Busca: "+strings.Join(searches, " · "))
			for _, note := range notes {
				lines = append(lines, "   ↳ "+note)
			}
			continue
		}
		lines = append(lines, fmt.Sprintf("• **%s** (`%s`) — pausado (⏸️) %s", scraper.Source(), slug, coverage))
		for _, reason := range reasons {
			lines = append(lines, "   ↳ "+reason)
		}
	}
	activeNames := a.portals.PortalNames(config.Portals)
	activeText := strings.Join(activeNames, ", ")
	if activeText == "" {
		activeText = "Ninguno"
	}
	parts := []string{
		strings.Join(lines, "\n"),
		fmt.Sprintf("**Activos en este servidor:** %s\n**Ejecutándose ahora:** %d de %d",
			activeText, running, len(config.Portals)),
	}
	if running == 0 {
		parts = append(parts, "⚠️ Ningún portal se está ejecutando con las ubicaciones actuales. Revisa `/list-locations`.")
	}
	parts = append(parts, PAUSE_HELP, "Usa `/add-portal` y `/remove-portal` para cambiarlos.")
	return textResponse(strings.Join(parts, "\n\n")), nil
}

// ListLocations reports saved locations and their per-portal translation.
func (a *Admin) ListLocations(ctx context.Context, guildID int64) (Response, error) {
	config, err := a.store.GetGuildConfig(ctx, guildID)
	if err != nil {
		return Response{}, fmt.Errorf("read guild config: %w", err)
	}
	intents := a.intents(config.Locations)
	current := make([]string, 0, len(intents))
	if len(config.Locations) == 0 {
		current = append(current, "• Sin ubicaciones guardadas · se usa **"+store.DefaultLocation+"**")
	} else {
		for _, intent := range intents {
			current = append(current, "• **"+intent.Label()+"**")
		}
	}
	lines, warnings := a.planLines(config.Portals, intents)
	parts := []string{
		fmt.Sprintf("**Tus ubicaciones (%d/%d)**\n%s", len(config.Locations), a.maxLocations, strings.Join(current, "\n")),
		"**Traducción por portal**\n" + strings.Join(lines, "\n"),
	}
	if len(warnings) > 0 {
		parts = append(parts, strings.Join(warnings, "\n"))
	}
	parts = append(parts, locations.LocationFormatHelp, PAUSE_HELP)
	return embedResponse(&Embed{
		Title:       "Ubicaciones de búsqueda",
		Description: strings.Join(parts, "\n\n"),
		Color:       EmbedColor,
	}), nil
}

// ListKeywords reports the effective guild configuration.
func (a *Admin) ListKeywords(ctx context.Context, guildID int64) (Response, error) {
	config, err := a.store.GetGuildConfig(ctx, guildID)
	if err != nil {
		return Response{}, fmt.Errorf("read guild config: %w", err)
	}
	intents := a.intents(config.Locations)
	channel := "No configurado"
	if config.ChannelID != nil {
		channel = fmt.Sprintf("<#%d>", *config.ChannelID)
	}
	locationsText := "**" + store.DefaultLocation + "** (por defecto)"
	if len(config.Locations) > 0 {
		labels := make([]string, 0, len(intents))
		for _, intent := range intents {
			labels = append(labels, "**"+intent.Label()+"**")
		}
		locationsText = strings.Join(labels, " · ")
	}
	keywords := strings.Join(config.Keywords, ", ")
	if keywords == "" {
		keywords = "Ninguna"
	}
	blockedKeywords := strings.Join(config.BlacklistedKeywords, ", ")
	if blockedKeywords == "" {
		blockedKeywords = "Ninguna"
	}
	companies := strings.Join(config.BlacklistedCompanies, ", ")
	if companies == "" {
		companies = "Ninguna"
	}
	langs := strings.Join(config.Languages, ", ")
	if langs == "" {
		langs = "Todos"
	}
	parts := []string{
		"**Canal:** " + channel,
		fmt.Sprintf("**Ubicaciones (%d/%d):** %s", len(config.Locations), a.maxLocations, locationsText),
		"**Portales:** " + a.portalSummary(config.Portals, intents),
		"**Antigüedad máxima:** " + formatAge(config.MaxAgeHours),
		"**Palabras clave:** " + keywords,
		"**Palabras excluidas:** " + blockedKeywords,
		"**Empresas ignoradas:** " + companies,
		"**Idiomas:** " + langs,
	}
	warnings := a.pauseWarnings(config.Portals, intents)
	if len(warnings) > 0 {
		parts = append(parts, strings.Join(warnings, "\n"), PAUSE_HELP)
	}
	return textResponse(strings.Join(parts, "\n")), nil
}

// Status reports the bot state and the guild's portal health.
func (a *Admin) Status(ctx context.Context, guildID int64) (Response, error) {
	config, err := a.store.GetGuildConfig(ctx, guildID)
	if err != nil {
		return Response{}, fmt.Errorf("read guild config: %w", err)
	}
	intents := a.intents(config.Locations)
	total, err := a.store.CountNotified(ctx, guildID)
	if err != nil {
		return Response{}, fmt.Errorf("count notified: %w", err)
	}
	parts := []string{
		"**Estado:** online",
		"**Portales activos:** " + a.portalSummary(config.Portals, intents),
		fmt.Sprintf("**Latencia:** %.0f ms", a.latency().Seconds()*1000),
		fmt.Sprintf("**Ofertas enviadas:** %d", total),
		"**Intervalo:** " + strconv.FormatFloat(a.interval, 'g', 6, 64) + " min",
		"**Antigüedad máxima:** " + formatAge(config.MaxAgeHours),
	}
	warnings := a.pauseWarnings(config.Portals, intents)
	if len(warnings) > 0 {
		parts = append(parts, strings.Join(warnings, "\n"))
	}
	running := false
	for _, slug := range config.Portals {
		if a.portalSupported(slug, intents) {
			running = true
			break
		}
	}
	if !running {
		parts = append(parts, "⚠️ Ningún portal se está ejecutando con las ubicaciones actuales. Revisa `/list-locations`.")
	}
	return textResponse(strings.Join(parts, "\n")), nil
}
