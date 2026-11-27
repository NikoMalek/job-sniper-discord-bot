package bot

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/NikoMalek/job-sniper-discord-bot/internal/config"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/scrapers"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/store"
	"github.com/bwmarrin/discordgo"
)

func TestAdminCommandDefinitions(t *testing.T) {
	commands := AdminCommandDefinitions(PortalChoices(basicRegistry()))
	wantNames := []string{
		"set-channel", "set-location", "add-location", "remove-location", "set-max-age",
		"add-keyword", "remove-keyword", "add-blacklist-company", "remove-blacklist-company",
		"add-blacklist-keyword", "remove-blacklist-keyword", "help", "add-language",
		"remove-language", "add-portal", "remove-portal", "list-languages", "list-portals",
		"list-locations", "list-keywords", "status",
	}
	gotNames := make([]string, 0, len(commands))
	for _, command := range commands {
		gotNames = append(gotNames, command.Name)
		if command.DefaultMemberPermissions == nil ||
			*command.DefaultMemberPermissions != int64(discordgo.PermissionManageGuild) {
			t.Errorf("%s: manage_guild default permission missing", command.Name)
		}
		if command.DMPermission == nil || *command.DMPermission {
			t.Errorf("%s: command is not guild-only", command.Name)
		}
		if command.Description == "" {
			t.Errorf("%s: empty description", command.Name)
		}
	}
	if !reflect.DeepEqual(gotNames, wantNames) {
		t.Fatalf("command names = %v, want %v", gotNames, wantNames)
	}

	byName := make(map[string]*discordgo.ApplicationCommand, len(commands))
	for _, command := range commands {
		byName[command.Name] = command
	}
	if option := byName["set-channel"].Options[0]; option.Type != discordgo.ApplicationCommandOptionChannel ||
		!option.Required || len(option.ChannelTypes) != 1 || option.ChannelTypes[0] != discordgo.ChannelTypeGuildText {
		t.Errorf("set-channel option = %#v", option)
	}
	for _, name := range []string{"remove-location", "remove-keyword", "remove-blacklist-keyword", "remove-portal"} {
		if !byName[name].Options[0].Autocomplete {
			t.Errorf("%s option is not autocomplete", name)
		}
	}
	aliasChoices := byName["add-blacklist-keyword"].Options[1].Choices
	if len(aliasChoices) != 2 || aliasChoices[0].Name != "Sí (incluir alias comunes)" ||
		aliasChoices[1].Name != "No (solo las palabras indicadas)" {
		t.Errorf("alias choices = %#v", aliasChoices)
	}
	languageChoices := byName["add-language"].Options[0].Choices
	if len(languageChoices) != 5 || languageChoices[0].Name != "Español (spanish)" {
		t.Errorf("language choices = %#v", languageChoices)
	}
	portalChoices := byName["add-portal"].Options[0].Choices
	if len(portalChoices) != 2 || portalChoices[0].Name != "LinkedIn" || portalChoices[1].Value != "chiletrabajos" {
		t.Errorf("portal choices = %#v", portalChoices)
	}
}

func TestAddAndRemoveLanguage(t *testing.T) {
	st := &fakeStore{}
	admin := newTestAdmin(t, st, basicRegistry())
	response, err := admin.AddLanguage(context.Background(), 1, "es, english")
	if err != nil {
		t.Fatalf("AddLanguage() error = %v", err)
	}
	if got, want := responseContent(t, response), "Idiomas agregados: **spanish, english**."; got != want {
		t.Fatalf("add = %q, want %q", got, want)
	}
	response, err = admin.AddLanguage(context.Background(), 1, "klingon")
	if err != nil {
		t.Fatalf("AddLanguage(unsupported) error = %v", err)
	}
	if got, want := responseContent(t, response), "Idiomas no soportados: **klingon**. Usa español, inglés, portugués, francés o alemán."; got != want {
		t.Fatalf("unsupported = %q, want %q", got, want)
	}
	response, err = admin.RemoveLanguage(context.Background(), 1, "es")
	if err != nil {
		t.Fatalf("RemoveLanguage() error = %v", err)
	}
	if got, want := responseContent(t, response), "Idiomas eliminados: **spanish**."; got != want {
		t.Fatalf("remove = %q, want %q", got, want)
	}
}

func TestAddPortal(t *testing.T) {
	st := &fakeStore{config: store.GuildMonitorConfig{
		Portals:   []string{"linkedin"},
		Locations: []string{"Chile"},
	}}
	registry := basicRegistry()
	admin := newTestAdmin(t, st, registry)
	response, err := admin.AddPortal(context.Background(), 1, "linkedin", "LinkedIn")
	if err != nil {
		t.Fatalf("AddPortal(active) error = %v", err)
	}
	if got, want := responseContent(t, response), "El portal **LinkedIn** ya estaba activo."; got != want {
		t.Fatalf("active = %q, want %q", got, want)
	}

	st.config.Locations = []string{"España"}
	response, err = admin.AddPortal(context.Background(), 1, "chiletrabajos", "ChileTrabajos")
	if err != nil {
		t.Fatalf("AddPortal(unsupported) error = %v", err)
	}
	got := responseContent(t, response)
	if !strings.HasPrefix(got, "No activé **ChileTrabajos**: solo cubre Chile y tus ubicaciones actuales son **España**.") {
		t.Fatalf("unsupported = %q", got)
	}

	st.config.Locations = []string{"Chile"}
	response, err = admin.AddPortal(context.Background(), 1, "chiletrabajos", "ChileTrabajos")
	if err != nil {
		t.Fatalf("AddPortal(success) error = %v", err)
	}
	if got, want := responseContent(t, response), "Portal activado: **ChileTrabajos**.\n\n• **ChileTrabajos** → Chile"; got != want {
		t.Fatalf("success = %q, want %q", got, want)
	}
}

func TestRemovePortal(t *testing.T) {
	st := &fakeStore{config: store.GuildMonitorConfig{Portals: []string{"linkedin"}}}
	admin := newTestAdmin(t, st, basicRegistry())
	response, err := admin.RemovePortal(context.Background(), 1, "chiletrabajos")
	if err != nil {
		t.Fatalf("RemovePortal(inactive) error = %v", err)
	}
	if got, want := responseContent(t, response), "El portal **ChileTrabajos** no estaba activo."; got != want {
		t.Fatalf("inactive = %q, want %q", got, want)
	}
	response, err = admin.RemovePortal(context.Background(), 1, "linkedin")
	if err != nil {
		t.Fatalf("RemovePortal(last) error = %v", err)
	}
	if got, want := responseContent(t, response), "No puedes desactivar el último portal. Activa otro antes de quitarlo."; got != want {
		t.Fatalf("last = %q, want %q", got, want)
	}
	st.config.Portals = append(st.config.Portals, "chiletrabajos")
	response, err = admin.RemovePortal(context.Background(), 1, "chiletrabajos")
	if err != nil {
		t.Fatalf("RemovePortal() error = %v", err)
	}
	if got, want := responseContent(t, response), "Portal desactivado: **ChileTrabajos**."; got != want {
		t.Fatalf("remove = %q, want %q", got, want)
	}
}

func TestPortalAutocomplete(t *testing.T) {
	st := &fakeStore{config: store.GuildMonitorConfig{Portals: []string{"linkedin", "chiletrabajos"}}}
	admin := newTestAdmin(t, st, basicRegistry())
	choices, err := admin.RemovePortalAutocomplete(context.Background(), 1, "CHILE")
	if err != nil {
		t.Fatalf("autocomplete error = %v", err)
	}
	want := []Choice{{Name: "ChileTrabajos", Value: "chiletrabajos"}}
	if !reflect.DeepEqual(choices, want) {
		t.Fatalf("choices = %#v, want %#v", choices, want)
	}
}

func TestBlacklistKeywordAutocomplete(t *testing.T) {
	st := &fakeStore{config: store.GuildMonitorConfig{
		BlacklistedKeywords: []string{"Práctica", "docente"},
	}}
	admin := newTestAdmin(t, st, basicRegistry())
	choices, err := admin.RemoveBlacklistKeywordAutocomplete(context.Background(), 1, "practica")
	if err != nil {
		t.Fatalf("autocomplete error = %v", err)
	}
	want := []Choice{{Name: "Práctica", Value: "Práctica"}}
	if !reflect.DeepEqual(choices, want) {
		t.Fatalf("choices = %#v, want %#v", choices, want)
	}
}

func TestListLanguages(t *testing.T) {
	st := &fakeStore{config: store.GuildMonitorConfig{Languages: []string{"spanish", "german"}}}
	admin := newTestAdmin(t, st, basicRegistry())
	response, err := admin.ListLanguages(context.Background(), 1)
	if err != nil {
		t.Fatalf("ListLanguages() error = %v", err)
	}
	got := responseContent(t, response)
	if !strings.HasPrefix(got, "**Idiomas disponibles**\n• **español** — `spanish`") {
		t.Fatalf("languages = %q", got)
	}
	if !strings.Contains(got, "**Activos en este servidor:** español, alemán") {
		t.Fatalf("languages = %q", got)
	}
	if !strings.HasSuffix(got, "Puedes usar el nombre o el código con `/add-language` y `/remove-language`.") {
		t.Fatalf("languages footer = %q", got)
	}
}

func TestListPortals(t *testing.T) {
	st := &fakeStore{config: store.GuildMonitorConfig{
		Portals:   []string{"linkedin", "chiletrabajos"},
		Locations: []string{"Chile"},
	}}
	admin := newTestAdmin(t, st, basicRegistry())
	response, err := admin.ListPortals(context.Background(), 1)
	if err != nil {
		t.Fatalf("ListPortals() error = %v", err)
	}
	got := responseContent(t, response)
	for _, part := range []string{
		"• **LinkedIn** (`linkedin`) — ✅ activo · cobertura: cualquier país",
		"   ↳ Busca: Chile",
		"• **ChileTrabajos** (`chiletrabajos`) — ✅ activo · cobertura: Chile",
		"**Ejecutándose ahora:** 2 de 2",
		PAUSE_HELP,
		"Usa `/add-portal` y `/remove-portal` para cambiarlos.",
	} {
		if !strings.Contains(got, part) {
			t.Errorf("ListPortals() missing %q in %q", part, got)
		}
	}
}

func TestListLocationsAndKeywords(t *testing.T) {
	channelID := int64(555)
	st := &fakeStore{config: store.GuildMonitorConfig{
		ChannelID:   &channelID,
		Portals:     []string{"linkedin"},
		Locations:   []string{"Chile"},
		Keywords:    []string{"python"},
		MaxAgeHours: 24,
	}}
	admin := newTestAdmin(t, st, basicRegistry())
	locationsResponse, err := admin.ListLocations(context.Background(), 1)
	if err != nil {
		t.Fatalf("ListLocations() error = %v", err)
	}
	if locationsResponse.Embed == nil || locationsResponse.Embed.Title != "Ubicaciones de búsqueda" {
		t.Fatalf("ListLocations() embed = %#v", locationsResponse.Embed)
	}
	if !strings.Contains(locationsResponse.Embed.Description, "**Tus ubicaciones (1/3)**\n• **Chile**") {
		t.Fatalf("ListLocations() description = %q", locationsResponse.Embed.Description)
	}
	if !strings.HasSuffix(locationsResponse.Embed.Description, PAUSE_HELP) {
		t.Fatalf("ListLocations() missing pause help: %q", locationsResponse.Embed.Description)
	}

	keywordsResponse, err := admin.ListKeywords(context.Background(), 1)
	if err != nil {
		t.Fatalf("ListKeywords() error = %v", err)
	}
	got := responseContent(t, keywordsResponse)
	for _, part := range []string{
		"**Canal:** <#555>",
		"**Ubicaciones (1/3):** **Chile**",
		"**Portales:** LinkedIn ✅",
		"**Antigüedad máxima:** 24 horas",
		"**Palabras clave:** python",
		"**Palabras excluidas:** Ninguna",
		"**Empresas ignoradas:** Ninguna",
		"**Idiomas:** Todos",
	} {
		if !strings.Contains(got, part) {
			t.Errorf("ListKeywords() missing %q in %q", part, got)
		}
	}
}

func TestStatus(t *testing.T) {
	st := &fakeStore{
		config: store.GuildMonitorConfig{
			Portals:     []string{"linkedin"},
			Locations:   []string{"Chile"},
			MaxAgeHours: 0.5,
		},
		notified: 7,
	}
	admin, err := NewAdmin(context.Background(), AdminOptions{
		Store:    st,
		Registry: basicRegistry(),
		Settings: config.Settings{CheckIntervalMinutes: 5},
		Latency:  func() time.Duration { return 120 * time.Millisecond },
	})
	if err != nil {
		t.Fatalf("NewAdmin() error = %v", err)
	}
	response, err := admin.Status(context.Background(), 1)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	got := responseContent(t, response)
	for _, part := range []string{
		"**Estado:** online",
		"**Portales activos:** LinkedIn ✅",
		"**Latencia:** 120 ms",
		"**Ofertas enviadas:** 7",
		"**Intervalo:** 5 min",
		"**Antigüedad máxima:** 30 minutos",
	} {
		if !strings.Contains(got, part) {
			t.Errorf("Status() missing %q in %q", part, got)
		}
	}
}

func TestPauseWarningsAndRunningSummary(t *testing.T) {
	st := &fakeStore{config: store.GuildMonitorConfig{
		Portals:   []string{"chiletrabajos"},
		Locations: []string{"España"},
	}}
	admin := newTestAdmin(t, st, basicRegistry())
	response, err := admin.ListKeywords(context.Background(), 1)
	if err != nil {
		t.Fatalf("ListKeywords() error = %v", err)
	}
	got := responseContent(t, response)
	if !strings.Contains(got, "**ChileTrabajos** está en pausa: sin cobertura.") || !strings.Contains(got, PAUSE_HELP) {
		t.Fatalf("ListKeywords() warnings = %q", got)
	}
	portals, err := admin.ListPortals(context.Background(), 1)
	if err != nil {
		t.Fatalf("ListPortals() error = %v", err)
	}
	if !strings.Contains(responseContent(t, portals), "⚠️ Ningún portal se está ejecutando con las ubicaciones actuales. Revisa `/list-locations`.") {
		t.Fatalf("ListPortals() = %q", responseContent(t, portals))
	}
}

func TestPortalCoverageUsesCountryNames(t *testing.T) {
	registry := &fakeRegistry{portals: map[string]scrapers.Scraper{
		"linkedin": &fakeScraper{slug: "linkedin", source: "LinkedIn", covered: []string{"CL", "ES"}},
	}}
	admin := newTestAdmin(t, &fakeStore{}, registry)
	scraper := registry.Get("linkedin")
	if got, want := admin.coveredCountries(scraper), "Chile, España"; got != want {
		t.Fatalf("coveredCountries() = %q, want %q", got, want)
	}
	registry.portals["all"] = &fakeScraper{slug: "all", source: "All"}
	if got, want := admin.coveredCountries(registry.Get("all")), "cualquier país"; got != want {
		t.Fatalf("coveredCountries(all) = %q, want %q", got, want)
	}
}

func TestLocationKeyAndIntentsFallBackToWorldwide(t *testing.T) {
	admin := newTestAdmin(t, &fakeStore{}, basicRegistry())
	if got, want := admin.locationKey("worldwide"), "worldwide"; got != want {
		t.Fatalf("locationKey() = %q, want %q", got, want)
	}
	intents := admin.intents(nil)
	if len(intents) != 1 || intents[0].Label() != "Worldwide" {
		t.Fatalf("intents() = %#v", intents)
	}
}
