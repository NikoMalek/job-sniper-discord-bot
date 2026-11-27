package notifier

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/NikoMalek/job-sniper-discord-bot/internal/job"
)

func notifierTestNow() time.Time {
	return time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
}

func TestBuildJobEmbedSingleSource(t *testing.T) {
	now := notifierTestNow()
	published := now.Add(-2 * time.Hour)
	record := job.Job{
		ID:                  "one",
		Title:               "  Python   Developer ",
		Company:             " Acme ",
		Location:            " Santiago, Chile ",
		URL:                 "https://example.com/job/one",
		Source:              "LinkedIn",
		PublishedAt:         "hace 2 horas",
		CompanyLogoURL:      "https://example.com/logo.png",
		Description:         " Primera  línea ",
		PublishedAtDatetime: &published,
	}

	embed, err := BuildJobEmbed(record, now)
	if err != nil {
		t.Fatalf("build embed: %v", err)
	}
	if embed.Type != "rich" {
		t.Errorf("embed type = %q, want rich", embed.Type)
	}
	if embed.Title != "Python Developer" {
		t.Errorf("title = %q", embed.Title)
	}
	if embed.URL != record.URL {
		t.Errorf("url = %q, want %q", embed.URL, record.URL)
	}
	if embed.Color != linkedInColor {
		t.Errorf("color = %d, want %d", embed.Color, linkedInColor)
	}
	if embed.Author == nil || embed.Author.Name != "Nueva vacante · LinkedIn" ||
		embed.Author.IconURL != linkedInIcon {
		t.Errorf("unexpected author: %+v", embed.Author)
	}
	wantDescription := "Primera línea… [Ver más...](https://example.com/job/one)" +
		"\n\n**Empresa**\nAcme\n\n**Ubicación**\nSantiago, Chile"
	if embed.Description != wantDescription {
		t.Errorf("description = %q, want %q", embed.Description, wantDescription)
	}
	if embed.Footer == nil ||
		embed.Footer.Text != "Fuente: LinkedIn • Publicada: hace 2 horas" {
		t.Errorf("unexpected footer: %+v", embed.Footer)
	}
	if embed.Thumbnail == nil || embed.Thumbnail.URL != record.CompanyLogoURL {
		t.Errorf("unexpected thumbnail: %+v", embed.Thumbnail)
	}
}

func TestBuildJobsEmbedSourceStyles(t *testing.T) {
	now := notifierTestNow()
	tests := []struct {
		source string
		color  int
		icon   string
	}{
		{"LinkedIn", linkedInColor, linkedInIcon},
		{"chiletrabajos", chileTrabajosColor, chileTrabajosIcon},
		{"Computrabajo", computrabajoColor, computrabajoIcon},
		{"Get on Board", getOnBoardColor, getOnBoardIcon},
		{"FirstJob", firstJobColor, firstJobIcon},
		{"Desconocido", blurpleColor, ""},
	}
	for _, test := range tests {
		t.Run(test.source, func(t *testing.T) {
			embed, err := BuildJobEmbed(job.Job{Source: test.source}, now)
			if err != nil {
				t.Fatalf("build embed: %v", err)
			}
			if embed.Color != test.color {
				t.Errorf("color = %d, want %d", embed.Color, test.color)
			}
			if embed.Author == nil || embed.Author.IconURL != test.icon {
				t.Errorf("unexpected author: %+v", embed.Author)
			}
		})
	}
}

func TestBuildJobsEmbedMultipleSources(t *testing.T) {
	now := notifierTestNow()
	published := now.Add(-3 * time.Hour)
	jobs := []job.Job{
		{
			ID: "one", Source: "LinkedIn", URL: "https://example.com/one",
			Title: "Developer", Company: "Acme", Location: "Santiago",
			Description: "Detalle", PublishedAtDatetime: &published,
		},
		{
			ID: "two", Source: "Get on Board", URL: "https://example.com/two",
		},
		{
			ID: "three", Source: "LinkedIn", URL: "https://example.com/three",
		},
	}

	embed, err := BuildJobsEmbed(jobs, now)
	if err != nil {
		t.Fatalf("build embed: %v", err)
	}
	if embed.Author == nil ||
		embed.Author.Name != "Nueva vacante · 2 portales" ||
		embed.Author.IconURL != "" {
		t.Errorf("unexpected author: %+v", embed.Author)
	}
	if embed.Color != linkedInColor {
		t.Errorf("color = %d, want %d", embed.Color, linkedInColor)
	}
	wantDescription := "Detalle… [Ver más...](https://example.com/one)" +
		"\n\n**Empresa**\nAcme\n\n**Ubicación**\nSantiago" +
		"\n\n**Disponible en**\n• [LinkedIn](https://example.com/one)" +
		"\n• [Get on Board](https://example.com/two)"
	if embed.Description != wantDescription {
		t.Errorf("description = %q, want %q", embed.Description, wantDescription)
	}
	wantFooter := "Fuentes: LinkedIn, Get on Board • Publicada: hace 3 horas"
	if embed.Footer == nil || embed.Footer.Text != wantFooter {
		t.Errorf("footer = %+v, want %q", embed.Footer, wantFooter)
	}
}

func TestBuildJobsEmbedFallbacksAndTruncation(t *testing.T) {
	now := notifierTestNow()
	if _, err := BuildJobsEmbed(nil, now); err != ErrNoJobs {
		t.Fatalf("empty group error = %v, want %v", err, ErrNoJobs)
	}
	if ErrNoJobs.Error() != "At least one job is required to build an embed." {
		t.Errorf("unexpected error text %q", ErrNoJobs.Error())
	}

	fallback, err := BuildJobEmbed(job.Job{
		Title:    " \t ",
		Company:  "\n ",
		Location: "  ",
		Source:   "  ",
		URL:      "",
	}, now)
	if err != nil {
		t.Fatalf("build fallback embed: %v", err)
	}
	if fallback.Title != titleFallback || fallback.URL != "" {
		t.Errorf("unexpected title/url: %q / %q", fallback.Title, fallback.URL)
	}
	wantDescription := "**Empresa**\nNo especificada\n\n**Ubicación**\nNo especificada"
	if fallback.Description != wantDescription {
		t.Errorf("description = %q, want %q", fallback.Description, wantDescription)
	}
	wantFooter := "Fuente: Portal de empleo • Publicada: Fecha no disponible"
	if fallback.Footer == nil || fallback.Footer.Text != wantFooter {
		t.Errorf("footer = %+v, want %q", fallback.Footer, wantFooter)
	}
	if fallback.Author == nil ||
		fallback.Author.Name != "Nueva vacante · Portal de empleo" ||
		fallback.Author.IconURL != "" {
		t.Errorf("unexpected fallback author: %+v", fallback.Author)
	}
	if fallback.Thumbnail != nil {
		t.Errorf("unexpected thumbnail: %+v", fallback.Thumbnail)
	}

	longURL := "https://example.com/" + strings.Repeat("u", 3950)
	suffix := "… [Ver más...](" + longURL + ")"
	available := embedDescriptionLimit - utf8.RuneCountInString(suffix)
	if available < 0 || available >= shortDescriptionLimit {
		t.Fatalf("test URL did not force truncation: available = %d", available)
	}
	record := job.Job{
		Source:      strings.Repeat("S", 300),
		Title:       strings.Repeat("T", 300),
		URL:         longURL,
		Description: strings.Repeat("x", available) + ", " + strings.Repeat("y", 10),
	}
	truncated, err := BuildJobEmbed(record, now)
	if err != nil {
		t.Fatalf("build truncated embed: %v", err)
	}
	if utf8.RuneCountInString(truncated.Title) != sourceLimit {
		t.Errorf("title length = %d, want %d", utf8.RuneCountInString(truncated.Title), sourceLimit)
	}
	wantPrefix := strings.Repeat("x", available) + "..." + suffix
	if !strings.HasPrefix(truncated.Description, wantPrefix) {
		t.Errorf(
			"description = %q, want prefix %q",
			truncated.Description, wantPrefix,
		)
	}
	if truncated.Author == nil ||
		utf8.RuneCountInString(truncated.Author.Name) !=
			sourceLimit+utf8.RuneCountInString("Nueva vacante · ") {
		t.Errorf("unexpected long author: %+v", truncated.Author)
	}
}

func TestShortDescriptionWithNegativeAvailable(t *testing.T) {
	url := "https://example.com/" + strings.Repeat("u", 3970)
	suffix := "… [Ver más...](" + url + ")"
	available := embedDescriptionLimit - utf8.RuneCountInString(suffix)
	if available >= 0 {
		t.Fatalf("test URL did not force a negative available length: %d", available)
	}
	description := strings.Repeat("a", 100) + strings.Repeat("Z", -available)
	want := strings.Repeat("a", 100) + "..." + suffix
	if got := shortDescription(job.Job{URL: url, Description: description}); got != want {
		t.Errorf("shortDescription() did not match Python negative slicing")
	}
}

func TestRelativePublished(t *testing.T) {
	now := notifierTestNow()
	tests := []struct {
		name string
		when time.Time
		want string
	}{
		{"seconds", now.Add(-59 * time.Second), "hace unos segundos"},
		{"one minute", now.Add(-time.Minute), "hace 1 minuto"},
		{"minutes", now.Add(-2 * time.Minute), "hace 2 minutos"},
		{"one hour", now.Add(-time.Hour), "hace 1 hora"},
		{"hours", now.Add(-11 * time.Hour), "hace 11 horas"},
		{"one day", now.Add(-24 * time.Hour), "hace 1 día"},
		{"days", now.AddDate(0, 0, -3), "hace 3 días"},
		{"future", now.Add(time.Hour), "hace unos segundos"},
		{
			"offset timezone",
			now.Add(-2 * time.Hour).In(time.FixedZone("UTC+3", 3*3600)),
			"hace 2 horas",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := relativePublished(test.when, now); got != test.want {
				t.Errorf("relativePublished() = %q, want %q", got, test.want)
			}
		})
	}
}
