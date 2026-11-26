package data

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// FirstJobCatalog is the FirstJob location catalog.
type FirstJobCatalog struct {
	DefaultCountry string
	Countries      []FirstJobCountry
}

// FirstJobCountry is one country id and its region catalog.
type FirstJobCountry struct {
	Code                string
	Label               string
	CountryID           string
	Aliases             []string
	Regions             []FirstJobRegion
	CountriesCovered    []string
	CountryCodesCovered []string
	Note                string
}

// FirstJobRegion is one region id accepted by FirstJob's region[] filter.
type FirstJobRegion struct {
	ID      string
	Label   string
	Aliases []string
}

type firstJobPayload struct {
	DefaultCountry string `json:"default_country"`
	Countries      map[string]struct {
		Label     string   `json:"label"`
		CountryID string   `json:"country_id"`
		Aliases   []string `json:"aliases"`
		Regions   []struct {
			ID      string   `json:"id"`
			Label   string   `json:"label"`
			Aliases []string `json:"aliases"`
		} `json:"regions"`
		CountriesCovered    []string `json:"countries_covered"`
		CountryCodesCovered []string `json:"country_codes_covered"`
		Note                string   `json:"note"`
	} `json:"countries"`
}

func parseFirstJobCatalog(raw []byte) (FirstJobCatalog, error) {
	var payload firstJobPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return FirstJobCatalog{}, fmt.Errorf("parse %s: %w", firstJobLocationsFile, err)
	}

	catalog := FirstJobCatalog{
		DefaultCountry: upper(payload.DefaultCountry),
		Countries:      make([]FirstJobCountry, 0, len(payload.Countries)),
	}
	seenCountries := make(map[string]struct{}, len(payload.Countries))
	seenIDs := make(map[string]struct{}, len(payload.Countries))

	codes := make([]string, 0, len(payload.Countries))
	for code := range payload.Countries {
		codes = append(codes, code)
	}
	sort.Strings(codes)

	for _, rawCode := range codes {
		item := payload.Countries[rawCode]
		code := upper(rawCode)
		countryID := strings.TrimSpace(item.CountryID)
		if _, ok := seenCountries[code]; ok {
			return FirstJobCatalog{}, fmt.Errorf(
				"%s: duplicate country: %s", firstJobLocationsFile, code,
			)
		}
		if countryID == "" {
			return FirstJobCatalog{}, fmt.Errorf(
				"%s: invalid country_id: %q", firstJobLocationsFile, countryID,
			)
		}
		if _, ok := seenIDs[countryID]; ok {
			return FirstJobCatalog{}, fmt.Errorf(
				"%s: duplicate country_id: %q", firstJobLocationsFile, countryID,
			)
		}
		seenCountries[code] = struct{}{}
		seenIDs[countryID] = struct{}{}

		country := FirstJobCountry{
			Code:                code,
			Label:               item.Label,
			CountryID:           countryID,
			Aliases:             nonEmpty(item.Aliases),
			Regions:             make([]FirstJobRegion, 0, len(item.Regions)),
			CountriesCovered:    append([]string(nil), item.CountriesCovered...),
			CountryCodesCovered: make([]string, 0, len(item.CountryCodesCovered)),
			Note:                item.Note,
		}
		for _, codeCovered := range item.CountryCodesCovered {
			country.CountryCodesCovered = append(country.CountryCodesCovered, upper(codeCovered))
		}

		seenRegionIDs := make(map[string]struct{}, len(item.Regions))
		for _, region := range item.Regions {
			regionID := strings.TrimSpace(region.ID)
			if regionID == "" {
				return FirstJobCatalog{}, fmt.Errorf(
					"%s: invalid region id in %s: %q", firstJobLocationsFile, code, regionID,
				)
			}
			if _, ok := seenRegionIDs[regionID]; ok {
				return FirstJobCatalog{}, fmt.Errorf(
					"%s: duplicate region id in %s: %q", firstJobLocationsFile, code, regionID,
				)
			}
			seenRegionIDs[regionID] = struct{}{}
			country.Regions = append(country.Regions, FirstJobRegion{
				ID:      regionID,
				Label:   region.Label,
				Aliases: nonEmpty(region.Aliases),
			})
		}
		catalog.Countries = append(catalog.Countries, country)
	}

	if _, ok := seenCountries[catalog.DefaultCountry]; !ok {
		return FirstJobCatalog{}, fmt.Errorf(
			"%s: default_country %q is not in the catalog", firstJobLocationsFile, catalog.DefaultCountry,
		)
	}
	return catalog, nil
}
