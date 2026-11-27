package scrapers

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/NikoMalek/job-sniper-discord-bot/internal/data"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/httpclient"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/job"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/locations"
	"github.com/PuerkitoBio/goquery"
)

const (
	// ComputrabajoSlug is the stable identifier of the portal.
	ComputrabajoSlug = "computrabajo"
	// ComputrabajoSource is the human-readable name shown in notifications.
	ComputrabajoSource = "Computrabajo"

	computrabajoBaseHost = "computrabajo.com"
	// ComputrabajoProxyMode describes the Worker fallback transport.
	ComputrabajoProxyMode = "fallback"

	computrabajoPageSize       = 20
	computrabajoMaxPages       = 3
	computrabajoMaxDetails     = 25
	computrabajoDetailMinDelay = 400 * time.Millisecond
	computrabajoDetailMaxDelay = time.Second
	computrabajoSearchMinDelay = 600 * time.Millisecond
	computrabajoSearchMaxDelay = 1200 * time.Millisecond
	computrabajoDetailTTL      = 6 * time.Hour
	computrabajoDetailLimit    = 500
	computrabajoSearchTTL      = 4 * time.Minute
	computrabajoDeferredTTL    = 3 * time.Hour
	computrabajoDeferredLimit  = 200
	computrabajoDeferredMaxCtx = 50
	computrabajoRemoteSuffix   = " (Remoto)"
)

var (
	computrabajoTotalPattern  = regexp.MustCompile(`(?i)Consulta las ([\d.]+) ofertas`)
	computrabajoDigitsPattern = regexp.MustCompile(`\d+`)
	computrabajoMonthPattern  = regexp.MustCompile(`(\d{1,2})\s+de\s+([a-z]+)`)
	computrabajoSlugPattern   = regexp.MustCompile(`[^a-z0-9]+`)
	computrabajoBlockMarkers  = []string{
		"captcha", "no soy un robot", "verifica que eres humano",
		"acceso denegado", "request blocked", "unusual traffic",
	}
	computrabajoMonths = map[string]time.Month{
		"ene": time.January, "enero": time.January,
		"feb": time.February, "febrero": time.February,
		"mar": time.March, "marzo": time.March,
		"abr": time.April, "abril": time.April,
		"may": time.May, "mayo": time.May,
		"jun": time.June, "junio": time.June,
		"jul": time.July, "julio": time.July,
		"ago": time.August, "agosto": time.August,
		"sep": time.September, "septiembre": time.September, "setiembre": time.September,
		"oct": time.October, "octubre": time.October,
		"nov": time.November, "noviembre": time.November,
		"dic": time.December, "diciembre": time.December,
	}
	computrabajoUserAgents = []string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Safari/537.36",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15",
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Safari/537.36",
	}
)

type computrabajoCatalogView struct {
	Catalog         data.ComputrabajoCatalog
	CountriesByCode map[string]data.ComputrabajoCountry
	CountryLookup   map[string]string
	CountryKeys     []string
	LocationLookup  map[[2]string]data.ComputrabajoLocation
	DefaultCountry  string
	LogoBaseURL     string
}

var (
	computrabajoCatalogOnce sync.Once
	computrabajoCatalog     *computrabajoCatalogView
)

func loadComputrabajoCatalog() (*computrabajoCatalogView, error) {
	var err error
	computrabajoCatalogOnce.Do(func() {
		catalog, loadErr := data.Load(context.Background())
		if loadErr != nil {
			err = fmt.Errorf("load Computrabajo catalog: %w", loadErr)
			return
		}
		computrabajoCatalog = buildComputrabajoCatalogView(catalog.Computrabajo())
	})
	if err != nil || computrabajoCatalog == nil {
		return nil, err
	}
	return computrabajoCatalog, nil
}

func buildComputrabajoCatalogView(catalog data.ComputrabajoCatalog) *computrabajoCatalogView {
	view := &computrabajoCatalogView{
		Catalog:         catalog,
		CountriesByCode: make(map[string]data.ComputrabajoCountry, len(catalog.Countries)),
		CountryLookup:   make(map[string]string),
		LocationLookup:  make(map[[2]string]data.ComputrabajoLocation),
		DefaultCountry:  strings.ToUpper(strings.TrimSpace(catalog.DefaultCountry)),
	}
	if view.DefaultCountry == "" {
		view.DefaultCountry = "CL"
	}

	countryOrder := make([]string, 0, len(view.CountryLookup))
	for _, country := range catalog.Countries {
		view.CountriesByCode[country.Code] = country
		for _, name := range append([]string{country.Label}, country.Aliases...) {
			key := locations.Normalize(name)
			if key == "" {
				continue
			}
			if _, exists := view.CountryLookup[key]; !exists {
				countryOrder = append(countryOrder, key)
			}
			view.CountryLookup[key] = country.Code
		}
		for _, place := range country.Locations {
			names := append([]string{place.City, place.Label}, place.Aliases...)
			for _, name := range names {
				key := locations.Normalize(name)
				if key == "" {
					continue
				}
				lookupKey := [2]string{country.Code, key}
				if _, exists := view.LocationLookup[lookupKey]; !exists {
					view.LocationLookup[lookupKey] = place
				}
			}
		}
	}
	view.CountryKeys = append(view.CountryKeys, countryOrder...)
	sort.SliceStable(view.CountryKeys, func(i, j int) bool {
		return len(view.CountryKeys[i]) > len(view.CountryKeys[j])
	})
	view.LogoBaseURL = "https://" + strings.ToLower(view.DefaultCountry) + "." + computrabajoBaseHost
	return view
}

func emptyComputrabajoCatalogView() *computrabajoCatalogView {
	return &computrabajoCatalogView{
		CountriesByCode: make(map[string]data.ComputrabajoCountry),
		CountryLookup:   make(map[string]string),
		LocationLookup:  make(map[[2]string]data.ComputrabajoLocation),
		DefaultCountry:  "CL",
		LogoBaseURL:     "https://cl." + computrabajoBaseHost,
	}
}

// ComputrabajoProxyHosts returns the Worker fallback hosts in catalog order.
func ComputrabajoProxyHosts() []string {
	catalog, err := loadComputrabajoCatalog()
	if err != nil {
		slog.Warn("Computrabajo catalog is unavailable; no proxy hosts were added", "error", err)
		return nil
	}
	hosts := make([]string, 0, len(catalog.Catalog.Countries))
	for _, country := range catalog.Catalog.Countries {
		hosts = append(hosts, country.Subdomain+"."+computrabajoBaseHost)
	}
	return hosts
}

// ComputrabajoOptions holds the deterministic dependencies of the scraper.
type ComputrabajoOptions struct {
	Fetcher    httpclient.Fetcher
	Clock      func() time.Time
	CacheClock func() time.Time
	Sleep      func(context.Context, time.Duration) error
	Random     func() float64
}

type computrabajoCandidateSet struct {
	Records map[string]job.Job
	Order   []string
}

func newComputrabajoCandidateSet() computrabajoCandidateSet {
	return computrabajoCandidateSet{Records: make(map[string]job.Job)}
}

func (c computrabajoCandidateSet) clone() computrabajoCandidateSet {
	clone := newComputrabajoCandidateSet()
	for id, record := range c.Records {
		clone.Records[id] = record
	}
	clone.Order = append(clone.Order, c.Order...)
	return clone
}

type computrabajoContext struct {
	TermsKey  string
	Subdomain string
	Slug      string
}

func (c computrabajoContext) key() string {
	return c.TermsKey + "\x00" + c.Subdomain + "\x00" + c.Slug
}

type computrabajoCachedSearch struct {
	ExpiresAt    time.Time
	Candidates   computrabajoCandidateSet
	PagesFetched int
	FoundCount   int
}

type computrabajoSearchCall struct {
	done       chan struct{}
	candidates computrabajoCandidateSet
	pages      int
	found      int
	complete   bool
	err        error
}

type computrabajoDetailEntry struct {
	ExpiresAt time.Time
	Job       job.Job
	Posted    time.Time
}

type computrabajoDeferredEntry struct {
	ExpiresAt time.Time
	Job       job.Job
}

type computrabajoDeferredState struct {
	Order []string
	Items map[string]computrabajoDeferredEntry
}

type computrabajoResolved struct {
	Country data.ComputrabajoCountry
	Slug    string
	Matched bool
}

// ComputrabajoScraper searches the per-country Computrabajo sites and enriches
// recent cards with the schema.org JobPosting data on the detail page.
type ComputrabajoScraper struct {
	BaseScraper

	fetcher    httpclient.Fetcher
	clock      func() time.Time
	cacheClock func() time.Time
	sleep      func(context.Context, time.Duration) error
	random     func() float64
	catalog    *computrabajoCatalogView

	mu          sync.Mutex
	detailCache map[string]computrabajoDetailEntry
	detailOrder []string
	searchCache map[computrabajoContext]computrabajoCachedSearch
	searchCalls map[computrabajoContext]*computrabajoSearchCall
	deferred    map[computrabajoContext]*computrabajoDeferredState
}

// NewComputrabajoScraper builds a scraper. Missing dependencies receive the
// production defaults; tests inject a replay fetcher, clocks and sleeper.
func NewComputrabajoScraper(options ComputrabajoOptions) *ComputrabajoScraper {
	fetcher := options.Fetcher
	if fetcher == nil {
		fetcher = httpclient.NewClient(httpclient.Options{
			Source:     ComputrabajoSource,
			ProxyMode:  ComputrabajoProxyMode,
			ProxyHosts: ComputrabajoProxyHosts(),
		})
	}
	clock := options.Clock
	if clock == nil {
		clock = time.Now
	}
	cacheClock := options.CacheClock
	if cacheClock == nil {
		cacheClock = time.Now
	}
	sleeper := options.Sleep
	if sleeper == nil {
		sleeper = sleepContext
	}
	random := options.Random
	if random == nil {
		random = rand.Float64
	}
	catalog, err := loadComputrabajoCatalog()
	if err != nil {
		slog.Warn("Computrabajo built-in location catalog is unavailable", "error", err)
		catalog = emptyComputrabajoCatalogView()
	}
	codes := make([]string, 0, len(catalog.Catalog.Countries))
	for _, country := range catalog.Catalog.Countries {
		codes = append(codes, country.Code)
	}
	return &ComputrabajoScraper{
		BaseScraper: NewBaseScraper(ComputrabajoSlug, ComputrabajoSource, codes),
		fetcher:     fetcher,
		clock:       clock,
		cacheClock:  cacheClock,
		sleep:       sleeper,
		random:      random,
		catalog:     catalog,
		detailCache: make(map[string]computrabajoDetailEntry),
		searchCache: make(map[computrabajoContext]computrabajoCachedSearch),
		searchCalls: make(map[computrabajoContext]*computrabajoSearchCall),
		deferred:    make(map[computrabajoContext]*computrabajoDeferredState),
	}
}

// Plan maps a neutral location intent to an exact catalog country and place.
func (s *ComputrabajoScraper) Plan(intent *locations.LocationIntent) PortalPlan {
	if !s.Covers(intent) {
		return PortalPlan{Reason: s.PauseReason(intent)}
	}
	code := ""
	if intent != nil {
		code = strings.ToUpper(strings.TrimSpace(intent.CountryCode))
	}
	noCountry := code == ""
	if noCountry {
		code = s.catalog.DefaultCountry
	}
	country, exists := s.catalog.CountriesByCode[code]
	if !exists {
		return PortalPlan{Reason: s.PauseReason(intent)}
	}
	note := ""
	if noCountry {
		note = fmt.Sprintf(
			"Computrabajo filtra por ubicación geográfica, no por modalidad: se buscará en %s (`%s.%s`).",
			country.Label, country.Subdomain, computrabajoBaseHost,
		)
	}
	if intent != nil && intent.Kind == "city" {
		if place, ok := s.findLocation(country, intent.City); ok {
			return PortalPlan{
				Supported:      true,
				SearchLocation: place.Label + ", " + country.Label,
				Note:           note,
			}
		}
		note = fmt.Sprintf(
			"%s No reconoció «%s» entre las ciudades registradas de %s; se buscará en todo el país.",
			note, intent.City, country.Label,
		)
	}
	return PortalPlan{Supported: true, SearchLocation: country.Label, Note: note}
}

func (s *ComputrabajoScraper) findLocation(
	country data.ComputrabajoCountry, value string,
) (data.ComputrabajoLocation, bool) {
	place, ok := s.catalog.LocationLookup[[2]string{
		country.Code, locations.Normalize(value),
	}]
	return place, ok
}

func (s *ComputrabajoScraper) resolveSearchLocation(location string) computrabajoResolved {
	normalized := locations.Normalize(location)
	country, rest := s.catalog.DefaultCountry, normalized
	for _, key := range s.catalog.CountryKeys {
		if normalized == key {
			country = s.catalog.CountryLookup[key]
			rest = ""
			break
		}
		if suffix := ", " + key; strings.HasSuffix(normalized, suffix) {
			country = s.catalog.CountryLookup[key]
			rest = strings.TrimSpace(strings.TrimSuffix(normalized, suffix))
			break
		}
	}
	resolved := computrabajoResolved{Country: s.catalog.CountriesByCode[country], Matched: rest == ""}
	if rest == "" {
		return resolved
	}
	if place, ok := s.findLocation(resolved.Country, rest); ok {
		resolved.Slug = place.Slug
		resolved.Matched = true
	}
	return resolved
}

// FetchJobs searches Computrabajo cards and enriches the recent ones with the
// portal's JobPosting details. Searches, details and failed details are cached
// across cycles and guilds.
func (s *ComputrabajoScraper) FetchJobs(
	ctx context.Context,
	keywords []string,
	location string,
	maxAgeHours float64,
	knownJobIDs map[string]struct{},
) ([]job.Job, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("computrabajo fetch jobs: %w", err)
	}
	terms := cleanBoardTerms(keywords)
	if len(terms) == 0 {
		return []job.Job{}, nil
	}

	now := s.now()
	cutoff := now.Add(-time.Duration(maxAgeHours * float64(time.Hour)))
	resolved := s.resolveSearchLocation(location)
	if !resolved.Matched {
		slog.Warn(
			"Computrabajo location is not in the location catalog; searching all of the country",
			"location", location, "country", resolved.Country.Label,
		)
	}
	subdomain := resolved.Country.Subdomain
	if subdomain == "" {
		subdomain = strings.ToLower(s.catalog.DefaultCountry)
	}
	headers := map[string]string{
		"User-Agent":      computrabajoUserAgent(s.random()),
		"Accept-Language": "es-LATAM,es;q=0.9,en;q=0.8",
	}
	s.fetcher.BeginCycle()
	slog.Info(fmt.Sprintf(
		"Computrabajo transport: proxy fallback (country=%s slug=%s)",
		subdomain, dashOrValue(resolved.Slug),
	))

	contextKey := computrabajoContext{
		TermsKey:  boardTermsKey(terms),
		Subdomain: subdomain,
		Slug:      resolved.Slug,
	}
	candidates, pagesFetched, foundCount, err := s.cachedCandidates(
		ctx, terms, subdomain, resolved.Slug, contextKey, headers,
	)
	if err != nil {
		return nil, err
	}

	known := knownJobIDs
	if known == nil {
		known = map[string]struct{}{}
	}
	knownCount := 0
	for _, id := range candidates.Order {
		if _, exists := known[id]; exists {
			knownCount++
		}
	}

	pendingOrder, pending, tooOldCount := s.buildPending(
		contextKey, candidates, known, cutoff,
	)
	enriched := make([]job.Job, 0, len(pendingOrder))
	remainingOrder := make([]string, 0)
	remaining := make(map[string]job.Job)
	cachedCount, fetchedCount := 0, 0
	withoutDetail, expiredCount, errorCount := 0, 0, 0

	for index, id := range pendingOrder {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("computrabajo detail pass: %w", err)
		}
		if s.fetcher.CircuitOpen() {
			computrabajoAppendRemaining(pendingOrder[index:], pending, &remainingOrder, remaining)
			break
		}
		if fetchedCount >= computrabajoMaxDetails {
			computrabajoAppendRemaining(pendingOrder[index:], pending, &remainingOrder, remaining)
			break
		}
		cardJob, exists := pending[id]
		if !exists {
			continue
		}
		if cached, ok := s.cachedDetail(id); ok {
			cachedCount++
			if computrabajoIsRecent(cached.Posted, cutoff) {
				enriched = append(enriched, cached.Job)
			} else {
				tooOldCount++
			}
			continue
		}

		response, err := s.fetcher.Fetch(ctx, httpclient.Request{
			URL:     cardJob.URL,
			Headers: headers,
			Retries: computrabajoRetryCount(2),
			Label:   "job " + id,
		})
		fetchedCount++
		if err != nil {
			errorCount++
			remaining[id] = cardJob
			remainingOrder = append(remainingOrder, id)
			continue
		}
		if response.StatusCode == http.StatusNotFound {
			expiredCount++
			continue
		}
		if !response.IsSuccess() {
			errorCount++
			remaining[id] = cardJob
			remainingOrder = append(remainingOrder, id)
			continue
		}

		detail := computrabajoParseDetail(response.Text())
		if detail == nil {
			withoutDetail++
			slog.Warn(
				"Computrabajo job has no JobPosting data; using the visible page as fallback",
				"job", id,
			)
			enriched = append(enriched, computrabajoApplyFallback(
				cardJob, computrabajoFallbackDetail(response.Text()),
			))
			continue
		}
		detailed, posted, ok := computrabajoJobFromDetail(cardJob, detail, s.catalog.LogoBaseURL)
		if !ok {
			slog.Warn("Computrabajo job has no parseable date; skipping", "job", id)
			errorCount++
			continue
		}
		s.storeDetail(id, detailed, posted)
		if computrabajoIsRecent(posted, cutoff) {
			enriched = append(enriched, detailed)
		} else {
			tooOldCount++
		}
		if err := s.sleep(ctx, s.detailDelay()); err != nil {
			return nil, fmt.Errorf("computrabajo detail delay: %w", err)
		}
	}

	nowMonotonic := s.cacheClock()
	s.storeDeferred(contextKey, remainingOrder, remaining, nowMonotonic)
	s.pruneDeferred(nowMonotonic)
	slog.Info(fmt.Sprintf(
		"Computrabajo cycle: keywords=%d pages=%d found=%d unique=%d known=%d cached=%d fetched=%d without_detail=%d deferred=%d too_old=%d expired=%d errors=%d",
		len(terms), pagesFetched, foundCount, len(candidates.Records), knownCount,
		cachedCount, fetchedCount, withoutDetail, len(remaining), tooOldCount, expiredCount, errorCount,
	))
	return enriched, nil
}

func dashOrValue(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func (s *ComputrabajoScraper) now() time.Time { return s.clock().UTC() }

func computrabajoRetryCount(retries int) *int { return &retries }

func computrabajoUserAgent(value float64) string {
	index := int(math.Floor(value * float64(len(computrabajoUserAgents))))
	if index < 0 {
		index = 0
	}
	if index >= len(computrabajoUserAgents) {
		index = len(computrabajoUserAgents) - 1
	}
	return computrabajoUserAgents[index]
}

func (s *ComputrabajoScraper) detailDelay() time.Duration {
	return computrabajoDelay(computrabajoDetailMinDelay, computrabajoDetailMaxDelay, s.random())
}

func (s *ComputrabajoScraper) searchDelay() time.Duration {
	return computrabajoDelay(computrabajoSearchMinDelay, computrabajoSearchMaxDelay, s.random())
}

func computrabajoDelay(minimum, maximum time.Duration, value float64) time.Duration {
	if math.IsNaN(value) {
		value = 0.5
	}
	if value < 0 {
		value = 0
	}
	if value > 1 {
		value = 1
	}
	span := maximum - minimum
	return minimum + time.Duration(value*float64(span))
}

func computrabajoIsRecent(posted, cutoff time.Time) bool {
	return !posted.UTC().Before(cutoff)
}

func computrabajoAppendRemaining(
	ids []string, pending map[string]job.Job,
	order *[]string, remaining map[string]job.Job,
) {
	for _, id := range ids {
		if _, exists := pending[id]; !exists {
			continue
		}
		if _, exists := remaining[id]; exists {
			continue
		}
		remaining[id] = pending[id]
		*order = append(*order, id)
	}
}

func (s *ComputrabajoScraper) buildPending(
	contextKey computrabajoContext,
	candidates computrabajoCandidateSet,
	known map[string]struct{},
	cutoff time.Time,
) ([]string, map[string]job.Job, int) {
	deferredOrder, deferredItems := s.deferredSnapshot(
		contextKey, s.cacheClock(), cutoff, known,
	)
	pending := make(map[string]job.Job, len(deferredOrder)+len(candidates.Order))
	order := make([]string, 0, len(deferredOrder)+len(candidates.Order))
	for _, id := range deferredOrder {
		entry, exists := deferredItems[id]
		if !exists {
			continue
		}
		pending[id] = entry.Job
		order = append(order, id)
	}
	tooOldCount := 0
	for _, id := range candidates.Order {
		record, exists := candidates.Records[id]
		if !exists {
			continue
		}
		if _, exists := known[id]; exists {
			continue
		}
		if _, exists := pending[id]; exists {
			continue
		}
		if posted := record.PublishedAtDatetime; posted != nil && !computrabajoIsRecent(*posted, cutoff) {
			tooOldCount++
			continue
		}
		pending[id] = record
		order = append(order, id)
	}
	return order, pending, tooOldCount
}

func (s *ComputrabajoScraper) deferredSnapshot(
	contextKey computrabajoContext,
	now, cutoff time.Time,
	known map[string]struct{},
) ([]string, map[string]computrabajoDeferredEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.deferred[contextKey]
	if state == nil {
		return nil, nil
	}
	order := make([]string, 0, len(state.Order))
	items := make(map[string]computrabajoDeferredEntry, len(state.Order))
	for _, id := range state.Order {
		entry, exists := state.Items[id]
		if !exists || !entry.ExpiresAt.After(now) {
			continue
		}
		if _, exists := known[id]; exists {
			continue
		}
		if posted := entry.Job.PublishedAtDatetime; posted != nil && !computrabajoIsRecent(*posted, cutoff) {
			continue
		}
		order = append(order, id)
		items[id] = entry
	}
	return order, items
}

func (s *ComputrabajoScraper) cachedCandidates(
	ctx context.Context,
	terms []string,
	subdomain, slug string,
	key computrabajoContext,
	headers map[string]string,
) (computrabajoCandidateSet, int, int, error) {
	now := s.cacheClock()
	s.mu.Lock()
	if cached, ok := s.searchCache[key]; ok && cached.ExpiresAt.After(now) {
		s.mu.Unlock()
		return cached.Candidates.clone(), cached.PagesFetched, cached.FoundCount, nil
	}
	if call, ok := s.searchCalls[key]; ok {
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return computrabajoCandidateSet{}, 0, 0, fmt.Errorf("wait for Computrabajo search: %w", ctx.Err())
		case <-call.done:
			return call.candidates.clone(), call.pages, call.found, call.err
		}
	}
	call := &computrabajoSearchCall{done: make(chan struct{})}
	s.searchCalls[key] = call
	s.mu.Unlock()

	candidates, pages, found, complete, err := s.searchCandidates(
		ctx, terms, subdomain, slug, headers,
	)
	if err == nil && complete {
		expiresAt := s.cacheClock().Add(computrabajoSearchTTL)
		s.mu.Lock()
		s.pruneSearchCache(s.cacheClock())
		s.searchCache[key] = computrabajoCachedSearch{
			ExpiresAt:    expiresAt,
			Candidates:   candidates.clone(),
			PagesFetched: pages,
			FoundCount:   found,
		}
		s.mu.Unlock()
	}
	call.candidates = candidates.clone()
	call.pages = pages
	call.found = found
	call.complete = complete
	call.err = err
	s.mu.Lock()
	delete(s.searchCalls, key)
	s.mu.Unlock()
	close(call.done)
	return candidates, pages, found, err
}

func (s *ComputrabajoScraper) pruneSearchCache(now time.Time) {
	for key, cached := range s.searchCache {
		if !cached.ExpiresAt.After(now) {
			delete(s.searchCache, key)
		}
	}
}

func (s *ComputrabajoScraper) searchCandidates(
	ctx context.Context,
	terms []string,
	subdomain, slug string,
	headers map[string]string,
) (computrabajoCandidateSet, int, int, bool, error) {
	candidates := newComputrabajoCandidateSet()
	pagesFetched, foundCount := 0, 0
	complete := true
	baseURL := "https://" + subdomain + "." + computrabajoBaseHost

	for _, keyword := range terms {
		keywordSlug := computrabajoSlug(keyword)
		if keywordSlug == "" {
			continue
		}
		path := "/trabajo-de-" + keywordSlug
		if slug != "" {
			path += "-en-" + slug
		}
		maxPages := computrabajoMaxPages
	pages:
		for page := 1; page <= maxPages; page++ {
			if err := ctx.Err(); err != nil {
				return computrabajoCandidateSet{}, 0, 0, false, fmt.Errorf("computrabajo search: %w", err)
			}
			if s.fetcher.CircuitOpen() {
				slog.Warn(
					"Computrabajo search circuit open; keeping jobs from previous keywords",
					"retained", len(candidates.Records),
				)
				complete = false
				break pages
			}
			params := map[string]string{"by": "publicationtime"}
			if page > 1 {
				params["p"] = strconv.Itoa(page)
			}
			response, err := s.fetcher.Fetch(ctx, httpclient.Request{
				URL:     baseURL + path,
				Params:  params,
				Headers: headers,
				Retries: computrabajoRetryCount(2),
				Label:   fmt.Sprintf("search '%s' page %d", keyword, page),
			})
			if err != nil {
				slog.Warn(
					"Computrabajo search stopped for keyword at page",
					"keyword", keyword, "page", page, "error", err,
				)
				complete = false
				break pages
			}
			if !response.IsSuccess() {
				slog.Warn(
					"Computrabajo search returned a non-success status",
					"keyword", keyword, "page", page, "status", response.StatusCode,
				)
				complete = false
				break pages
			}
			if computrabajoLooksBlocked(response.Text()) {
				slog.Warn(
					"Computrabajo returned a challenge page",
					"keyword", keyword, "page", page,
				)
				complete = false
				break pages
			}

			pageJobs := computrabajoParseCards(response.Text(), baseURL, s.now())
			pagesFetched++
			foundCount += len(pageJobs)
			if page == 1 {
				if total, ok := computrabajoParseTotal(response.Text()); ok {
					pageCount := int(math.Ceil(float64(total) / float64(computrabajoPageSize)))
					if pageCount < 1 {
						pageCount = 1
					}
					if pageCount < maxPages {
						maxPages = pageCount
					}
				}
			}
			if len(pageJobs) == 0 {
				break
			}
			newJobs := 0
			for _, record := range pageJobs {
				if _, exists := candidates.Records[record.ID]; exists {
					continue
				}
				candidates.Records[record.ID] = record
				candidates.Order = append(candidates.Order, record.ID)
				newJobs++
			}
			if page >= maxPages || len(pageJobs) < computrabajoPageSize || newJobs == 0 {
				break
			}
			if err := s.sleep(ctx, s.searchDelay()); err != nil {
				return computrabajoCandidateSet{}, 0, 0, false, fmt.Errorf("computrabajo search delay: %w", err)
			}
		}
	}
	return candidates, pagesFetched, foundCount, complete, nil
}

func (s *ComputrabajoScraper) cachedDetail(id string) (computrabajoDetailEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, exists := s.detailCache[id]
	if !exists {
		return computrabajoDetailEntry{}, false
	}
	if !entry.ExpiresAt.After(s.cacheClock()) {
		delete(s.detailCache, id)
		s.removeDetailOrder(id)
		return computrabajoDetailEntry{}, false
	}
	return entry, true
}

func (s *ComputrabajoScraper) removeDetailOrder(id string) {
	remaining := s.detailOrder[:0]
	for _, value := range s.detailOrder {
		if value != id {
			remaining = append(remaining, value)
		}
	}
	s.detailOrder = remaining
}

func (s *ComputrabajoScraper) storeDetail(
	id string, detailed job.Job, posted time.Time,
) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.cacheClock()
	if len(s.detailCache) >= computrabajoDetailLimit {
		for _, key := range s.detailOrder {
			entry := s.detailCache[key]
			if !entry.ExpiresAt.After(now) {
				delete(s.detailCache, key)
			}
		}
		remaining := s.detailOrder[:0]
		for _, key := range s.detailOrder {
			if _, exists := s.detailCache[key]; exists {
				remaining = append(remaining, key)
			}
		}
		s.detailOrder = remaining
		for len(s.detailCache) >= computrabajoDetailLimit && len(s.detailOrder) > 0 {
			oldest := s.detailOrder[0]
			delete(s.detailCache, oldest)
			s.detailOrder = s.detailOrder[1:]
		}
	}
	if _, exists := s.detailCache[id]; !exists {
		s.detailOrder = append(s.detailOrder, id)
	}
	s.detailCache[id] = computrabajoDetailEntry{
		ExpiresAt: now.Add(computrabajoDetailTTL),
		Job:       detailed,
		Posted:    posted,
	}
}

func (s *ComputrabajoScraper) storeDeferred(
	key computrabajoContext, order []string, items map[string]job.Job, now time.Time,
) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(order) == 0 {
		delete(s.deferred, key)
		return
	}
	if len(order) > computrabajoDeferredLimit {
		order = order[:computrabajoDeferredLimit]
	}
	state := &computrabajoDeferredState{
		Order: append([]string(nil), order...),
		Items: make(map[string]computrabajoDeferredEntry, len(order)),
	}
	for _, id := range order {
		state.Items[id] = computrabajoDeferredEntry{
			ExpiresAt: now.Add(computrabajoDeferredTTL),
			Job:       items[id],
		}
	}
	s.deferred[key] = state
}

func (s *ComputrabajoScraper) pruneDeferred(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, state := range s.deferred {
		order := state.Order[:0]
		alive := make(map[string]computrabajoDeferredEntry, len(state.Items))
		for _, id := range state.Order {
			entry, exists := state.Items[id]
			if !exists || !entry.ExpiresAt.After(now) {
				continue
			}
			order = append(order, id)
			alive[id] = entry
		}
		if len(order) == 0 {
			delete(s.deferred, key)
			continue
		}
		state.Order = order
		state.Items = alive
	}
	for len(s.deferred) > computrabajoDeferredMaxCtx {
		var oldestKey computrabajoContext
		oldestExpiry := time.Time{}
		haveOldest := false
		for key, state := range s.deferred {
			expiry := time.Time{}
			first := true
			for _, id := range state.Order {
				entry := state.Items[id]
				if first || entry.ExpiresAt.Before(expiry) {
					expiry = entry.ExpiresAt
					first = false
				}
			}
			if !haveOldest || expiry.Before(oldestExpiry) ||
				(expiry.Equal(oldestExpiry) && key.key() < oldestKey.key()) {
				oldestKey = key
				oldestExpiry = expiry
				haveOldest = true
			}
		}
		delete(s.deferred, oldestKey)
	}
}

func computrabajoSlug(value string) string {
	return strings.Trim(computrabajoSlugPattern.ReplaceAllString(
		locations.Normalize(value), "-",
	), "-")
}

func computrabajoSearchPath(keyword, slug string) string {
	path := "/trabajo-de-" + computrabajoSlug(keyword)
	if slug == "" {
		return path
	}
	return path + "-en-" + slug
}

func computrabajoText(selection *goquery.Selection) string {
	if selection == nil {
		return ""
	}
	parts := make([]string, 0, 8)
	collectBoardText(selection, &parts)
	return strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
}

func computrabajoResolveURL(value, baseURL string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	base, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	reference, err := url.Parse(value)
	if err != nil {
		return ""
	}
	resolved := base.ResolveReference(reference)
	if !isHTTPScheme(resolved.Scheme) {
		return ""
	}
	return resolved.String()
}

func computrabajoOrUnspecified(value string) string {
	if strings.TrimSpace(value) == "" {
		return "No especificada"
	}
	return value
}

func computrabajoLooksBlocked(raw string) bool {
	if len(raw) > 4000 {
		raw = raw[:4000]
	}
	sample := strings.ToLower(raw)
	for _, marker := range computrabajoBlockMarkers {
		if strings.Contains(sample, marker) {
			return true
		}
	}
	return false
}

func computrabajoParseTotal(raw string) (int, bool) {
	match := computrabajoTotalPattern.FindStringSubmatch(raw)
	if match == nil {
		return 0, false
	}
	total, err := strconv.Atoi(strings.ReplaceAll(match[1], ".", ""))
	if err != nil {
		return 0, false
	}
	return total, true
}

func computrabajoParseCards(raw, baseURL string, now time.Time) []job.Job {
	document, err := goquery.NewDocumentFromReader(strings.NewReader(raw))
	if err != nil {
		return nil
	}
	results := make([]job.Job, 0, 16)
	seen := make(map[string]struct{})
	document.Find("article.box_offer").Each(func(_ int, card *goquery.Selection) {
		id, exists := card.Attr("data-id")
		id = strings.TrimSpace(id)
		if !exists || id == "" {
			return
		}
		if _, exists := seen[id]; exists {
			return
		}
		link := card.Find("h2 a.js-o-link").First()
		if link.Length() == 0 {
			link = card.Find("h2 a").First()
		}
		href, exists := link.Attr("href")
		if !exists || strings.TrimSpace(href) == "" {
			return
		}
		title := computrabajoText(link)
		if title == "" {
			return
		}
		jobURL := computrabajoResolveURL(strings.TrimSpace(strings.SplitN(href, "#", 2)[0]), baseURL)
		if jobURL == "" {
			return
		}

		company := computrabajoText(card.Find("p.dFlex a").First())
		location := ""
		card.Find("p.fs16.fc_base.mt5").EachWithBreak(func(_ int, element *goquery.Selection) bool {
			if element.HasClass("dFlex") {
				return true
			}
			location = computrabajoText(element)
			return false
		})
		modality := computrabajoText(card.Find("div.fs13.mt15").First())
		foldedModality := strings.ToLower(modality)
		if strings.Contains(foldedModality, "remoto") || strings.Contains(foldedModality, "teletrabajo") {
			if location == "" {
				location = "Remoto"
			} else {
				location += computrabajoRemoteSuffix
			}
		}
		published := computrabajoText(card.Find("p.fs13.fc_aux.mt15").First())

		seen[id] = struct{}{}
		results = append(results, job.Job{
			ID:                  id,
			Title:               title,
			Company:             computrabajoOrUnspecified(company),
			Location:            computrabajoOrUnspecified(location),
			URL:                 jobURL,
			Source:              ComputrabajoSource,
			PublishedAt:         published,
			PublishedAtDatetime: computrabajoRelativeDate(published, now),
		})
	})
	return results
}

func computrabajoRelativeDate(text string, now time.Time) *time.Time {
	normalized := locations.Normalize(text)
	if normalized == "" {
		return nil
	}
	resolve := func(value time.Time) *time.Time { return &value }
	switch {
	case strings.Contains(normalized, "minuto"):
		value := computrabajoFirstNumber(normalized)
		result := now.Add(-time.Duration(value) * time.Minute)
		return resolve(result)
	case strings.Contains(normalized, "hora"):
		value := computrabajoFirstNumber(normalized)
		result := now.Add(-time.Duration(value) * time.Hour)
		return resolve(result)
	case strings.Contains(normalized, "ayer"):
		result := now.Add(-24 * time.Hour)
		return resolve(result)
	case strings.Contains(normalized, "hoy"):
		result := now
		return resolve(result)
	case strings.Contains(normalized, "dia"):
		value := computrabajoFirstNumber(normalized)
		if !computrabajoHasNumber(normalized) {
			return nil
		}
		result := now.Add(-time.Duration(value) * 24 * time.Hour)
		return resolve(result)
	}
	match := computrabajoMonthPattern.FindStringSubmatch(normalized)
	if match == nil {
		return nil
	}
	day, err := strconv.Atoi(match[1])
	if err != nil {
		return nil
	}
	month, exists := computrabajoMonths[match[2]]
	if !exists {
		return nil
	}
	candidate := time.Date(now.Year(), month, day, 0, 0, 0, 0, time.UTC)
	if candidate.After(now) {
		candidate = candidate.AddDate(-1, 0, 0)
	}
	return resolve(candidate)
}

func computrabajoHasNumber(value string) bool {
	return computrabajoDigitsPattern.MatchString(value)
}

func computrabajoFirstNumber(value string) int {
	match := computrabajoDigitsPattern.FindString(value)
	if match == "" {
		return 0
	}
	number, err := strconv.Atoi(match)
	if err != nil {
		return 0
	}
	return number
}

func computrabajoObject(value any) map[string]any {
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	return object
}

func computrabajoString(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return text
}

func computrabajoUnescape(value any) string {
	return html.UnescapeString(strings.TrimSpace(computrabajoString(value)))
}

func computrabajoParseDetail(raw string) map[string]any {
	document, err := goquery.NewDocumentFromReader(strings.NewReader(raw))
	if err != nil {
		return nil
	}
	var result map[string]any
	document.Find("script[type='application/ld+json']").EachWithBreak(
		func(_ int, script *goquery.Selection) bool {
			text := strings.TrimSpace(script.Text())
			if text == "" {
				return true
			}
			payload, err := decodeBoardJSON([]byte(text))
			if err != nil {
				return true
			}
			items := []any{payload}
			object := computrabajoObject(payload)
			if object != nil {
				if graph, exists := object["@graph"]; exists {
					items = nil
					if list, ok := graph.([]any); ok {
						items = list
					} else if graphObject := computrabajoObject(graph); graphObject != nil {
						items = []any{graphObject}
					}
				} else {
					items = []any{payload}
				}
			}
			if list, ok := payload.([]any); ok && object == nil {
				items = list
			}
			for _, item := range items {
				posting := computrabajoObject(item)
				if posting == nil {
					continue
				}
				if computrabajoIsJobPosting(posting["@type"]) {
					result = posting
					return false
				}
			}
			return true
		},
	)
	return result
}

func computrabajoIsJobPosting(value any) bool {
	if text, ok := value.(string); ok {
		return text == "JobPosting"
	}
	values, ok := value.([]any)
	if !ok {
		return false
	}
	for _, item := range values {
		if text, ok := item.(string); ok && text == "JobPosting" {
			return true
		}
	}
	return false
}

func computrabajoDescriptionText(value any) string {
	raw := computrabajoUnescape(value)
	if raw == "" {
		return ""
	}
	document, err := goquery.NewDocumentFromReader(strings.NewReader(raw))
	if err != nil {
		return strings.Join(strings.Fields(raw), " ")
	}
	lines := make([]string, 0, 8)
	computrabajoCollectLines(document.Selection, &lines)
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		if line != "" {
			result = append(result, line)
		}
	}
	return strings.Join(result, "\n")
}

func computrabajoCollectLines(selection *goquery.Selection, lines *[]string) {
	if selection == nil {
		return
	}
	selection.Contents().Each(func(_ int, child *goquery.Selection) {
		switch goquery.NodeName(child) {
		case "#text":
			for _, line := range strings.Split(child.Text(), "\n") {
				if text := strings.Join(strings.Fields(line), " "); text != "" {
					*lines = append(*lines, text)
				}
			}
		case "#comment":
			return
		default:
			computrabajoCollectLines(child, lines)
		}
	})
}

func computrabajoFallbackDetail(raw string) map[string]any {
	document, err := goquery.NewDocumentFromReader(strings.NewReader(raw))
	if err != nil {
		return map[string]any{}
	}
	descriptionLines := make([]string, 0, 8)
	document.Find("div.box_detail div.mb40.pb40.bb1 p").Each(func(_ int, paragraph *goquery.Selection) {
		if text := computrabajoText(paragraph); text != "" {
			descriptionLines = append(descriptionLines, text)
		}
	})
	company, location := "", ""
	header := document.Find("main.detail_fs div.container p.fs16").First()
	if header.Length() > 0 {
		parts := make([]string, 0, 4)
		for _, part := range strings.Split(computrabajoText(header), " - ") {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				parts = append(parts, trimmed)
			}
		}
		if len(parts) >= 2 {
			company = parts[0]
			location = strings.Join(parts[1:], " - ")
		}
	}
	return map[string]any{
		"description": strings.Join(descriptionLines, "\n"),
		"company":     company,
		"location":    location,
	}
}

func computrabajoApplyFallback(card job.Job, fallback map[string]any) job.Job {
	enriched := card
	if description := computrabajoUnescape(fallback["description"]); description != "" {
		enriched.Description = description
	}
	if company := computrabajoUnescape(fallback["company"]); company != "" {
		enriched.Company = company
	}
	if location := computrabajoUnescape(fallback["location"]); location != "" {
		enriched.Location = location
	}
	return enriched
}

func computrabajoParsePostedDate(value any) (time.Time, bool) {
	text := strings.TrimSpace(computrabajoString(value))
	if text == "" {
		return time.Time{}, false
	}
	text = strings.ReplaceAll(text, "Z", "+00:00")
	for _, layout := range []string{
		time.RFC3339,
		"2006-01-02T15:04:05.999999999",
		"2006-01-02T15:04:05",
		"2006-01-02T15:04",
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05Z07:00",
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		"2006-01-02",
	} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}

func computrabajoJobFromDetail(
	card job.Job, detail map[string]any, logoBaseURL string,
) (job.Job, time.Time, bool) {
	var posted time.Time
	if card.PublishedAtDatetime != nil {
		posted = *card.PublishedAtDatetime
	} else {
		parsed, ok := computrabajoParsePostedDate(detail["datePosted"])
		if !ok {
			return job.Job{}, time.Time{}, false
		}
		posted = parsed
	}

	organization := computrabajoObject(detail["hiringOrganization"])
	jobLocation := computrabajoObject(detail["jobLocation"])
	address := computrabajoObject(jobLocation["address"])

	title := computrabajoUnescape(detail["title"])
	if title == "" {
		title = card.Title
	}
	company := computrabajoUnescape(organization["name"])
	if company == "" {
		company = card.Company
	}
	logoURL := ""
	if rawLogo := computrabajoUnescape(organization["logo"]); rawLogo != "" {
		logoURL = computrabajoResolveURL(rawLogo, logoBaseURL)
	}

	locality := computrabajoUnescape(address["addressLocality"])
	region := computrabajoUnescape(address["addressRegion"])
	detailLocationParts := make([]string, 0, 2)
	for _, value := range []string{locality, region} {
		if value != "" {
			detailLocationParts = append(detailLocationParts, value)
		}
	}
	detailLocation := strings.Join(detailLocationParts, ", ")
	location := card.Location
	if detailLocation != "" && strings.Contains(strings.ToLower(location), "no especificada") {
		location = detailLocation
	}
	if strings.EqualFold(strings.TrimSpace(computrabajoString(detail["jobLocationType"])), "TELECOMMUTE") {
		if !strings.Contains(location, strings.TrimSpace(computrabajoRemoteSuffix)) {
			if location == "" || location == "No especificada" {
				location = "Remoto"
			} else {
				location += computrabajoRemoteSuffix
			}
		}
	}

	description := computrabajoDescriptionText(detail["description"])
	if description == "" {
		description = card.Description
	}

	enriched := card
	enriched.Title = title
	enriched.Company = computrabajoOrUnspecified(company)
	enriched.Location = computrabajoOrUnspecified(location)
	if enriched.CompanyLogoURL == "" {
		enriched.CompanyLogoURL = logoURL
	}
	enriched.Description = description
	enriched.PublishedAtDatetime = &posted
	return enriched, posted, true
}

var _ Scraper = (*ComputrabajoScraper)(nil)
