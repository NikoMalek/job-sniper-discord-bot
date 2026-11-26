// Package data embeds the editable JSON catalogs and exposes typed loaders.
package data

import (
	"context"
	"embed"
	"fmt"
)

//go:embed *.json
var catalogFiles embed.FS

const (
	countriesFile                   = "countries.json"
	chileCitiesFile                 = "chiletrabajos_cities.json"
	computrabajoLocationsFile       = "computrabajo_locations.json"
	firstJobLocationsFile           = "firstjob_locations.json"
	keywordAliasesFile              = "keyword_aliases.json"
	blacklistKeywordAliasGroupsFile = "blacklist_keyword_alias_groups.json"
)

// Catalog holds every embedded catalog after parsing and validation.
type Catalog struct {
	countries       []Country
	countriesByCode map[string]Country
	countryAliases  map[string]string
	chileCities     []ChileanCity
	computrabajo    ComputrabajoCatalog
	firstJob        FirstJobCatalog
	keywordAliases  map[string][]string
	blacklistGroups [][]string
}

// Load reads, parses and validates all embedded JSON catalogs.
func Load(ctx context.Context) (*Catalog, error) {
	catalog := &Catalog{}

	files := []string{
		countriesFile,
		chileCitiesFile,
		computrabajoLocationsFile,
		firstJobLocationsFile,
		keywordAliasesFile,
		blacklistKeywordAliasGroupsFile,
	}
	for _, name := range files {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("load catalogs: %w", err)
		}

		raw, err := catalogFiles.ReadFile(name)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}

		switch name {
		case countriesFile:
			countries, aliases, err := parseCountries(raw)
			if err != nil {
				return nil, err
			}
			catalog.countries = countries
			catalog.countryAliases = aliases
			catalog.countriesByCode = make(map[string]Country, len(countries))
			for _, country := range countries {
				catalog.countriesByCode[country.Code] = country
			}
		case chileCitiesFile:
			catalog.chileCities, err = parseChileCities(raw)
		case computrabajoLocationsFile:
			catalog.computrabajo, err = parseComputrabajoCatalog(raw)
		case firstJobLocationsFile:
			catalog.firstJob, err = parseFirstJobCatalog(raw)
		case keywordAliasesFile:
			catalog.keywordAliases, err = parseKeywordAliases(raw)
		case blacklistKeywordAliasGroupsFile:
			catalog.blacklistGroups, err = parseBlacklistGroups(raw)
		}
		if err != nil {
			return nil, err
		}
	}

	return catalog, nil
}

// Countries returns a copy of the country catalog.
func (c *Catalog) Countries() []Country {
	return append([]Country(nil), c.countries...)
}

// CountryByCode looks up a country by its uppercase ISO code.
func (c *Catalog) CountryByCode(code string) (Country, bool) {
	country, ok := c.countriesByCode[upper(code)]
	return country, ok
}

// CountryAliases returns a copy of the raw alias-to-code mapping.
func (c *Catalog) CountryAliases() map[string]string {
	aliases := make(map[string]string, len(c.countryAliases))
	for alias, code := range c.countryAliases {
		aliases[alias] = code
	}
	return aliases
}

// ChileCities returns a copy of the Chilean city catalog.
func (c *Catalog) ChileCities() []ChileanCity {
	return append([]ChileanCity(nil), c.chileCities...)
}

// Computrabajo returns the Computrabajo location catalog.
func (c *Catalog) Computrabajo() ComputrabajoCatalog {
	return c.computrabajo
}

// FirstJob returns the FirstJob location catalog.
func (c *Catalog) FirstJob() FirstJobCatalog {
	return c.firstJob
}

// KeywordAliases returns a copy of the keyword alias mapping.
func (c *Catalog) KeywordAliases() map[string][]string {
	aliases := make(map[string][]string, len(c.keywordAliases))
	for keyword, variants := range c.keywordAliases {
		aliases[keyword] = append([]string(nil), variants...)
	}
	return aliases
}

// BlacklistKeywordAliasGroups returns a copy of the blacklist alias groups.
func (c *Catalog) BlacklistKeywordAliasGroups() [][]string {
	groups := make([][]string, len(c.blacklistGroups))
	for index, group := range c.blacklistGroups {
		groups[index] = append([]string(nil), group...)
	}
	return groups
}
