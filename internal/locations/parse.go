package locations

import (
	"errors"
	"fmt"
	"strings"

	"github.com/NikoMalek/job-sniper-discord-bot/internal/data"
)

// Parse parses a location using the embedded default catalog.
func Parse(value string) (LocationIntent, error) {
	parser, err := defaultParser()
	if err != nil {
		return LocationIntent{}, err
	}
	return parser.Parse(value)
}

// ParseOrNone returns nil when the value is not a recognized location.
func ParseOrNone(value string) (*LocationIntent, error) {
	parser, err := defaultParser()
	if err != nil {
		return nil, err
	}
	return parser.ParseOrNone(value)
}

// CountryFor resolves a country by name, code or alias using the default catalog.
func CountryFor(value string) (data.Country, bool) {
	parser, err := defaultParser()
	if err != nil {
		return data.Country{}, false
	}
	return parser.CountryFor(value)
}

// ChileCities returns a copy of the embedded Chilean city catalog.
func ChileCities() []data.ChileanCity {
	parser, err := defaultParser()
	if err != nil {
		return nil
	}
	return parser.ChileCities()
}

// ChileCitiesByRegion groups the embedded Chilean city catalog by region.
func ChileCitiesByRegion() map[string][]data.ChileanCity {
	parser, err := defaultParser()
	if err != nil {
		return map[string][]data.ChileanCity{}
	}
	return parser.ChileCitiesByRegion()
}

// Chile returns the Chile country record from the default catalog.
func Chile() (data.Country, bool) {
	parser, err := defaultParser()
	if err != nil {
		return data.Country{}, false
	}
	return parser.CountryByCode("CL")
}

// Parse parses a location using this parser's catalog.
func (p *Parser) Parse(value string) (LocationIntent, error) {
	raw := strings.Join(strings.Fields(value), " ")
	parts := make([]string, 0, 2)
	for _, part := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	if raw == "" || len(parts) == 0 {
		return LocationIntent{}, emptyLocationError()
	}

	if len(parts) == 1 {
		return p.parseSingle(raw, parts[0])
	}
	return p.parseMultiple(raw, parts)
}

// ParseOrNone returns nil when the value is not a recognized location.
func (p *Parser) ParseOrNone(value string) (*LocationIntent, error) {
	intent, err := p.Parse(value)
	if err != nil {
		var locationErr *LocationError
		if asLocationError(err, &locationErr) {
			return nil, nil
		}
		return nil, err
	}
	return &intent, nil
}

func (p *Parser) parseSingle(raw, part string) (LocationIntent, error) {
	token := Normalize(part)

	if contains(WorldwideWords, token) {
		return LocationIntent{Raw: raw, Kind: "worldwide"}, nil
	}
	if contains(RemoteWords, token) {
		return LocationIntent{Raw: raw, Kind: "remote"}, nil
	}
	if contains(UnknownRemoteWords, token) {
		return LocationIntent{}, unknownRemoteError(part)
	}
	if country, ok := p.CountryFor(part); ok {
		return p.newIntent(raw, "country", country.Code, "", false), nil
	}
	if city, ok := p.cityAliases[token]; ok {
		return p.knownCityIntent(raw, "CL", city, true), nil
	}
	return LocationIntent{}, unknownLocationError(part)
}

func (p *Parser) parseMultiple(raw string, parts []string) (LocationIntent, error) {
	first, last := parts[0], parts[len(parts)-1]
	country, isCountry := p.CountryFor(last)

	if !isCountry {
		firstCountry, firstIsCountry := p.CountryFor(first)
		if firstIsCountry && len(parts) == 2 {
			return p.cityIntent(raw, firstCountry, last, false), nil
		}
		token := Normalize(first)
		if contains(RemoteWords, token) || contains(UnknownRemoteWords, token) {
			return LocationIntent{}, badRemoteCountryError(last)
		}
		return LocationIntent{}, badCountryError(last)
	}

	firstToken := Normalize(first)
	if contains(WorldwideWords, firstToken) {
		return LocationIntent{}, worldwideWithCountryError()
	}
	if contains(RemoteWords, firstToken) {
		return p.newIntent(raw, "remote", country.Code, "", false), nil
	}
	return p.cityIntent(raw, country, first, false), nil
}

func (p *Parser) cityIntent(raw string, country data.Country, cityText string, inferred bool) LocationIntent {
	if country.Code == "CL" {
		if city, ok := p.cityAliases[Normalize(cityText)]; ok {
			return p.knownCityIntent(raw, country.Code, city, inferred)
		}
	}
	intent := p.newIntent(raw, "city", country.Code, displayCase(cityText), inferred)
	return intent
}

func (p *Parser) knownCityIntent(raw, countryCode string, city cityMatch, inferred bool) LocationIntent {
	intent := p.newIntent(raw, "city", countryCode, city.display, inferred)
	intent.CityKnown = true
	intent.CityAlias = Normalize(city.display) != Normalize(city.city.Name)
	intent.ChileBucket = city.city.Bucket
	intent.ChileBucketCity = city.city.Name
	return intent
}

func (p *Parser) newIntent(raw, kind, countryCode, city string, inferred bool) LocationIntent {
	intent := LocationIntent{
		Raw:             raw,
		Kind:            kind,
		CountryCode:     countryCode,
		City:            city,
		InferredCountry: inferred,
	}
	if country, ok := p.CountryByCode(countryCode); ok {
		countryCopy := country
		intent.country = &countryCopy
	}
	return intent
}

func contains(values []string, token string) bool {
	for _, value := range values {
		if value == token {
			return true
		}
	}
	return false
}

func asLocationError(err error, target **LocationError) bool {
	return errors.As(err, target)
}

func emptyLocationError() error {
	return &LocationError{message: "Indica una ubicación, por ejemplo `Chile` o `Remoto`."}
}

func unknownRemoteError(part string) error {
	return &LocationError{message: fmt.Sprintf(
		"No reconocí **%s** como modalidad. "+
			"Para buscar remoto escribe `Remoto` o `Remoto, país`; "+
			"para cualquier modalidad en todo el mundo, `Worldwide`.",
		part,
	)}
}

func unknownLocationError(part string) error {
	return &LocationError{message: fmt.Sprintf(
		"No reconocí **%s** como país ni como ciudad chilena.\n"+
			"Usa `ciudad, país` (ej. `Río Bueno, Chile`), `país` "+
			"(ej. `Chile`), `Remoto` o `Worldwide`.\n"+
			LocationFormatHelp,
		part,
	)}
}

func badRemoteCountryError(last string) error {
	return &LocationError{message: fmt.Sprintf(
		"No reconocí **%s** como país. "+
			"Usa `Remoto, país` con un país real (ej. `Remoto, Chile`) "+
			"o solo `Remoto`.",
		last,
	)}
}

func badCountryError(last string) error {
	return &LocationError{message: fmt.Sprintf(
		"No reconocí **%s** como país.\n"+
			"Usa `ciudad, país` (ej. `Madrid, España`) o `país` "+
			"(ej. `España`). Si es una ciudad chilena, escribe "+
			"`ciudad, Chile`.",
		last,
	)}
}

func worldwideWithCountryError() error {
	return &LocationError{message: "Para buscar en todo el mundo escribe solo `Worldwide`. " +
		"Si quieres limitarlo a un país, usa `País` (ej. `Chile`)."}
}
