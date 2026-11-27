// Package scrapers defines the shared scraper contract and portal implementations.
package scrapers

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/NikoMalek/job-sniper-discord-bot/internal/httpclient"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/job"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/locations"
)

// PortalPlan describes how a portal will search a location intent.
type PortalPlan struct {
	Supported      bool
	SearchLocation string
	Note           string
	Reason         string
}

// Scraper is the common contract implemented by every portal. FetchJobs takes
// an explicit age window and the job ids already known to the caller.
type Scraper interface {
	Slug() string
	Source() string
	CoveredCountries() []string
	Covers(intent *locations.LocationIntent) bool
	PauseReason(intent *locations.LocationIntent) string
	Plan(intent *locations.LocationIntent) PortalPlan
	BeginCycle()
	FetchJobs(
		ctx context.Context,
		keywords []string,
		location string,
		maxAgeHours float64,
		knownJobIDs map[string]struct{},
	) ([]job.Job, error)
}

// BaseScraper implements the default coverage and planning behavior.
type BaseScraper struct {
	slug             string
	source           string
	coveredCountries []string
}

// NewBaseScraper builds the shared scraper behavior. A nil covered slice means
// the portal can search every country.
func NewBaseScraper(slug, source string, coveredCountries []string) BaseScraper {
	return BaseScraper{
		slug:             slug,
		source:           source,
		coveredCountries: append([]string(nil), coveredCountries...),
	}
}

// Slug returns the stable portal identifier.
func (s BaseScraper) Slug() string { return s.slug }

// Source returns the human-readable portal name.
func (s BaseScraper) Source() string { return s.source }

// CoveredCountries returns a copy of the supported ISO alpha-2 codes.
func (s BaseScraper) CoveredCountries() []string {
	return append([]string(nil), s.coveredCountries...)
}

// Covers reports whether the portal can search the given intent.
func (s BaseScraper) Covers(intent *locations.LocationIntent) bool {
	if s.coveredCountries == nil || intent == nil || intent.CountryCode == "" {
		return true
	}
	country := strings.ToUpper(strings.TrimSpace(intent.CountryCode))
	for _, code := range s.coveredCountries {
		if strings.EqualFold(code, country) {
			return true
		}
	}
	return false
}

// PauseReason returns the user-facing text for an unsupported location.
func (s BaseScraper) PauseReason(intent *locations.LocationIntent) string {
	covered := "esos países"
	if len(s.coveredCountries) == 1 && strings.EqualFold(s.coveredCountries[0], "CL") {
		covered = "Chile"
	}
	label := ""
	if intent != nil {
		label = intent.Label()
	}
	return fmt.Sprintf(
		"solo cubre %s y la ubicación actual es %s. Cambia la ubicación o agrega una compatible con `/add-location`.",
		covered, label,
	)
}

// Plan translates a location intent into the portal's search plan.
func (s BaseScraper) Plan(intent *locations.LocationIntent) PortalPlan {
	if !s.Covers(intent) {
		return PortalPlan{Reason: s.PauseReason(intent)}
	}
	label := ""
	if intent != nil {
		label = intent.Label()
	}
	return PortalPlan{Supported: true, SearchLocation: label}
}

// BeginCycle is the base no-op for portals without per-cycle state.
func (s BaseScraper) BeginCycle() {}

// Registry holds scraper singletons reused across monitoring cycles.
type Registry struct {
	mu       sync.RWMutex
	scrapers map[string]Scraper
}

// NewRegistry builds a registry from the supplied scrapers. Later entries with
// the same slug replace earlier ones.
func NewRegistry(scrapers ...Scraper) *Registry {
	registry := &Registry{scrapers: make(map[string]Scraper, len(scrapers))}
	for _, scraper := range scrapers {
		registry.Register(scraper)
	}
	return registry
}

// Register adds or replaces one scraper.
func (r *Registry) Register(scraper Scraper) {
	if r == nil || scraper == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scrapers[scraper.Slug()] = scraper
}

// Get returns the scraper registered for slug, or nil when unknown.
func (r *Registry) Get(slug string) Scraper {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.scrapers[slug]
}

// Scrapers returns a copy of the slug-to-scraper map.
func (r *Registry) Scrapers() map[string]Scraper {
	if r == nil {
		return map[string]Scraper{}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make(map[string]Scraper, len(r.scrapers))
	for slug, scraper := range r.scrapers {
		result[slug] = scraper
	}
	return result
}

// PortalNames maps known slugs to source names and leaves unknown slugs as-is.
func (r *Registry) PortalNames(slugs []string) []string {
	names := make([]string, len(slugs))
	for index, slug := range slugs {
		names[index] = slug
		if scraper := r.Get(slug); scraper != nil {
			names[index] = scraper.Source()
		}
	}
	return names
}

// DefaultPortals mirrors the Python default portal tuple.
var DefaultPortals = []string{"linkedin"}

var (
	defaultRegistryOnce sync.Once
	defaultRegistry     *Registry
)

// DefaultRegistry returns the shared singleton registry. Instances are reused
// so their in-memory caches persist between cycles and guilds.
func DefaultRegistry() *Registry {
	defaultRegistryOnce.Do(func() {
		boardClient := httpclient.NewClient(httpclient.Options{
			Source:     GetOnBoardSource,
			ProxyMode:  GetOnBoardProxyMode,
			ProxyHosts: []string{GetOnBoardProxyHost},
		})
		chileClient := httpclient.NewClient(httpclient.Options{
			Source:     ChileTrabajosSource,
			ProxyMode:  ChileTrabajosProxyMode,
			ProxyHosts: []string{ChileTrabajosProxyHost},
		})
		computrabajoClient := httpclient.NewClient(httpclient.Options{
			Source:     ComputrabajoSource,
			ProxyMode:  ComputrabajoProxyMode,
			ProxyHosts: ComputrabajoProxyHosts(),
		})
		firstJobClient := httpclient.NewClient(httpclient.Options{
			Source:     FirstJobSource,
			ProxyMode:  FirstJobProxyMode,
			ProxyHosts: FirstJobProxyHosts(),
		})
		defaultRegistry = NewRegistry(
			NewGetOnBoardScraper(GetOnBoardOptions{Fetcher: boardClient}),
			NewChileTrabajosScraper(ChileTrabajosOptions{Fetcher: chileClient}),
			NewComputrabajoScraper(ComputrabajoOptions{Fetcher: computrabajoClient}),
			NewFirstJobScraper(FirstJobOptions{Fetcher: firstJobClient}),
		)
	})
	return defaultRegistry
}

// GetScraper looks up a slug in the shared registry.
func GetScraper(slug string) Scraper {
	return DefaultRegistry().Get(slug)
}

// PortalNames maps slugs using the shared registry.
func PortalNames(slugs []string) []string {
	return DefaultRegistry().PortalNames(slugs)
}
