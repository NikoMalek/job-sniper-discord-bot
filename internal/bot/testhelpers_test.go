package bot

import (
	"context"
	"strings"
	"testing"

	"github.com/NikoMalek/job-sniper-discord-bot/internal/data"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/job"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/locations"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/matching"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/scrapers"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/store"
)

type fakeStore struct {
	config           store.GuildMonitorConfig
	setupSent        bool
	setupSentByGuild map[int64]bool
	setupChecks      int
	markCount        int
	notified         int64

	getConfigErr      error
	setChannelErr     error
	setLocationErr    error
	addLocationErr    error
	removeLocationErr error
	setAgeErr         error
	addKeywordErr     error
	removeKeywordErr  error
	addCompanyErr     error
	removeCompanyErr  error
	addBlockedErr     error
	removeBlockedErr  error
	addLanguageErr    error
	removeLanguageErr error
	addPortalErr      error
	removePortalErr   error
	countErr          error
	hasSetupErr       error
	markSetupErr      error
}

func (s *fakeStore) HasSetupMessage(_ context.Context, guildID int64) (bool, error) {
	s.setupChecks++
	if s.hasSetupErr != nil {
		return false, s.hasSetupErr
	}
	if s.setupSentByGuild != nil {
		return s.setupSentByGuild[guildID], nil
	}
	return s.setupSent, nil
}

func (s *fakeStore) MarkSetupMessageSent(_ context.Context, guildID int64) error {
	s.markCount++
	if s.markSetupErr != nil {
		return s.markSetupErr
	}
	s.setupSent = true
	if s.setupSentByGuild == nil {
		s.setupSentByGuild = make(map[int64]bool)
	}
	s.setupSentByGuild[guildID] = true
	return nil
}

func (s *fakeStore) SetChannel(_ context.Context, _, channelID int64) error {
	if s.setChannelErr != nil {
		return s.setChannelErr
	}
	s.config.ChannelID = &channelID
	return nil
}

func (s *fakeStore) SetLocation(_ context.Context, _ int64, location string) error {
	if s.setLocationErr != nil {
		return s.setLocationErr
	}
	s.config.Locations = []string{location}
	return nil
}

func (s *fakeStore) GetLocations(context.Context, int64) ([]string, error) {
	return append([]string(nil), s.config.Locations...), nil
}

func (s *fakeStore) AddLocation(_ context.Context, _ int64, location string) (bool, error) {
	if s.addLocationErr != nil {
		return false, s.addLocationErr
	}
	for _, existing := range s.config.Locations {
		if existing == location {
			return false, nil
		}
	}
	s.config.Locations = append(s.config.Locations, location)
	return true, nil
}

func (s *fakeStore) RemoveLocation(_ context.Context, _ int64, location string) (bool, error) {
	if s.removeLocationErr != nil {
		return false, s.removeLocationErr
	}
	for index, existing := range s.config.Locations {
		if existing == location {
			s.config.Locations = append(s.config.Locations[:index], s.config.Locations[index+1:]...)
			return true, nil
		}
	}
	return false, nil
}

func (s *fakeStore) SetMaxAgeHours(_ context.Context, _ int64, hours float64) error {
	if s.setAgeErr != nil {
		return s.setAgeErr
	}
	s.config.MaxAgeHours = hours
	return nil
}

func (s *fakeStore) AddKeyword(_ context.Context, _ int64, keyword string) (bool, error) {
	if s.addKeywordErr != nil {
		return false, s.addKeywordErr
	}
	value := strings.ToLower(keyword)
	if containsString(s.config.Keywords, value) {
		return false, nil
	}
	s.config.Keywords = append(s.config.Keywords, value)
	return true, nil
}

func (s *fakeStore) RemoveKeyword(_ context.Context, _ int64, keyword string) (bool, error) {
	if s.removeKeywordErr != nil {
		return false, s.removeKeywordErr
	}
	value := strings.ToLower(keyword)
	for index, existing := range s.config.Keywords {
		if existing == value {
			s.config.Keywords = append(s.config.Keywords[:index], s.config.Keywords[index+1:]...)
			return true, nil
		}
	}
	return false, nil
}

func (s *fakeStore) AddBlacklistedCompany(_ context.Context, _ int64, company string) (bool, error) {
	if s.addCompanyErr != nil {
		return false, s.addCompanyErr
	}
	value := strings.ToLower(company)
	if containsString(s.config.BlacklistedCompanies, value) {
		return false, nil
	}
	s.config.BlacklistedCompanies = append(s.config.BlacklistedCompanies, value)
	return true, nil
}

func (s *fakeStore) RemoveBlacklistedCompany(_ context.Context, _ int64, company string) (bool, error) {
	if s.removeCompanyErr != nil {
		return false, s.removeCompanyErr
	}
	value := strings.ToLower(company)
	for index, existing := range s.config.BlacklistedCompanies {
		if existing == value {
			s.config.BlacklistedCompanies = append(s.config.BlacklistedCompanies[:index], s.config.BlacklistedCompanies[index+1:]...)
			return true, nil
		}
	}
	return false, nil
}

func (s *fakeStore) AddBlacklistedKeyword(_ context.Context, _ int64, keyword string) (bool, error) {
	if s.addBlockedErr != nil {
		return false, s.addBlockedErr
	}
	value := strings.ToLower(keyword)
	if containsString(s.config.BlacklistedKeywords, value) {
		return false, nil
	}
	s.config.BlacklistedKeywords = append(s.config.BlacklistedKeywords, value)
	return true, nil
}

func (s *fakeStore) RemoveBlacklistedKeyword(_ context.Context, _ int64, keyword string) (bool, error) {
	if s.removeBlockedErr != nil {
		return false, s.removeBlockedErr
	}
	value := strings.ToLower(keyword)
	for index, existing := range s.config.BlacklistedKeywords {
		if existing == value {
			s.config.BlacklistedKeywords = append(s.config.BlacklistedKeywords[:index], s.config.BlacklistedKeywords[index+1:]...)
			return true, nil
		}
	}
	return false, nil
}

func (s *fakeStore) AddLanguage(_ context.Context, _ int64, language string) (bool, error) {
	if s.addLanguageErr != nil {
		return false, s.addLanguageErr
	}
	if containsString(s.config.Languages, language) {
		return false, nil
	}
	s.config.Languages = append(s.config.Languages, language)
	return true, nil
}

func (s *fakeStore) RemoveLanguage(_ context.Context, _ int64, language string) (bool, error) {
	if s.removeLanguageErr != nil {
		return false, s.removeLanguageErr
	}
	for index, existing := range s.config.Languages {
		if existing == language {
			s.config.Languages = append(s.config.Languages[:index], s.config.Languages[index+1:]...)
			return true, nil
		}
	}
	return false, nil
}

func (s *fakeStore) AddPortal(_ context.Context, _ int64, portal string) (bool, error) {
	if s.addPortalErr != nil {
		return false, s.addPortalErr
	}
	if containsString(s.config.Portals, portal) {
		return false, nil
	}
	s.config.Portals = append(s.config.Portals, portal)
	return true, nil
}

func (s *fakeStore) RemovePortal(_ context.Context, _ int64, portal string) (bool, error) {
	if s.removePortalErr != nil {
		return false, s.removePortalErr
	}
	for index, existing := range s.config.Portals {
		if existing == portal {
			s.config.Portals = append(s.config.Portals[:index], s.config.Portals[index+1:]...)
			return true, nil
		}
	}
	return false, nil
}

func (s *fakeStore) GetGuildConfig(context.Context, int64) (store.GuildMonitorConfig, error) {
	config := s.config
	config.Locations = append([]string(nil), s.config.Locations...)
	config.Keywords = append([]string(nil), s.config.Keywords...)
	config.BlacklistedCompanies = append([]string(nil), s.config.BlacklistedCompanies...)
	config.BlacklistedKeywords = append([]string(nil), s.config.BlacklistedKeywords...)
	config.Languages = append([]string(nil), s.config.Languages...)
	config.Portals = append([]string(nil), s.config.Portals...)
	return config, s.getConfigErr
}

func (s *fakeStore) CountNotified(context.Context, int64) (int64, error) {
	return s.notified, s.countErr
}

type fakeRegistry struct {
	portals map[string]scrapers.Scraper
}

func (r *fakeRegistry) Get(slug string) scrapers.Scraper {
	return r.portals[slug]
}

func (r *fakeRegistry) Scrapers() map[string]scrapers.Scraper {
	result := make(map[string]scrapers.Scraper, len(r.portals))
	for slug, scraper := range r.portals {
		result[slug] = scraper
	}
	return result
}

func (r *fakeRegistry) PortalNames(slugs []string) []string {
	result := make([]string, len(slugs))
	for index, slug := range slugs {
		result[index] = slug
		if scraper := r.portals[slug]; scraper != nil {
			result[index] = scraper.Source()
		}
	}
	return result
}

type fakeScraper struct {
	slug     string
	source   string
	covered  []string
	planFunc func(*locations.LocationIntent) scrapers.PortalPlan
}

func (s *fakeScraper) Slug() string               { return s.slug }
func (s *fakeScraper) Source() string             { return s.source }
func (s *fakeScraper) CoveredCountries() []string { return append([]string(nil), s.covered...) }

func (s *fakeScraper) Covers(intent *locations.LocationIntent) bool {
	if s.covered == nil || intent == nil || intent.CountryCode == "" {
		return true
	}
	for _, code := range s.covered {
		if strings.EqualFold(code, intent.CountryCode) {
			return true
		}
	}
	return false
}

func (s *fakeScraper) PauseReason(*locations.LocationIntent) string {
	return "sin cobertura."
}

func (s *fakeScraper) Plan(intent *locations.LocationIntent) scrapers.PortalPlan {
	if s.planFunc != nil {
		return s.planFunc(intent)
	}
	if !s.Covers(intent) {
		return scrapers.PortalPlan{Reason: "sin cobertura."}
	}
	label := ""
	if intent != nil {
		label = intent.Label()
	}
	return scrapers.PortalPlan{Supported: true, SearchLocation: label}
}

func (s *fakeScraper) BeginCycle() {}

func (s *fakeScraper) FetchJobs(
	context.Context, []string, string, float64, map[string]struct{},
) ([]job.Job, error) {
	return nil, nil
}

func newTestAdmin(t *testing.T, st Store, registry *fakeRegistry) *Admin {
	t.Helper()
	ctx := context.Background()
	catalog, err := data.Load(ctx)
	if err != nil {
		t.Fatalf("data.Load() error = %v", err)
	}
	admin, err := NewAdmin(ctx, AdminOptions{
		Store:    st,
		Registry: registry,
		Catalog:  catalog,
		Matcher:  matching.NewWithCatalog(catalog, nil),
	})
	if err != nil {
		t.Fatalf("NewAdmin() error = %v", err)
	}
	return admin
}

func channelConfig(channelID int64) store.GuildMonitorConfig {
	return store.GuildMonitorConfig{ChannelID: &channelID}
}

func basicRegistry() *fakeRegistry {
	return &fakeRegistry{portals: map[string]scrapers.Scraper{
		"linkedin": &fakeScraper{slug: "linkedin", source: "LinkedIn"},
		"chiletrabajos": &fakeScraper{
			slug: "chiletrabajos", source: "ChileTrabajos", covered: []string{"CL"},
		},
	}}
}
