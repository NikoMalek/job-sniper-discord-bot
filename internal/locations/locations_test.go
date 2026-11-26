package locations

import (
	"errors"
	"testing"
)

func TestParseValidLocations(t *testing.T) {
	tests := []struct {
		value       string
		kind        string
		countryCode string
		label       string
	}{
		{value: "Chile", kind: "country", countryCode: "CL", label: "Chile"},
		{value: "Remoto", kind: "remote", countryCode: "", label: "Remoto"},
		{value: "Remoto, Chile", kind: "remote", countryCode: "CL", label: "Remoto, Chile"},
		{value: "Worldwide", kind: "worldwide", countryCode: "", label: "Worldwide"},
		{value: "Madrid, España", kind: "city", countryCode: "ES", label: "Madrid, España"},
		{value: "Santiago, Chile", kind: "city", countryCode: "CL", label: "Santiago, Chile"},
	}

	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			intent, err := Parse(tt.value)
			if err != nil {
				t.Fatalf("Parse(%q) unexpected error: %v", tt.value, err)
			}
			if intent.Kind != tt.kind {
				t.Errorf("Kind = %q, want %q", intent.Kind, tt.kind)
			}
			if intent.CountryCode != tt.countryCode {
				t.Errorf("CountryCode = %q, want %q", intent.CountryCode, tt.countryCode)
			}
			if intent.Label() != tt.label {
				t.Errorf("Label() = %q, want %q", intent.Label(), tt.label)
			}
		})
	}
}

func TestChileanCityWithoutCountryIsInferred(t *testing.T) {
	intent, err := Parse("Santiago")
	if err != nil {
		t.Fatalf("Parse() unexpected error: %v", err)
	}
	if intent.Kind != "city" || intent.CountryCode != "CL" {
		t.Errorf("intent = %+v, want a Chilean city", intent)
	}
	if !intent.InferredCountry || !intent.CityKnown || !intent.IsChile() {
		t.Errorf("intent flags = %+v, want inferred, known and Chilean", intent)
	}
}

func TestExplicitCountryIsNotMarkedAsInferred(t *testing.T) {
	intent, err := Parse("Santiago, Chile")
	if err != nil {
		t.Fatalf("Parse() unexpected error: %v", err)
	}
	if intent.InferredCountry {
		t.Error("InferredCountry = true, want false")
	}
}

func TestWhitespaceAndCaseAreNormalized(t *testing.T) {
	intent, err := Parse("  santiago  ,  chile ")
	if err != nil {
		t.Fatalf("Parse() unexpected error: %v", err)
	}
	if intent.Label() != "Santiago, Chile" {
		t.Fatalf("Label() = %q, want %q", intent.Label(), "Santiago, Chile")
	}
}

func TestCountryFirstOrderIsAccepted(t *testing.T) {
	intent, err := Parse("Chile, Santiago")
	if err != nil {
		t.Fatalf("Parse() unexpected error: %v", err)
	}
	if intent.City != "Santiago" || intent.CountryCode != "CL" {
		t.Fatalf("intent = %+v, want city Santiago in CL", intent)
	}
}

func TestInvalidLocationsRaise(t *testing.T) {
	for _, value := range []string{"", "   ", "Zzzz", "Worldwide, Chile", "Remoto, Narnia"} {
		t.Run(value, func(t *testing.T) {
			_, err := Parse(value)
			var locationErr *LocationError
			if !errors.As(err, &locationErr) {
				t.Fatalf("Parse(%q) error = %v, want LocationError", value, err)
			}
		})
	}
}

func TestParseLocationOrNone(t *testing.T) {
	invalid, err := ParseOrNone("Zzzz")
	if err != nil || invalid != nil {
		t.Fatalf("ParseOrNone(Zzzz) = %+v, %v, want nil, nil", invalid, err)
	}

	valid, err := ParseOrNone("Chile")
	if err != nil || valid == nil {
		t.Fatalf("ParseOrNone(Chile) = %+v, %v, want a non-nil intent", valid, err)
	}
}

func TestParseErrorMessages(t *testing.T) {
	unknownValueMessage := "No reconocí **Zzzz** como país ni como ciudad chilena.\n" +
		"Usa `ciudad, país` (ej. `Río Bueno, Chile`), `país` (ej. `Chile`), `Remoto` o `Worldwide`.\n" +
		"Formatos válidos:\n" +
		"• `Worldwide` — cualquier modalidad, sin filtro geográfico\n" +
		"• `Remoto` — solo ofertas remotas en todo el mundo\n" +
		"• `Remoto, país` — remoto en un país (ej. `Remoto, Chile`)\n" +
		"• `País` — todo el país (ej. `Chile`, `España`)\n" +
		"• `Ciudad, país` — ej. `Santiago, Chile`, `Madrid, España`\n" +
		"También acepto una `ciudad` sola si es chilena conocida, pero el formato recomendado es `ciudad, país`."

	tests := []struct {
		name  string
		value string
		want  string
	}{
		{
			name:  "empty",
			value: "",
			want:  "Indica una ubicación, por ejemplo `Chile` o `Remoto`.",
		},
		{
			name:  "whitespace only",
			value: "   ",
			want:  "Indica una ubicación, por ejemplo `Chile` o `Remoto`.",
		},
		{
			name:  "commas only",
			value: " , ",
			want:  "Indica una ubicación, por ejemplo `Chile` o `Remoto`.",
		},
		{
			name:  "unknown remote word",
			value: "Online",
			want: "No reconocí **Online** como modalidad. " +
				"Para buscar remoto escribe `Remoto` o `Remoto, país`; " +
				"para cualquier modalidad en todo el mundo, `Worldwide`.",
		},
		{
			name:  "unknown value",
			value: "Zzzz",
			want:  unknownValueMessage,
		},
		{
			name:  "remote with bad country",
			value: "Remoto, Narnia",
			want: "No reconocí **Narnia** como país. " +
				"Usa `Remoto, país` con un país real (ej. `Remoto, Chile`) o solo `Remoto`.",
		},
		{
			name:  "bad last country",
			value: "Madrid, Narnia",
			want: "No reconocí **Narnia** como país.\n" +
				"Usa `ciudad, país` (ej. `Madrid, España`) o `país` (ej. `España`). " +
				"Si es una ciudad chilena, escribe `ciudad, Chile`.",
		},
		{
			name:  "worldwide with country",
			value: "Worldwide, Chile",
			want: "Para buscar en todo el mundo escribe solo `Worldwide`. " +
				"Si quieres limitarlo a un país, usa `País` (ej. `Chile`).",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.value)
			if err == nil {
				t.Fatalf("Parse(%q) error = nil, want %q", tt.value, tt.want)
			}
			if err.Error() != tt.want {
				t.Fatalf("Parse(%q) error = %q, want %q", tt.value, err.Error(), tt.want)
			}
		})
	}
}

func TestNormalize(t *testing.T) {
	tests := []struct {
		value string
		want  string
	}{
		{value: "  Árbol   Navidad  ", want: "arbol navidad"},
		{value: "Niño", want: "nino"},
		{value: "Español", want: "espanol"},
		{value: "Åland", want: "aland"},
		{value: "Straße", want: "strasse"},
		{value: "e\u0301", want: "e"},
		{value: "SANTIAGO", want: "santiago"},
	}

	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			if got := Normalize(tt.value); got != tt.want {
				t.Fatalf("Normalize(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestDisplayCase(t *testing.T) {
	tests := []struct {
		value string
		want  string
	}{
		{value: "de la costa", want: "De la Costa"},
		{value: "santiago del campo", want: "Santiago del Campo"},
		{value: "los ángeles y da norte", want: "Los Ángeles y da Norte"},
	}

	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			if got := displayCase(tt.value); got != tt.want {
				t.Fatalf("displayCase(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestChileCityAliasesAndBuckets(t *testing.T) {
	t.Run("alias city", func(t *testing.T) {
		intent, err := Parse("Viña del Mar")
		if err != nil {
			t.Fatalf("Parse() unexpected error: %v", err)
		}
		if intent.City != "Viña del Mar" || !intent.CityKnown || !intent.CityAlias {
			t.Errorf("intent = %+v, want a known alias city", intent)
		}
		if intent.ChileBucket != "1014" || intent.ChileBucketCity != "Valparaíso" {
			t.Errorf("bucket = %q/%q, want 1014/Valparaíso", intent.ChileBucket, intent.ChileBucketCity)
		}
		if intent.Label() != "Viña del Mar, Chile" {
			t.Errorf("Label() = %q, want %q", intent.Label(), "Viña del Mar, Chile")
		}
	})

	t.Run("bucket city", func(t *testing.T) {
		intent, err := Parse("Santiago")
		if err != nil {
			t.Fatalf("Parse() unexpected error: %v", err)
		}
		if intent.CityAlias || !intent.CityKnown {
			t.Errorf("intent = %+v, want a bucket city without alias", intent)
		}
		if intent.ChileBucketCity != "Santiago" {
			t.Errorf("ChileBucketCity = %q, want Santiago", intent.ChileBucketCity)
		}
	})

	t.Run("city ending with country name", func(t *testing.T) {
		intent, err := Parse("Santiago de Chile")
		if err != nil {
			t.Fatalf("Parse() unexpected error: %v", err)
		}
		if intent.Label() != "Santiago de Chile" {
			t.Errorf("Label() = %q, want %q", intent.Label(), "Santiago de Chile")
		}
	})
}

func TestCountryForAliases(t *testing.T) {
	tests := []struct {
		value string
		code  string
	}{
		{value: "usa", code: "US"},
		{value: "uk", code: "GB"},
		{value: "todo chile", code: "CL"},
		{value: "España", code: "ES"},
	}

	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			country, ok := CountryFor(tt.value)
			if !ok || country.Code != tt.code {
				t.Fatalf("CountryFor(%q) = %+v, %v, want %s", tt.value, country, ok, tt.code)
			}
		})
	}
}

func TestChileCitiesByRegion(t *testing.T) {
	grouped := ChileCitiesByRegion()
	total := 0
	for _, cities := range grouped {
		total += len(cities)
	}
	if total != len(ChileCities()) {
		t.Fatalf("grouped cities = %d, want %d", total, len(ChileCities()))
	}
	found := false
	for _, city := range grouped["Metropolitana"] {
		if city.Name == "Santiago" {
			found = true
		}
	}
	if !found {
		t.Error("Santiago is missing from the Metropolitana group")
	}
}

func TestMaxLocations(t *testing.T) {
	if MaxLocations != 3 {
		t.Fatalf("MaxLocations = %d, want 3", MaxLocations)
	}
}

func TestNewParserRejectsNilCatalog(t *testing.T) {
	if _, err := NewParser(nil); err == nil {
		t.Fatal("NewParser(nil) error = nil, want an error")
	}
}
