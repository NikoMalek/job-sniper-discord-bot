package bot

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/NikoMalek/job-sniper-discord-bot/internal/locations"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/scrapers"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/store"
)

func responseContent(t *testing.T, response Response) string {
	t.Helper()
	if response.Embed != nil {
		t.Fatalf("expected text response, got embed %q", response.Embed.Title)
	}
	return response.Content
}

func TestCleanAndFormatAge(t *testing.T) {
	got, err := clean("  python \t backend  ", "La palabra clave")
	if err != nil || got != "python backend" {
		t.Fatalf("clean() = %q, %v", got, err)
	}
	if _, err := clean("   ", "La empresa"); err == nil || err.Error() != "La empresa no puede estar vacío." {
		t.Fatalf("clean(empty) error = %v", err)
	}
	long := strings.Repeat("a", 250)
	got, err = clean(long, "La palabra clave")
	if err != nil || len(got) != 200 {
		t.Fatalf("clean(long) length = %d, %v", len(got), err)
	}

	ages := []struct {
		hours float64
		want  string
	}{
		{10.0 / 60.0, "10 minutos"},
		{1.0 / 60.0, "1 minuto"},
		{0.5, "30 minutos"},
		{1, "1 hora"},
		{8, "8 horas"},
		{8.5, "8 horas 30 min"},
	}
	for _, age := range ages {
		if got := formatAge(age.hours); got != age.want {
			t.Errorf("formatAge(%v) = %q, want %q", age.hours, got, age.want)
		}
	}
}

func TestSetChannel(t *testing.T) {
	st := &fakeStore{}
	admin := newTestAdmin(t, st, basicRegistry())
	response, err := admin.SetChannel(context.Background(), 42, ChannelRef{ID: 99})
	if err != nil {
		t.Fatalf("SetChannel() error = %v", err)
	}
	if want := "Las vacantes se enviarán en <#99>."; responseContent(t, response) != want {
		t.Fatalf("SetChannel() = %q, want %q", response.Content, want)
	}
	if st.config.ChannelID == nil || *st.config.ChannelID != 99 {
		t.Fatalf("channel id = %v, want 99", st.config.ChannelID)
	}
}

func TestSetLocationSuccessAndInvalid(t *testing.T) {
	st := &fakeStore{config: store.GuildMonitorConfig{
		Portals: []string{"linkedin", "chiletrabajos"},
	}}
	registry := basicRegistry()
	chile := registry.Get("chiletrabajos").(*fakeScraper)
	chile.planFunc = func(intent *locations.LocationIntent) scrapers.PortalPlan {
		if intent == nil || intent.CountryCode != "CL" {
			return scrapers.PortalPlan{Reason: "sin cobertura."}
		}
		return scrapers.PortalPlan{Supported: true, SearchLocation: intent.Label()}
	}
	admin := newTestAdmin(t, st, registry)

	invalid, err := admin.SetLocation(context.Background(), 1, "marte")
	if err != nil {
		t.Fatalf("SetLocation(marte) error = %v", err)
	}
	if responseContent(t, invalid) == "" {
		t.Fatal("SetLocation(marte) returned an empty error message")
	}

	response, err := admin.SetLocation(context.Background(), 1, "Worldwide")
	if err != nil {
		t.Fatalf("SetLocation(Worldwide) error = %v", err)
	}
	want := "Ubicación configurada: **Worldwide**\n\n" +
		"• **LinkedIn** → Worldwide\n• **ChileTrabajos** → pausado (⏸️)\n\n" +
		"**ChileTrabajos**: sin cobertura."
	if got := responseContent(t, response); got != want {
		t.Fatalf("SetLocation() =\n%q\nwant\n%q", got, want)
	}
}

func TestSetLocationWithNoSupportingPortal(t *testing.T) {
	st := &fakeStore{config: store.GuildMonitorConfig{Portals: []string{"chiletrabajos"}}}
	admin := newTestAdmin(t, st, basicRegistry())
	response, err := admin.SetLocation(context.Background(), 1, "España")
	if err != nil {
		t.Fatalf("SetLocation() error = %v", err)
	}
	got := responseContent(t, response)
	want := "⚠️ Ningún portal activo cubre esta ubicación. Activa LinkedIn o cambia la ubicación."
	if !strings.HasSuffix(got, want) {
		t.Fatalf("SetLocation() = %q, want suffix %q", got, want)
	}
}

func TestAddLocationDuplicateAndLimit(t *testing.T) {
	st := &fakeStore{config: store.GuildMonitorConfig{
		Locations: []string{"Worldwide", "Chile", "Remoto"},
	}}
	admin := newTestAdmin(t, st, basicRegistry())
	response, err := admin.AddLocation(context.Background(), 1, "worldwide")
	if err != nil {
		t.Fatalf("AddLocation(duplicate) error = %v", err)
	}
	if got, want := responseContent(t, response), "**Worldwide** ya estaba configurada."; got != want {
		t.Fatalf("duplicate = %q, want %q", got, want)
	}
	response, err = admin.AddLocation(context.Background(), 1, "España")
	if err != nil {
		t.Fatalf("AddLocation(limit) error = %v", err)
	}
	if got := responseContent(t, response); !strings.Contains(got, "Ya tienes **3** ubicaciones") {
		t.Fatalf("limit response = %q", got)
	}
}

func TestAddLocationSuccessWithInferredCity(t *testing.T) {
	st := &fakeStore{config: store.GuildMonitorConfig{Locations: []string{"Chile"}}}
	admin := newTestAdmin(t, st, basicRegistry())
	response, err := admin.AddLocation(context.Background(), 1, "Osorno")
	if err != nil {
		t.Fatalf("AddLocation() error = %v", err)
	}
	got := responseContent(t, response)
	if !strings.HasPrefix(got, "Ubicación agregada: **Osorno, Chile** (2/3).") {
		t.Fatalf("AddLocation() = %q", got)
	}
	if !strings.Contains(got, "Interpreté **Osorno** como ciudad de Chile.") {
		t.Fatalf("AddLocation() missing inferred city note: %q", got)
	}
}

func TestRemoveLocation(t *testing.T) {
	st := &fakeStore{config: store.GuildMonitorConfig{
		Locations: []string{"Worldwide"},
		Portals:   []string{"linkedin"},
	}}
	admin := newTestAdmin(t, st, basicRegistry())
	response, err := admin.RemoveLocation(context.Background(), 1, "Chile")
	if err != nil {
		t.Fatalf("RemoveLocation(missing) error = %v", err)
	}
	if got, want := responseContent(t, response), "No encontré **Chile** entre tus ubicaciones."; got != want {
		t.Fatalf("missing = %q, want %q", got, want)
	}
	response, err = admin.RemoveLocation(context.Background(), 1, "worldwide")
	if err != nil {
		t.Fatalf("RemoveLocation() error = %v", err)
	}
	got := responseContent(t, response)
	if !strings.HasPrefix(got, "Ubicación eliminada: **Worldwide**.") {
		t.Fatalf("RemoveLocation() = %q", got)
	}
	if !strings.Contains(got, "No quedan ubicaciones guardadas; se usará **Worldwide** por defecto.") {
		t.Fatalf("RemoveLocation() missing last-location note: %q", got)
	}
}

func TestRemoveLocationAutocomplete(t *testing.T) {
	st := &fakeStore{config: store.GuildMonitorConfig{
		Locations: []string{"Santiago, Chile", "Worldwide", "Remoto, España"},
	}}
	admin := newTestAdmin(t, st, basicRegistry())
	choices, err := admin.RemoveLocationAutocomplete(context.Background(), 7, "santiago")
	if err != nil {
		t.Fatalf("autocomplete error = %v", err)
	}
	want := []Choice{{Name: "Santiago, Chile", Value: "Santiago, Chile"}}
	if !reflect.DeepEqual(choices, want) {
		t.Fatalf("choices = %#v, want %#v", choices, want)
	}
	choices, err = admin.RemoveLocationAutocomplete(context.Background(), 0, "")
	if err != nil || len(choices) != 0 {
		t.Fatalf("guildless autocomplete = %#v, %v", choices, err)
	}
}

func TestSetMaxAge(t *testing.T) {
	admin := newTestAdmin(t, &fakeStore{}, basicRegistry())
	response, err := admin.SetMaxAge(context.Background(), 1, 0.01)
	if err != nil {
		t.Fatalf("SetMaxAge(invalid) error = %v", err)
	}
	if got, want := responseContent(t, response), "La antigüedad debe estar entre 10 minutos y 24 horas (por ejemplo 0,5 = 30 minutos, 8 = 8 horas)."; got != want {
		t.Fatalf("invalid = %q, want %q", got, want)
	}
	response, err = admin.SetMaxAge(context.Background(), 1, 8.5)
	if err != nil {
		t.Fatalf("SetMaxAge() error = %v", err)
	}
	if got, want := responseContent(t, response), "Buscaré vacantes publicadas en los últimos **8 horas 30 min**."; got != want {
		t.Fatalf("valid = %q, want %q", got, want)
	}
}

func TestAddAndRemoveKeyword(t *testing.T) {
	st := &fakeStore{}
	admin := newTestAdmin(t, st, basicRegistry())
	response, err := admin.AddKeyword(context.Background(), 1, " Python , python , backend ")
	if err != nil {
		t.Fatalf("AddKeyword() error = %v", err)
	}
	if got, want := responseContent(t, response), "Palabras clave agregadas: **Python, backend**."; got != want {
		t.Fatalf("AddKeyword() = %q, want %q", got, want)
	}
	response, err = admin.AddKeyword(context.Background(), 1, "python, backend")
	if err != nil {
		t.Fatalf("AddKeyword(duplicate) error = %v", err)
	}
	if got, want := responseContent(t, response), "Todas las palabras clave ya estaban configuradas."; got != want {
		t.Fatalf("duplicate = %q, want %q", got, want)
	}
	response, err = admin.AddKeyword(context.Background(), 1, "   ")
	if err != nil {
		t.Fatalf("AddKeyword(empty) error = %v", err)
	}
	if got, want := responseContent(t, response), "Debes indicar al menos una palabra clave."; got != want {
		t.Fatalf("empty = %q, want %q", got, want)
	}
	response, err = admin.RemoveKeyword(context.Background(), 1, "PYTHON, missing")
	if err != nil {
		t.Fatalf("RemoveKeyword() error = %v", err)
	}
	if got, want := responseContent(t, response), "Palabras clave eliminadas: **PYTHON**."; got != want {
		t.Fatalf("RemoveKeyword() = %q, want %q", got, want)
	}
}

func TestKeywordAutocomplete(t *testing.T) {
	st := &fakeStore{config: store.GuildMonitorConfig{Keywords: []string{"Python", "Backend", "java"}}}
	admin := newTestAdmin(t, st, basicRegistry())
	choices, err := admin.RemoveKeywordAutocomplete(context.Background(), 1, "  PY ")
	if err != nil {
		t.Fatalf("autocomplete error = %v", err)
	}
	want := []Choice{{Name: "Python", Value: "Python"}}
	if !reflect.DeepEqual(choices, want) {
		t.Fatalf("choices = %#v, want %#v", choices, want)
	}
}

func TestBlacklistCompanies(t *testing.T) {
	st := &fakeStore{}
	admin := newTestAdmin(t, st, basicRegistry())
	response, err := admin.AddBlacklistCompany(context.Background(), 1, "  Acme  ")
	if err != nil {
		t.Fatalf("AddBlacklistCompany() error = %v", err)
	}
	if got, want := responseContent(t, response), "Las vacantes de **Acme** serán ignoradas."; got != want {
		t.Fatalf("add = %q, want %q", got, want)
	}
	response, err = admin.AddBlacklistCompany(context.Background(), 1, "Acme")
	if err != nil {
		t.Fatalf("AddBlacklistCompany(duplicate) error = %v", err)
	}
	if got, want := responseContent(t, response), "Esa empresa ya estaba en la blacklist."; got != want {
		t.Fatalf("duplicate = %q, want %q", got, want)
	}
	response, err = admin.RemoveBlacklistCompany(context.Background(), 1, "ACME")
	if err != nil {
		t.Fatalf("RemoveBlacklistCompany() error = %v", err)
	}
	if got, want := responseContent(t, response), "Empresa eliminada de la blacklist."; got != want {
		t.Fatalf("remove = %q, want %q", got, want)
	}
	response, err = admin.RemoveBlacklistCompany(context.Background(), 1, "ACME")
	if err != nil {
		t.Fatalf("RemoveBlacklistCompany(missing) error = %v", err)
	}
	if got, want := responseContent(t, response), "No encontré esa empresa."; got != want {
		t.Fatalf("missing = %q, want %q", got, want)
	}
}

func TestAddAndRemoveBlacklistKeyword(t *testing.T) {
	st := &fakeStore{}
	admin := newTestAdmin(t, st, basicRegistry())
	response, err := admin.AddBlacklistKeyword(context.Background(), 1, "Práctica", true)
	if err != nil {
		t.Fatalf("AddBlacklistKeyword() error = %v", err)
	}
	got := responseContent(t, response)
	if !strings.HasPrefix(got, "Palabras excluidas en títulos agregadas: **Práctica**.") {
		t.Fatalf("add = %q", got)
	}
	if !strings.Contains(got, "Alias comunes incluidos automáticamente:") {
		t.Fatalf("add missing aliases: %q", got)
	}
	response, err = admin.AddBlacklistKeyword(context.Background(), 1, "práctica", true)
	if err != nil {
		t.Fatalf("AddBlacklistKeyword(duplicate) error = %v", err)
	}
	if got, want := responseContent(t, response), "Todas esas palabras ya estaban en la lista de exclusión."; got != want {
		t.Fatalf("duplicate = %q, want %q", got, want)
	}
	response, err = admin.RemoveBlacklistKeyword(context.Background(), 1, "PRÁCTICA")
	if err != nil {
		t.Fatalf("RemoveBlacklistKeyword() error = %v", err)
	}
	if !strings.HasPrefix(responseContent(t, response), "Palabras excluidas eliminadas: **PRÁCTICA**.") {
		t.Fatalf("remove = %q", response.Content)
	}
}

func TestErrorResponseTexts(t *testing.T) {
	admin := newTestAdmin(t, &fakeStore{}, basicRegistry())
	if got, want := responseContent(t, admin.ErrorResponse(ErrPermissionDenied)), "No tienes permiso para usar este comando."; got != want {
		t.Fatalf("permission error = %q, want %q", got, want)
	}
	want := "No se pudo completar el comando. Revisa los logs del bot."
	if got := responseContent(t, admin.ErrorResponse(context.Canceled)); got != want {
		t.Fatalf("generic error = %q, want %q", got, want)
	}
}

func TestLatencyIsInjected(t *testing.T) {
	st := &fakeStore{config: store.GuildMonitorConfig{MaxAgeHours: 24}}
	admin, err := NewAdmin(context.Background(), AdminOptions{
		Store:    st,
		Registry: basicRegistry(),
		Latency:  func() time.Duration { return 250 * time.Millisecond },
	})
	if err != nil {
		t.Fatalf("NewAdmin() error = %v", err)
	}
	admin.interval = 5
	response, err := admin.Status(context.Background(), 1)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !strings.Contains(responseContent(t, response), "**Latencia:** 250 ms") {
		t.Fatalf("Status() = %q", response.Content)
	}
}
