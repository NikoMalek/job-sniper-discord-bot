package scrapers

import (
	"context"
	"testing"

	"github.com/NikoMalek/job-sniper-discord-bot/internal/job"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/locations"
)

type stubScraper struct {
	BaseScraper
}

func (stubScraper) FetchJobs(
	context.Context, []string, string, float64, map[string]struct{},
) ([]job.Job, error) {
	return nil, nil
}

func TestBaseScraperCoverageAndPlanning(t *testing.T) {
	chile, err := locations.Parse("Chile")
	if err != nil {
		t.Fatalf("parse Chile: %v", err)
	}
	spain, err := locations.Parse("España")
	if err != nil {
		t.Fatalf("parse España: %v", err)
	}
	worldwide, err := locations.Parse("Worldwide")
	if err != nil {
		t.Fatalf("parse Worldwide: %v", err)
	}

	global := NewBaseScraper("global", "Global Portal", nil)
	if !global.Covers(&chile) || !global.Covers(&spain) || !global.Covers(&worldwide) {
		t.Fatal("global scraper must cover every country and worldwide")
	}
	plan := global.Plan(&chile)
	if !plan.Supported || plan.SearchLocation != chile.Label() {
		t.Fatalf("unexpected global plan: %+v", plan)
	}

	regional := NewBaseScraper("regional", "Regional Portal", []string{"CL"})
	if !regional.Covers(&chile) {
		t.Fatal("regional scraper must cover Chile")
	}
	if regional.Covers(&spain) {
		t.Fatal("regional scraper must not cover Spain")
	}
	if !regional.Covers(&worldwide) {
		t.Fatal("regional scraper must cover a country-less intent")
	}
	regionalPlan := regional.Plan(&spain)
	if regionalPlan.Supported || regionalPlan.SearchLocation != "" {
		t.Fatalf("unexpected regional plan: %+v", regionalPlan)
	}
	wantReason := "solo cubre Chile y la ubicación actual es España. Cambia la ubicación o agrega una compatible con `/add-location`."
	if regionalPlan.Reason != wantReason {
		t.Fatalf("pause reason = %q, want %q", regionalPlan.Reason, wantReason)
	}

	multi := NewBaseScraper("multi", "Multi Portal", []string{"CL", "AR"})
	multiPlan := multi.Plan(&spain)
	wantMultiReason := "solo cubre esos países y la ubicación actual es España. Cambia la ubicación o agrega una compatible con `/add-location`."
	if multiPlan.Reason != wantMultiReason {
		t.Fatalf("pause reason = %q, want %q", multiPlan.Reason, wantMultiReason)
	}
	countries := multi.CoveredCountries()
	countries[0] = "XX"
	if multi.CoveredCountries()[0] != "CL" {
		t.Fatal("CoveredCountries must return a copy")
	}
}

func TestRegistryAndPortalNames(t *testing.T) {
	registry := NewRegistry(
		stubScraper{NewBaseScraper("alpha", "Alpha Portal", nil)},
		NewGetOnBoardScraper(GetOnBoardOptions{}),
	)
	if got := registry.Get(GetOnBoardSlug); got == nil || got.Source() != GetOnBoardSource {
		t.Fatalf("Get(%q) = %v", GetOnBoardSlug, got)
	}
	if registry.Get("missing") != nil {
		t.Fatal("unknown slug must return nil")
	}
	names := registry.PortalNames([]string{"alpha", GetOnBoardSlug, "missing"})
	want := []string{"Alpha Portal", GetOnBoardSource, "missing"}
	for index := range want {
		if names[index] != want[index] {
			t.Fatalf("names[%d] = %q, want %q", index, names[index], want[index])
		}
	}

	if len(DefaultPortals) != 1 || DefaultPortals[0] != "linkedin" {
		t.Fatalf("unexpected default portals: %v", DefaultPortals)
	}
	if got := DefaultRegistry().Get(GetOnBoardSlug); got == nil {
		t.Fatal("default registry must contain the Get on Board singleton")
	}
	if got := PortalNames([]string{GetOnBoardSlug}); len(got) != 1 || got[0] != GetOnBoardSource {
		t.Fatalf("unexpected portal names: %v", got)
	}
}
