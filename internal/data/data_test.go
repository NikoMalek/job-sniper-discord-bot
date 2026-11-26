package data

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestLoadEmbeddedCatalogs(t *testing.T) {
	catalog, err := Load(context.Background())
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}

	if got := len(catalog.Countries()); got != 256 {
		t.Errorf("len(Countries()) = %d, want 256", got)
	}
	if got := len(catalog.CountryAliases()); got != 20 {
		t.Errorf("len(CountryAliases()) = %d, want 20", got)
	}
	if country, ok := catalog.CountryByCode("cl"); !ok || country.NameES != "Chile" {
		t.Errorf("CountryByCode(cl) = %+v, %v", country, ok)
	}
	if got := catalog.CountryAliases()["uk"]; got != "GB" {
		t.Errorf("CountryAliases()[uk] = %q, want GB", got)
	}

	cities := catalog.ChileCities()
	if got := len(cities); got != 58 {
		t.Errorf("len(ChileCities()) = %d, want 58", got)
	}
	aliasRecords, aliasCount := 0, 0
	for _, city := range cities {
		if len(city.Aliases) > 0 {
			aliasRecords++
			aliasCount += len(city.Aliases)
		}
	}
	if aliasRecords != 2 || aliasCount != 19 {
		t.Errorf("alias records = %d and aliases = %d, want 2 and 19", aliasRecords, aliasCount)
	}

	if got := catalog.Computrabajo().SchemaVersion; got != 1 {
		t.Errorf("Computrabajo().SchemaVersion = %d, want 1", got)
	}
	if got := catalog.Computrabajo().DefaultCountry; got != "CL" {
		t.Errorf("Computrabajo().DefaultCountry = %q, want CL", got)
	}
	if got := catalog.FirstJob().DefaultCountry; got != "CL" {
		t.Errorf("FirstJob().DefaultCountry = %q, want CL", got)
	}
	if len(catalog.KeywordAliases()) == 0 {
		t.Error("KeywordAliases() is empty")
	}
	if len(catalog.BlacklistKeywordAliasGroups()) == 0 {
		t.Error("BlacklistKeywordAliasGroups() is empty")
	}
}

func TestLoadContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Load(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Load() error = %v, want context.Canceled", err)
	}
}

func TestParseCountriesValidation(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr string
	}{
		{
			name: "duplicate codes",
			raw: `{"countries":[
				{"code":"CL","name_es":"Chile","name_en":"Chile"},
				{"code":"cl","name_es":"Chile 2","name_en":"Chile 2"}
			],"aliases":{}}`,
			wantErr: "countries.json: duplicate country codes: CL",
		},
		{
			name: "unknown alias target",
			raw: `{"countries":[
				{"code":"CL","name_es":"Chile","name_en":"Chile"}
			],"aliases":{"narnia":"XX"}}`,
			wantErr: "countries.json: aliases point to unknown countries: XX",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := parseCountries([]byte(tt.raw))
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("parseCountries() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestParseComputrabajoValidation(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr string
	}{
		{
			name:    "duplicate country",
			raw:     `{"schema_version":1,"default_country":"CL","countries":{"cl":{"label":"Chile","subdomain":"cl"},"CL":{"label":"Chile","subdomain":"cl2"}}}`,
			wantErr: "computrabajo_locations.json: duplicate country: CL",
		},
		{
			name:    "invalid subdomain",
			raw:     `{"schema_version":1,"default_country":"CL","countries":{"CL":{"label":"Chile","subdomain":"www.cl"}}}`,
			wantErr: "computrabajo_locations.json: invalid subdomain: \"www.cl\"",
		},
		{
			name:    "duplicate subdomain",
			raw:     `{"schema_version":1,"default_country":"CL","countries":{"CL":{"label":"Chile","subdomain":"cl"},"AR":{"label":"Argentina","subdomain":"CL"}}}`,
			wantErr: "computrabajo_locations.json: duplicate subdomain: \"cl\"",
		},
		{
			name:    "empty slug",
			raw:     `{"schema_version":1,"default_country":"CL","countries":{"CL":{"label":"Chile","subdomain":"cl","locations":[{"slug":"  ","label":"x"}]}}}`,
			wantErr: "computrabajo_locations.json: invalid location slug in CL: \"\"",
		},
		{
			name:    "duplicate slug",
			raw:     `{"schema_version":1,"default_country":"CL","countries":{"CL":{"label":"Chile","subdomain":"cl","locations":[{"slug":"santiago","label":"a"},{"slug":"santiago","label":"b"}]}}}`,
			wantErr: "computrabajo_locations.json: duplicate location slug in CL: \"santiago\"",
		},
		{
			name:    "unknown default country",
			raw:     `{"schema_version":1,"default_country":"XX","countries":{"CL":{"label":"Chile","subdomain":"cl"}}}`,
			wantErr: "computrabajo_locations.json: default_country \"XX\" is not in the catalog",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseComputrabajoCatalog([]byte(tt.raw))
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("parseComputrabajoCatalog() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestParseFirstJobValidation(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr string
	}{
		{
			name:    "duplicate country",
			raw:     `{"default_country":"CL","countries":{"cl":{"label":"Chile","country_id":"43"},"CL":{"label":"Chile","country_id":"44"}}}`,
			wantErr: "firstjob_locations.json: duplicate country: CL",
		},
		{
			name:    "empty country id",
			raw:     `{"default_country":"CL","countries":{"CL":{"label":"Chile","country_id":"  "}}}`,
			wantErr: "firstjob_locations.json: invalid country_id: \"\"",
		},
		{
			name:    "duplicate country id",
			raw:     `{"default_country":"CL","countries":{"CL":{"label":"Chile","country_id":"43"},"AR":{"label":"Argentina","country_id":"43"}}}`,
			wantErr: "firstjob_locations.json: duplicate country_id: \"43\"",
		},
		{
			name:    "empty region id",
			raw:     `{"default_country":"CL","countries":{"CL":{"label":"Chile","country_id":"43","regions":[{"id":"  ","label":"x"}]}}}`,
			wantErr: "firstjob_locations.json: invalid region id in CL: \"\"",
		},
		{
			name:    "duplicate region id",
			raw:     `{"default_country":"CL","countries":{"CL":{"label":"Chile","country_id":"43","regions":[{"id":"13","label":"a"},{"id":"13","label":"b"}]}}}`,
			wantErr: "firstjob_locations.json: duplicate region id in CL: \"13\"",
		},
		{
			name:    "unknown default country",
			raw:     `{"default_country":"XX","countries":{"CL":{"label":"Chile","country_id":"43"}}}`,
			wantErr: "firstjob_locations.json: default_country \"XX\" is not in the catalog",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseFirstJobCatalog([]byte(tt.raw))
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("parseFirstJobCatalog() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoadRejectsMalformedEmbeddedJSON(t *testing.T) {
	_, _, err := parseCountries([]byte(strings.Repeat("{", 2)))
	if err == nil || !strings.Contains(err.Error(), "parse countries.json") {
		t.Fatalf("parseCountries() error = %v, want parse error", err)
	}
}
