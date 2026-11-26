// Package locations turns user-supplied location text into portal-neutral intents.
package locations

import (
	"context"
	"fmt"
	"sync"

	"github.com/NikoMalek/job-sniper-discord-bot/internal/data"
)

// MaxLocations is the maximum number of locations accepted per guild.
const MaxLocations = 3

// LocationFormatHelp is the exact user-facing help text for invalid locations.
const LocationFormatHelp = `Formatos válidos:
• ` + "`Worldwide`" + ` — cualquier modalidad, sin filtro geográfico
• ` + "`Remoto`" + ` — solo ofertas remotas en todo el mundo
• ` + "`Remoto, país`" + ` — remoto en un país (ej. ` + "`Remoto, Chile`" + `)
• ` + "`País`" + ` — todo el país (ej. ` + "`Chile`, `España`" + `)
• ` + "`Ciudad, país`" + ` — ej. ` + "`Santiago, Chile`, `Madrid, España`" + `
También acepto una ` + "`ciudad`" + ` sola si es chilena conocida, pero el formato recomendado es ` + "`ciudad, país`" + `.`

var (
	// WorldwideWords matches every accepted worldwide spelling.
	WorldwideWords = []string{"todo el mundo", "worldwide", "anywhere", "cualquier lugar", "global"}
	// RemoteWords matches every accepted remote spelling.
	RemoteWords = []string{"remoto", "remote", "teletrabajo", "home office"}
	// UnknownRemoteWords are near misses that get a dedicated error message.
	UnknownRemoteWords = []string{"online", "virtual", "home", "remotamente"}
)

// ChileanCity is re-exported from the embedded catalog for scraper use.
type ChileanCity = data.ChileanCity

// LocationError is raised when a location string does not match the standard.
type LocationError struct {
	message string
}

func (e *LocationError) Error() string {
	return e.message
}

// LocationIntent is a parsed location plus how it maps to each portal.
type LocationIntent struct {
	Raw             string
	Kind            string // "worldwide", "remote", "country" or "city"
	CountryCode     string // empty means no country
	City            string
	InferredCountry bool
	CityKnown       bool
	CityAlias       bool
	ChileBucket     string
	ChileBucketCity string

	country *data.Country
}

// Country returns the resolved country, if any.
func (i LocationIntent) Country() *data.Country {
	if i.country != nil {
		return i.country
	}
	if i.CountryCode == "" {
		return nil
	}
	parser, err := defaultParser()
	if err != nil {
		return nil
	}
	country, ok := parser.CountryByCode(i.CountryCode)
	if !ok {
		return nil
	}
	return &country
}

// IsChile reports whether the intent is explicitly Chilean.
func (i LocationIntent) IsChile() bool {
	return i.CountryCode == "CL"
}

// Label returns the canonical display label for the intent.
func (i LocationIntent) Label() string {
	switch i.Kind {
	case "worldwide":
		return "Worldwide"
	case "remote":
		if i.Country() == nil {
			return "Remoto"
		}
		return "Remoto, " + i.Country().NameES
	case "country":
		if i.Country() == nil {
			return i.City
		}
		return i.Country().NameES
	}

	if i.Country() == nil {
		return i.City
	}
	countryName := i.Country().NameES
	suffix := "de " + countryName
	if len([]rune(i.City)) >= len([]rune(suffix)) &&
		equalFoldSuffix(i.City, suffix) {
		return i.City
	}
	return i.City + ", " + countryName
}

func equalFoldSuffix(value, suffix string) bool {
	valueRunes := []rune(value)
	suffixRunes := []rune(suffix)
	if len(suffixRunes) > len(valueRunes) {
		return false
	}
	start := len(valueRunes) - len(suffixRunes)
	for index, r := range suffixRunes {
		if toLowerRune(valueRunes[start+index]) != toLowerRune(r) {
			return false
		}
	}
	return true
}

type cityMatch struct {
	display string
	city    data.ChileanCity
}

// Parser parses locations against a specific catalog.
type Parser struct {
	countriesByCode map[string]data.Country
	countryAliases  map[string]string
	cityAliases     map[string]cityMatch
	chileCities     []data.ChileanCity
}

// NewParser builds a location parser from a loaded catalog.
func NewParser(catalog *data.Catalog) (*Parser, error) {
	if catalog == nil {
		return nil, fmt.Errorf("build location parser: nil catalog")
	}

	parser := &Parser{
		countriesByCode: make(map[string]data.Country),
		countryAliases:  make(map[string]string),
		cityAliases:     make(map[string]cityMatch),
		chileCities:     catalog.ChileCities(),
	}

	countries := catalog.Countries()
	for _, country := range countries {
		parser.countriesByCode[country.Code] = country
	}
	for _, country := range countries {
		parser.countryAliases[Normalize(country.NameES)] = country.Code
		parser.countryAliases[Normalize(country.NameEN)] = country.Code
		parser.countryAliases[Normalize(country.Code)] = country.Code
	}
	rawAliases := catalog.CountryAliases()
	for _, code := range sortedKeys(rawAliases) {
		parser.countryAliases[Normalize(code)] = rawAliases[code]
	}
	if _, ok := parser.CountryByCode("CL"); !ok {
		return nil, fmt.Errorf("build location parser: CL country is missing")
	}

	for _, city := range parser.chileCities {
		for _, candidate := range append([]string{city.Name}, city.Aliases...) {
			parser.cityAliases[Normalize(candidate)] = cityMatch{display: candidate, city: city}
		}
	}
	return parser, nil
}

// CountryByCode looks up a country by code.
func (p *Parser) CountryByCode(code string) (data.Country, bool) {
	country, ok := p.countriesByCode[upper(code)]
	return country, ok
}

// CountryFor resolves a country by name, code or alias.
func (p *Parser) CountryFor(value string) (data.Country, bool) {
	code, ok := p.countryAliases[Normalize(value)]
	if !ok {
		return data.Country{}, false
	}
	return p.CountryByCode(code)
}

// ChileCities returns a copy of the Chilean city catalog.
func (p *Parser) ChileCities() []data.ChileanCity {
	return append([]data.ChileanCity(nil), p.chileCities...)
}

// ChileCitiesByRegion groups the Chilean city catalog by region.
func (p *Parser) ChileCitiesByRegion() map[string][]data.ChileanCity {
	grouped := make(map[string][]data.ChileanCity)
	for _, city := range p.chileCities {
		grouped[city.Region] = append(grouped[city.Region], city)
	}
	return grouped
}

var (
	defaultOnce        sync.Once
	defaultParserValue *Parser
	defaultParserErr   error
)

func defaultParser() (*Parser, error) {
	defaultOnce.Do(func() {
		catalog, err := loadCatalog()
		if err != nil {
			defaultParserErr = err
			return
		}
		defaultParserValue, defaultParserErr = NewParser(catalog)
	})
	if defaultParserErr != nil {
		return nil, defaultParserErr
	}
	return defaultParserValue, nil
}

func loadCatalog() (*data.Catalog, error) {
	return data.Load(context.Background())
}
