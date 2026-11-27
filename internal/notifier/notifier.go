// Package notifier builds Discord embeds and delivers them without calling
// Discord directly from its pure helpers.
package notifier

import (
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/NikoMalek/job-sniper-discord-bot/internal/job"
	"github.com/bwmarrin/discordgo"
)

const (
	sourceFallback = "Portal de empleo"
	titleFallback  = "Vacante sin título"
	unspecified    = "No especificada"
	dateFallback   = "Fecha no disponible"

	sourceLimit           = 256
	titleLimit            = 256
	dateLimit             = 256
	fieldLimit            = 1024
	shortDescriptionLimit = 280
	embedDescriptionLimit = 4000
)

const (
	linkedInIcon      = "https://img.icons8.com/color/96/linkedin.png"
	chileTrabajosIcon = "https://s3.amazonaws.com/cht2/front/img/logo.png"
	computrabajoIcon  = "https://cl.computrabajo.com/c/img/logos/logoct-ogp.png"
	getOnBoardIcon    = "https://d2dgum4gsvdsrq.cloudfront.net/assets/" +
		"icon192-64920e83a21d9477c8d163577f6cd2f6691c9bcd0499e37325dcb1246709e3e8.png"
	firstJobIcon = "https://cdn.firstjob.me/asset-web/header_fj.png"
)

const (
	linkedInColor      = 10<<16 | 102<<8 | 194
	chileTrabajosColor = 31<<16 | 83<<8 | 114
	computrabajoColor  = 242<<16 | 107<<8 | 33
	getOnBoardColor    = 0<<16 | 147<<8 | 211
	firstJobColor      = 233<<16 | 64<<8 | 108
	blurpleColor       = 88<<16 | 101<<8 | 242
)

// ErrNoJobs is returned when an embed is requested for an empty group.
var ErrNoJobs = errors.New("At least one job is required to build an embed.")

// BuildJobsEmbed builds the non-experimental Discord embed for one job or a
// group of the same job found on several portals. now is injected so relative
// publication text is deterministic.
func BuildJobsEmbed(jobs []job.Job, now time.Time) (*discordgo.MessageEmbed, error) {
	if len(jobs) == 0 {
		return nil, ErrNoJobs
	}
	primary := jobs[0]
	sources := uniqueSources(jobs)
	source := sources[0]
	color, icon := sourceStyle(source)

	embed := &discordgo.MessageEmbed{
		Type:  discordgo.EmbedTypeRich,
		Title: display(primary.Title, titleFallback, titleLimit),
		URL:   primary.URL,
		Color: color,
	}
	if len(sources) == 1 {
		embed.Author = &discordgo.MessageEmbedAuthor{
			Name:    "Nueva vacante · " + source,
			IconURL: icon,
		}
	} else {
		embed.Author = &discordgo.MessageEmbedAuthor{
			Name: fmt.Sprintf("Nueva vacante · %d portales", len(sources)),
		}
	}

	descriptionParts := make([]string, 0, 4)
	if short := shortDescription(primary); short != "" {
		descriptionParts = append(descriptionParts, short)
	}
	descriptionParts = append(descriptionParts,
		"**Empresa**\n"+display(primary.Company, unspecified, fieldLimit),
		"**Ubicación**\n"+display(primary.Location, unspecified, fieldLimit),
	)
	if len(sources) > 1 {
		descriptionParts = append(descriptionParts,
			"**Disponible en**\n"+sourceLinks(jobs),
		)
	}
	embed.Description = strings.Join(descriptionParts, "\n\n")

	published := display(primary.PublishedAt, dateFallback, dateLimit)
	if primary.PublishedAtDatetime != nil {
		published = relativePublished(*primary.PublishedAtDatetime, now)
	}
	sourceLabel := "Fuente"
	if len(sources) > 1 {
		sourceLabel = "Fuentes"
	}
	embed.Footer = &discordgo.MessageEmbedFooter{
		Text: fmt.Sprintf(
			"%s: %s • Publicada: %s",
			sourceLabel, strings.Join(sources, ", "), published,
		),
	}
	if primary.CompanyLogoURL != "" {
		embed.Thumbnail = &discordgo.MessageEmbedThumbnail{
			URL: primary.CompanyLogoURL,
		}
	}
	return embed, nil
}

// BuildJobEmbed builds a non-experimental embed for a single job.
func BuildJobEmbed(record job.Job, now time.Time) (*discordgo.MessageEmbed, error) {
	return BuildJobsEmbed([]job.Job{record}, now)
}

func uniqueSources(jobs []job.Job) []string {
	sources := make([]string, 0, len(jobs))
	seen := make(map[string]struct{}, len(jobs))
	for _, record := range jobs {
		value := display(record.Source, sourceFallback, sourceLimit)
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		sources = append(sources, value)
	}
	return sources
}

func sourceLinks(jobs []job.Job) string {
	seen := make(map[string]struct{}, len(jobs))
	links := make([]string, 0, len(jobs))
	for _, record := range jobs {
		value := display(record.Source, sourceFallback, sourceLimit)
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		links = append(links, fmt.Sprintf("• [%s](%s)", value, record.URL))
	}
	return strings.Join(links, "\n")
}

func sourceStyle(source string) (int, string) {
	switch strings.ToLower(source) {
	case "linkedin":
		return linkedInColor, linkedInIcon
	case "chiletrabajos":
		return chileTrabajosColor, chileTrabajosIcon
	case "computrabajo":
		return computrabajoColor, computrabajoIcon
	case "get on board":
		return getOnBoardColor, getOnBoardIcon
	case "firstjob":
		return firstJobColor, firstJobIcon
	default:
		return blurpleColor, ""
	}
}

// display mirrors Python's whitespace collapse, fallback and character limit.
func display(value, fallback string, limit int) string {
	cleaned := strings.Join(strings.Fields(value), " ")
	if cleaned == "" {
		return fallback
	}
	return truncateRunes(cleaned, limit)
}

func truncateRunes(value string, limit int) string {
	if limit < 0 {
		limit = 0
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}

// sliceRunes reproduces Python slicing, including negative indices, for the
// overlong-description branch.
func sliceRunes(value string, index int) string {
	runes := []rune(value)
	if index >= 0 {
		if index > len(runes) {
			index = len(runes)
		}
		return string(runes[:index])
	}
	index += len(runes)
	if index < 0 {
		index = 0
	}
	return string(runes[:index])
}

func shortDescription(record job.Job) string {
	description := display(record.Description, "", shortDescriptionLimit)
	if description == "" {
		return ""
	}
	suffix := "… [Ver más...](" + record.URL + ")"
	available := embedDescriptionLimit - utf8.RuneCountInString(suffix)
	if utf8.RuneCountInString(description) > available {
		description = strings.TrimRight(
			sliceRunes(description, available), " .,;:",
		) + "..."
	}
	return description + suffix
}

func relativePublished(value, now time.Time) string {
	seconds := int64(0)
	if delta := now.UTC().Sub(value.UTC()); delta > 0 {
		seconds = int64(delta / time.Second)
	}
	if seconds < 60 {
		return "hace unos segundos"
	}
	if minutes := seconds / 60; minutes < 60 {
		return fmt.Sprintf("hace %d %s", minutes, plural(minutes, "minuto"))
	}
	if hours := seconds / 3600; hours < 24 {
		return fmt.Sprintf("hace %d %s", hours, plural(hours, "hora"))
	}
	days := seconds / 86400
	return fmt.Sprintf("hace %d %s", days, plural(days, "día"))
}

func plural(amount int64, singular string) string {
	if amount == 1 {
		return singular
	}
	return singular + "s"
}
