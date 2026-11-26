package data

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ComputrabajoCatalog is the Computrabajo location catalog.
type ComputrabajoCatalog struct {
	SchemaVersion  int
	DefaultCountry string
	Note           string
	Countries      []ComputrabajoCountry
}

// ComputrabajoCountry is one country subdomain and its locations.
type ComputrabajoCountry struct {
	Code      string
	Label     string
	Subdomain string
	Aliases   []string
	Locations []ComputrabajoLocation
}

// ComputrabajoLocation is one exact slug accepted by Computrabajo.
type ComputrabajoLocation struct {
	Slug    string
	Label   string
	Region  string
	City    string
	Aliases []string
}

type computrabajoPayload struct {
	SchemaVersion  int    `json:"schema_version"`
	DefaultCountry string `json:"default_country"`
	Note           string `json:"note"`
	Countries      map[string]struct {
		Label     string   `json:"label"`
		Subdomain string   `json:"subdomain"`
		Aliases   []string `json:"aliases"`
		Locations []struct {
			Slug    string   `json:"slug"`
			Label   string   `json:"label"`
			Region  string   `json:"region"`
			City    string   `json:"city"`
			Aliases []string `json:"aliases"`
		} `json:"locations"`
	} `json:"countries"`
}

func parseComputrabajoCatalog(raw []byte) (ComputrabajoCatalog, error) {
	var payload computrabajoPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ComputrabajoCatalog{}, fmt.Errorf("parse %s: %w", computrabajoLocationsFile, err)
	}

	catalog := ComputrabajoCatalog{
		SchemaVersion:  payload.SchemaVersion,
		DefaultCountry: upper(payload.DefaultCountry),
		Note:           payload.Note,
		Countries:      make([]ComputrabajoCountry, 0, len(payload.Countries)),
	}
	seenCountries := make(map[string]struct{}, len(payload.Countries))
	seenSubdomains := make(map[string]struct{}, len(payload.Countries))

	codes := make([]string, 0, len(payload.Countries))
	for code := range payload.Countries {
		codes = append(codes, code)
	}
	sort.Strings(codes)

	for _, rawCode := range codes {
		item := payload.Countries[rawCode]
		code := upper(rawCode)
		subdomain := strings.ToLower(strings.TrimSpace(item.Subdomain))
		if _, ok := seenCountries[code]; ok {
			return ComputrabajoCatalog{}, fmt.Errorf(
				"%s: duplicate country: %s", computrabajoLocationsFile, code,
			)
		}
		if subdomain == "" || strings.Contains(subdomain, ".") {
			return ComputrabajoCatalog{}, fmt.Errorf(
				"%s: invalid subdomain: %q", computrabajoLocationsFile, subdomain,
			)
		}
		if _, ok := seenSubdomains[subdomain]; ok {
			return ComputrabajoCatalog{}, fmt.Errorf(
				"%s: duplicate subdomain: %q", computrabajoLocationsFile, subdomain,
			)
		}
		seenCountries[code] = struct{}{}
		seenSubdomains[subdomain] = struct{}{}

		country := ComputrabajoCountry{
			Code:      code,
			Label:     item.Label,
			Subdomain: subdomain,
			Aliases:   nonEmpty(item.Aliases),
			Locations: make([]ComputrabajoLocation, 0, len(item.Locations)),
		}
		seenSlugs := make(map[string]struct{}, len(item.Locations))
		for _, location := range item.Locations {
			slug := strings.TrimSpace(location.Slug)
			if slug == "" {
				return ComputrabajoCatalog{}, fmt.Errorf(
					"%s: invalid location slug in %s: %q", computrabajoLocationsFile, code, slug,
				)
			}
			if _, ok := seenSlugs[slug]; ok {
				return ComputrabajoCatalog{}, fmt.Errorf(
					"%s: duplicate location slug in %s: %q", computrabajoLocationsFile, code, slug,
				)
			}
			seenSlugs[slug] = struct{}{}
			country.Locations = append(country.Locations, ComputrabajoLocation{
				Slug:    slug,
				Label:   location.Label,
				Region:  location.Region,
				City:    location.City,
				Aliases: nonEmpty(location.Aliases),
			})
		}
		catalog.Countries = append(catalog.Countries, country)
	}

	if _, ok := seenCountries[catalog.DefaultCountry]; !ok {
		return ComputrabajoCatalog{}, fmt.Errorf(
			"%s: default_country %q is not in the catalog", computrabajoLocationsFile, catalog.DefaultCountry,
		)
	}
	return catalog, nil
}

func nonEmpty(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}
