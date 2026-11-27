package scrapers

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"math/rand"
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
	// FirstJobSlug is the stable identifier of the portal.
	FirstJobSlug = "firstjob"
	// FirstJobSource is the human-readable name shown in notifications.
	FirstJobSource = "FirstJob"

	firstJobBaseHost = "firstjob.me"
	firstJobBaseURL  = "https://" + firstJobBaseHost
	// FirstJobProxyMode describes the Worker fallback transport.
	FirstJobProxyMode = "fallback"

	firstJobPageSize       = 20
	firstJobMaxPages       = 3
	firstJobMaxDetails     = 25
	firstJobDetailMinDelay = 400 * time.Millisecond
	firstJobDetailMaxDelay = time.Second
	firstJobSearchMinDelay = 600 * time.Millisecond
	firstJobSearchMaxDelay = 1200 * time.Millisecond
	firstJobDetailTTL      = 6 * time.Hour
	firstJobDetailLimit    = 500
	firstJobSearchTTL      = 4 * time.Minute
	firstJobDeferredTTL    = 3 * time.Hour
	firstJobDeferredLimit  = 200
	firstJobDeferredMaxCtx = 50
	firstJobRemoteSuffix   = " (Remoto)"

	firstJobNoDatesNote = "FirstJob no publica fechas de publicación: no se aplica `max_age` y los duplicados se controlan por ID de aviso."
)

var (
	firstJobOfferIDPattern = regexp.MustCompile(`/oferta/(\d+)`)
	firstJobBlockMarkers   = []string{
		"captcha", "no soy un robot", "verifica que eres humano",
		"acceso denegado", "request blocked", "unusual traffic",
	}
	firstJobUserAgents = []string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Safari/537.36",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15",
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Safari/537.36",
	}
)

type firstJobCatalogView struct {
	Catalog          data.FirstJobCatalog
	CountriesByCode  map[string]data.FirstJobCountry
	CountryToCatalog map[string]string
	GlobalByCode     map[string]data.Country
	GlobalNameToCode map[string]string
	CountryLookup    map[string]string
	CountryKeys      []string
	RegionLookup     map[[2]string]data.FirstJobRegion
	DefaultCountry   string
	CoveredCodes     []string
}

var (
	firstJobCatalogOnce sync.Once
	firstJobCatalog     *firstJobCatalogView
)

func loadFirstJobCatalog() (*firstJobCatalogView, error) {
	var err error
	firstJobCatalogOnce.Do(func() {
		catalog, loadErr := data.Load(context.Background())
		if loadErr != nil {
			err = fmt.Errorf("load FirstJob catalog: %w", loadErr)
			return
		}
		firstJobCatalog = buildFirstJobCatalogView(catalog)
	})
	if err != nil || firstJobCatalog == nil {
		return nil, err
	}
	return firstJobCatalog, nil
}

func buildFirstJobCatalogView(catalog *data.Catalog) *firstJobCatalogView {
	firstJob := catalog.FirstJob()
	view := &firstJobCatalogView{
		Catalog:          firstJob,
		CountriesByCode:  make(map[string]data.FirstJobCountry, len(firstJob.Countries)),
		CountryToCatalog: make(map[string]string),
		GlobalByCode:     make(map[string]data.Country),
		GlobalNameToCode: make(map[string]string),
		CountryLookup:    make(map[string]string),
		RegionLookup:     make(map[[2]string]data.FirstJobRegion),
		DefaultCountry:   strings.ToUpper(strings.TrimSpace(firstJob.DefaultCountry)),
	}
	if view.DefaultCountry == "" {
		view.DefaultCountry = "CL"
	}

	for _, country := range catalog.Countries() {
		view.GlobalByCode[country.Code] = country
		if _, exists := view.GlobalNameToCode[locations.Normalize(country.NameES)]; !exists {
			view.GlobalNameToCode[locations.Normalize(country.NameES)] = country.Code
		}
	}

	countryOrder := make([]string, 0, len(view.CountryLookup))
	for _, country := range firstJob.Countries {
		view.CountriesByCode[country.Code] = country
		view.CountryToCatalog[country.Code] = country.Code
		for _, covered := range country.CountryCodesCovered {
			if _, exists := view.GlobalByCode[covered]; exists {
				view.CountryToCatalog[covered] = country.Code
			}
		}
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
	}
	// Names are a fallback: explicit ISO codes above take precedence.
	for _, country := range firstJob.Countries {
		for _, name := range country.CountriesCovered {
			code, exists := view.GlobalNameToCode[locations.Normalize(name)]
			if !exists {
				continue
			}
			if _, mapped := view.CountryToCatalog[code]; !mapped {
				view.CountryToCatalog[code] = country.Code
			}
		}
		for _, region := range country.Regions {
			for _, name := range append([]string{region.Label}, region.Aliases...) {
				key := locations.Normalize(name)
				if key == "" {
					continue
				}
				lookupKey := [2]string{country.Code, key}
				if _, exists := view.RegionLookup[lookupKey]; !exists {
					view.RegionLookup[lookupKey] = region
				}
			}
		}
	}
	view.CountryKeys = append(view.CountryKeys, countryOrder...)
	sort.SliceStable(view.CountryKeys, func(i, j int) bool {
		return len(view.CountryKeys[i]) > len(view.CountryKeys[j])
	})
	view.CoveredCodes = make([]string, 0, len(view.CountryToCatalog))
	for code := range view.CountryToCatalog {
		view.CoveredCodes = append(view.CoveredCodes, code)
	}
	sort.Strings(view.CoveredCodes)
	return view
}

func emptyFirstJobCatalogView() *firstJobCatalogView {
	return &firstJobCatalogView{
		CountriesByCode:  make(map[string]data.FirstJobCountry),
		CountryToCatalog: make(map[string]string),
		GlobalByCode:     make(map[string]data.Country),
		GlobalNameToCode: make(map[string]string),
		CountryLookup:    make(map[string]string),
		RegionLookup:     make(map[[2]string]data.FirstJobRegion),
		DefaultCountry:   "CL",
	}
}

// FirstJobProxyHosts returns the fixed Worker fallback hosts.
func FirstJobProxyHosts() []string {
	return []string{firstJobBaseHost, "www." + firstJobBaseHost}
}

// FirstJobOptions holds the deterministic dependencies of the scraper.
type FirstJobOptions struct {
	Fetcher    httpclient.Fetcher
	Clock      func() time.Time
	CacheClock func() time.Time
	Sleep      func(context.Context, time.Duration) error
	Random     func() float64
}

type firstJobCandidateSet struct {
	Records map[string]job.Job
	Order   []string
}

func newFirstJobCandidateSet() firstJobCandidateSet {
	return firstJobCandidateSet{Records: make(map[string]job.Job)}
}

func (c firstJobCandidateSet) clone() firstJobCandidateSet {
	clone := newFirstJobCandidateSet()
	for id, record := range c.Records {
		clone.Records[id] = record
	}
	clone.Order = append(clone.Order, c.Order...)
	return clone
}

type firstJobContext struct {
	TermsKey    string
	CountryCode string
	RegionID    string
}

func (c firstJobContext) key() string {
	return c.TermsKey + "\x00" + c.CountryCode + "\x00" + c.RegionID
}

type firstJobCachedSearch struct {
	ExpiresAt  time.Time
	Candidates firstJobCandidateSet
	Pages      int
	Found      int
}

type firstJobSearchCall struct {
	done       chan struct{}
	candidates firstJobCandidateSet
	pages      int
	found      int
	complete   bool
	err        error
}

type firstJobDetailEntry struct {
	ExpiresAt time.Time
	Job       job.Job
}

type firstJobDeferredEntry struct {
	ExpiresAt time.Time
	Job       job.Job
}

type firstJobDeferredState struct {
	Order []string
	Items map[string]firstJobDeferredEntry
}

type firstJobResolved struct {
	Country  data.FirstJobCountry
	RegionID string
	Matched  bool
}

// FirstJobScraper searches FirstJob cards and enriches every pending job with
// the visible detail page. FirstJob publishes no dates, so max age is ignored.
type FirstJobScraper struct {
	BaseScraper

	fetcher    httpclient.Fetcher
	clock      func() time.Time
	cacheClock func() time.Time
	sleep      func(context.Context, time.Duration) error
	random     func() float64
	catalog    *firstJobCatalogView

	mu          sync.Mutex
	detailCache map[string]firstJobDetailEntry
	detailOrder []string
	searchCache map[firstJobContext]firstJobCachedSearch
	searchCalls map[firstJobContext]*firstJobSearchCall
	deferred    map[firstJobContext]*firstJobDeferredState
}

// NewFirstJobScraper builds a scraper. Missing dependencies receive the
// production defaults; tests inject a replay fetcher, clocks and sleeper.
func NewFirstJobScraper(options FirstJobOptions) *FirstJobScraper {
	fetcher := options.Fetcher
	if fetcher == nil {
		fetcher = httpclient.NewClient(httpclient.Options{
			Source:     FirstJobSource,
			ProxyMode:  FirstJobProxyMode,
			ProxyHosts: FirstJobProxyHosts(),
		})
	}
	clock := options.Clock
	if clock == nil {
		clock = time.Now
	}
	cacheClock := options.CacheClock
	if cacheClock == nil {
		cacheClock = clock
	}
	sleeper := options.Sleep
	if sleeper == nil {
		sleeper = sleepContext
	}
	random := options.Random
	if random == nil {
		random = rand.Float64
	}
	catalog, err := loadFirstJobCatalog()
	if err != nil {
		slog.Warn("FirstJob built-in location catalog is unavailable", "error", err)
		catalog = emptyFirstJobCatalogView()
	}
	return &FirstJobScraper{
		BaseScraper: NewBaseScraper(
			FirstJobSlug, FirstJobSource, append([]string(nil), catalog.CoveredCodes...),
		),
		fetcher:     fetcher,
		clock:       clock,
		cacheClock:  cacheClock,
		sleep:       sleeper,
		random:      random,
		catalog:     catalog,
		detailCache: make(map[string]firstJobDetailEntry),
		searchCache: make(map[firstJobContext]firstJobCachedSearch),
		searchCalls: make(map[firstJobContext]*firstJobSearchCall),
		deferred:    make(map[firstJobContext]*firstJobDeferredState),
	}
}

// Plan maps a neutral location intent to FirstJob's country and region ids.
func (s *FirstJobScraper) Plan(intent *locations.LocationIntent) PortalPlan {
	if !s.Covers(intent) {
		return PortalPlan{Reason: s.PauseReason(intent)}
	}
	sourceCode := ""
	if intent != nil {
		sourceCode = strings.ToUpper(strings.TrimSpace(intent.CountryCode))
	}
	noCountry := sourceCode == ""
	code := sourceCode
	if noCountry {
		code = s.catalog.DefaultCountry
	}
	catalogCode := s.catalog.CountryToCatalog[code]
	if catalogCode == "" {
		catalogCode = s.catalog.DefaultCountry
	}
	country, exists := s.catalog.CountriesByCode[catalogCode]
	if !exists {
		return PortalPlan{Reason: s.PauseReason(intent)}
	}
	grouped := catalogCode != sourceCode
	note := ""
	switch {
	case noCountry:
		note = fmt.Sprintf(
			"FirstJob no filtra por modalidad: se buscará en %s por defecto.",
			country.Label,
		)
	case grouped:
		sourceLabel := sourceCode
		if global, ok := s.catalog.GlobalByCode[sourceCode]; ok {
			sourceLabel = global.NameES
		}
		covered := strings.Join(country.CountriesCovered, ", ")
		if covered == "" {
			covered = country.Label
		}
		note = fmt.Sprintf(
			"FirstJob agrupa %s dentro de %s y no tiene regiones para esa opción; se buscará en toda la región (%s).",
			sourceLabel, country.Label, covered,
		)
	case country.Note != "":
		note = country.Note
	}

	if intent != nil && intent.Kind == "city" {
		region, found := s.findRegion(country, intent.City)
		if found {
			return PortalPlan{
				Supported:      true,
				SearchLocation: region.Label + ", " + country.Label,
				Note:           firstJobWithNoDatesNote(note),
			}
		}
		if !grouped {
			extra := ""
			if len(country.Regions) > 0 {
				extra = fmt.Sprintf(
					"No reconoció «%s» entre las regiones de %s; se buscará en todo el país.",
					intent.City, country.Label,
				)
			} else {
				extra = fmt.Sprintf(
					"FirstJob no ofrece regiones para %s; se buscará en todo el país.",
					country.Label,
				)
			}
			note = strings.TrimSpace(note + " " + extra)
		}
	}
	return PortalPlan{
		Supported:      true,
		SearchLocation: country.Label,
		Note:           firstJobWithNoDatesNote(note),
	}
}

func firstJobWithNoDatesNote(note string) string {
	if strings.TrimSpace(note) == "" {
		return firstJobNoDatesNote
	}
	return strings.TrimSpace(note) + " " + firstJobNoDatesNote
}

func (s *FirstJobScraper) findRegion(
	country data.FirstJobCountry, value string,
) (data.FirstJobRegion, bool) {
	region, ok := s.catalog.RegionLookup[[2]string{
		country.Code, locations.Normalize(value),
	}]
	return region, ok
}

func (s *FirstJobScraper) resolveSearchLocation(location string) firstJobResolved {
	normalized := locations.Normalize(location)
	code := s.catalog.DefaultCountry
	rest := normalized
	for _, key := range s.catalog.CountryKeys {
		if normalized == key {
			code = s.catalog.CountryLookup[key]
			rest = ""
			break
		}
		if suffix := ", " + key; strings.HasSuffix(normalized, suffix) {
			code = s.catalog.CountryLookup[key]
			rest = strings.TrimSpace(strings.TrimSuffix(normalized, suffix))
			break
		}
	}
	country := s.catalog.CountriesByCode[code]
	if rest == "" {
		return firstJobResolved{Country: country, Matched: true}
	}
	if region, ok := s.findRegion(country, rest); ok {
		return firstJobResolved{
			Country: country, RegionID: region.ID, Matched: true,
		}
	}
	return firstJobResolved{Country: country}
}

// FetchJobs searches FirstJob cards and enriches the pending jobs with their
// visible detail pages. Search results and failed details are cached across
// cycles and guilds; publication dates and max age play no part.
func (s *FirstJobScraper) FetchJobs(
	ctx context.Context,
	keywords []string,
	location string,
	maxAgeHours float64,
	knownJobIDs map[string]struct{},
) ([]job.Job, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("firstjob fetch jobs: %w", err)
	}
	terms := cleanBoardTerms(keywords)
	if len(terms) == 0 {
		return []job.Job{}, nil
	}

	resolved := s.resolveSearchLocation(location)
	if !resolved.Matched {
		slog.Warn(
			"FirstJob location is not in the location catalog; searching all of the country",
			"location", location, "country", resolved.Country.Label,
		)
	}
	headers := map[string]string{
		"User-Agent":      firstJobUserAgent(s.random()),
		"Accept-Language": "es-LATAM,es;q=0.9,en;q=0.8",
	}
	s.fetcher.BeginCycle()
	slog.Info(fmt.Sprintf(
		"FirstJob transport: proxy fallback (country=%s region=%s) — no publication dates, max_age_hours=%s is not applied",
		resolved.Country.Label, dashOrValue(resolved.RegionID), firstJobAgeText(maxAgeHours),
	))

	key := firstJobContext{
		TermsKey:    boardTermsKey(terms),
		CountryCode: resolved.Country.Code,
		RegionID:    resolved.RegionID,
	}
	candidates, pagesFetched, foundCount, err := s.cachedCandidates(
		ctx, terms, resolved.Country, resolved.RegionID, key, headers,
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

	pendingOrder, pending := s.buildPending(key, candidates, known)
	enriched := make([]job.Job, 0, len(pendingOrder))
	remainingOrder := make([]string, 0)
	remaining := make(map[string]job.Job)
	cachedCount, fetchedCount, errorCount := 0, 0, 0

	for index, id := range pendingOrder {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("firstjob detail pass: %w", err)
		}
		if s.fetcher.CircuitOpen() {
			firstJobAppendRemaining(pendingOrder[index:], pending, &remainingOrder, remaining)
			break
		}
		if fetchedCount >= firstJobMaxDetails {
			firstJobAppendRemaining(pendingOrder[index:], pending, &remainingOrder, remaining)
			break
		}
		cardJob, exists := pending[id]
		if !exists {
			continue
		}
		if cached, ok := s.cachedDetail(id); ok {
			cachedCount++
			enriched = append(enriched, cached.Job)
			continue
		}

		response, err := s.fetcher.Fetch(ctx, httpclient.Request{
			URL:     cardJob.URL,
			Headers: headers,
			Retries: firstJobRetryCount(2),
			Label:   "job " + id,
		})
		fetchedCount++
		if err != nil || response == nil || !response.IsSuccess() {
			if err == nil && response != nil {
				slog.Warn(
					"FirstJob detail returned a non-success status",
					"job", id, "status", response.StatusCode,
				)
			}
			errorCount++
			remaining[id] = cardJob
			remainingOrder = append(remainingOrder, id)
			continue
		}

		detailed := firstJobParseDetail(response.Text(), cardJob)
		s.storeDetail(id, detailed)
		enriched = append(enriched, detailed)
		if err := s.sleep(ctx, s.detailDelay()); err != nil {
			return nil, fmt.Errorf("firstjob detail delay: %w", err)
		}
	}

	nowMonotonic := s.cacheClock()
	s.storeDeferred(key, remainingOrder, remaining, nowMonotonic)
	s.pruneDeferred(nowMonotonic)
	slog.Info(fmt.Sprintf(
		"FirstJob cycle: keywords=%d pages=%d found=%d unique=%d known=%d cached=%d fetched=%d deferred=%d errors=%d",
		len(terms), pagesFetched, foundCount, len(candidates.Records), knownCount,
		cachedCount, fetchedCount, len(remaining), errorCount,
	))
	return enriched, nil
}

func firstJobAgeText(value float64) string {
	text := strconv.FormatFloat(value, 'f', -1, 64)
	if !strings.ContainsAny(text, ".eE") {
		text += ".0"
	}
	return text
}

func firstJobRetryCount(retries int) *int { return &retries }

func firstJobUserAgent(value float64) string {
	index := int(math.Floor(value * float64(len(firstJobUserAgents))))
	if index < 0 {
		index = 0
	}
	if index >= len(firstJobUserAgents) {
		index = len(firstJobUserAgents) - 1
	}
	return firstJobUserAgents[index]
}

func (s *FirstJobScraper) detailDelay() time.Duration {
	return firstJobDelay(firstJobDetailMinDelay, firstJobDetailMaxDelay, s.random())
}

func (s *FirstJobScraper) searchDelay() time.Duration {
	return firstJobDelay(firstJobSearchMinDelay, firstJobSearchMaxDelay, s.random())
}

func firstJobDelay(minimum, maximum time.Duration, value float64) time.Duration {
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

func firstJobAppendRemaining(
	ids []string, pending map[string]job.Job,
	order *[]string, remaining map[string]job.Job,
) {
	for _, id := range ids {
		record, exists := pending[id]
		if !exists {
			continue
		}
		if _, exists := remaining[id]; exists {
			continue
		}
		remaining[id] = record
		*order = append(*order, id)
	}
}

func (s *FirstJobScraper) buildPending(
	key firstJobContext,
	candidates firstJobCandidateSet,
	known map[string]struct{},
) ([]string, map[string]job.Job) {
	deferredOrder, deferredItems := s.deferredSnapshot(key, s.cacheClock(), known)
	pending := make(map[string]job.Job, len(candidates.Order)+len(deferredOrder))
	order := make([]string, 0, len(candidates.Order)+len(deferredOrder))
	for _, id := range candidates.Order {
		record, exists := candidates.Records[id]
		if !exists {
			continue
		}
		if _, exists := known[id]; exists {
			continue
		}
		pending[id] = record
		order = append(order, id)
	}
	for _, id := range deferredOrder {
		entry, exists := deferredItems[id]
		if !exists {
			continue
		}
		if _, exists := pending[id]; !exists {
			order = append(order, id)
		}
		pending[id] = entry.Job
	}
	return order, pending
}

func (s *FirstJobScraper) deferredSnapshot(
	key firstJobContext, now time.Time, known map[string]struct{},
) ([]string, map[string]firstJobDeferredEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.deferred[key]
	if state == nil {
		return nil, nil
	}
	order := make([]string, 0, len(state.Order))
	items := make(map[string]firstJobDeferredEntry, len(state.Order))
	for _, id := range state.Order {
		entry, exists := state.Items[id]
		if !exists || !entry.ExpiresAt.After(now) {
			continue
		}
		if _, exists := known[id]; exists {
			continue
		}
		order = append(order, id)
		items[id] = entry
	}
	return order, items
}

func (s *FirstJobScraper) cachedCandidates(
	ctx context.Context,
	terms []string,
	country data.FirstJobCountry,
	regionID string,
	key firstJobContext,
	headers map[string]string,
) (firstJobCandidateSet, int, int, error) {
	now := s.cacheClock()
	s.mu.Lock()
	if cached, ok := s.searchCache[key]; ok && cached.ExpiresAt.After(now) {
		s.mu.Unlock()
		return cached.Candidates.clone(), cached.Pages, cached.Found, nil
	}
	if call, ok := s.searchCalls[key]; ok {
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return firstJobCandidateSet{}, 0, 0, fmt.Errorf("wait for FirstJob search: %w", ctx.Err())
		case <-call.done:
			return call.candidates.clone(), call.pages, call.found, call.err
		}
	}
	call := &firstJobSearchCall{done: make(chan struct{})}
	s.searchCalls[key] = call
	s.mu.Unlock()

	candidates, pages, found, complete, err := s.searchCandidates(
		ctx, terms, country, regionID, headers,
	)
	if err == nil && complete {
		expiresAt := s.cacheClock().Add(firstJobSearchTTL)
		s.mu.Lock()
		s.pruneSearchCache(s.cacheClock())
		s.searchCache[key] = firstJobCachedSearch{
			ExpiresAt:  expiresAt,
			Candidates: candidates.clone(),
			Pages:      pages,
			Found:      found,
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

func (s *FirstJobScraper) pruneSearchCache(now time.Time) {
	for key, cached := range s.searchCache {
		if !cached.ExpiresAt.After(now) {
			delete(s.searchCache, key)
		}
	}
}

func (s *FirstJobScraper) searchCandidates(
	ctx context.Context,
	terms []string,
	country data.FirstJobCountry,
	regionID string,
	headers map[string]string,
) (firstJobCandidateSet, int, int, bool, error) {
	candidates := newFirstJobCandidateSet()
	pagesFetched, foundCount := 0, 0
	complete := true

	for _, keyword := range terms {
		for page := 1; page <= firstJobMaxPages; page++ {
			if err := ctx.Err(); err != nil {
				return firstJobCandidateSet{}, 0, 0, false, fmt.Errorf("firstjob search: %w", err)
			}
			if s.fetcher.CircuitOpen() {
				slog.Warn(
					"FirstJob search circuit open; keeping jobs from previous keywords",
					"retained", len(candidates.Records),
				)
				complete = false
				break
			}
			response, err := s.fetcher.Fetch(ctx, httpclient.Request{
				URL:     firstJobBaseURL + "/ofertas",
				Params:  firstJobSearchParams(keyword, country, regionID, page),
				Headers: headers,
				Retries: firstJobRetryCount(2),
				Label:   fmt.Sprintf("search '%s' page %d", keyword, page),
			})
			if err != nil || response == nil {
				slog.Warn(
					"FirstJob search stopped for keyword at page",
					"keyword", keyword, "page", page, "error", err,
				)
				complete = false
				break
			}
			if !response.IsSuccess() {
				slog.Warn(
					"FirstJob search returned a non-success status",
					"keyword", keyword, "page", page, "status", response.StatusCode,
				)
				complete = false
				break
			}
			if firstJobLooksBlocked(response.Text()) {
				slog.Warn(
					"FirstJob returned a challenge page",
					"keyword", keyword, "page", page,
				)
				complete = false
				break
			}

			pageJobs := firstJobParseCards(response.Text(), firstJobBaseURL)
			pagesFetched++
			foundCount += len(pageJobs)
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
			if page >= firstJobMaxPages ||
				len(pageJobs) < firstJobPageSize || newJobs == 0 {
				break
			}
			if err := s.sleep(ctx, s.searchDelay()); err != nil {
				return firstJobCandidateSet{}, 0, 0, false, fmt.Errorf("firstjob search delay: %w", err)
			}
		}
	}
	return candidates, pagesFetched, foundCount, complete, nil
}

func firstJobSearchParams(
	keyword string, country data.FirstJobCountry, regionID string, page int,
) map[string]string {
	params := map[string]string{
		"title":     keyword,
		"country[]": country.CountryID,
	}
	if regionID != "" {
		params["region[]"] = regionID
	}
	if page > 1 {
		params["page"] = strconv.Itoa(page)
	}
	return params
}

func (s *FirstJobScraper) cachedDetail(id string) (firstJobDetailEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, exists := s.detailCache[id]
	if !exists {
		return firstJobDetailEntry{}, false
	}
	if !entry.ExpiresAt.After(s.cacheClock()) {
		delete(s.detailCache, id)
		s.removeDetailOrder(id)
		return firstJobDetailEntry{}, false
	}
	return entry, true
}

func (s *FirstJobScraper) removeDetailOrder(id string) {
	remaining := s.detailOrder[:0]
	for _, value := range s.detailOrder {
		if value != id {
			remaining = append(remaining, value)
		}
	}
	s.detailOrder = remaining
}

func (s *FirstJobScraper) storeDetail(id string, detailed job.Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.cacheClock()
	if len(s.detailCache) >= firstJobDetailLimit {
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
		for len(s.detailCache) >= firstJobDetailLimit && len(s.detailOrder) > 0 {
			oldest := s.detailOrder[0]
			delete(s.detailCache, oldest)
			s.detailOrder = s.detailOrder[1:]
		}
	}
	if _, exists := s.detailCache[id]; !exists {
		s.detailOrder = append(s.detailOrder, id)
	}
	s.detailCache[id] = firstJobDetailEntry{
		ExpiresAt: now.Add(firstJobDetailTTL),
		Job:       detailed,
	}
}

func (s *FirstJobScraper) storeDeferred(
	key firstJobContext, order []string, items map[string]job.Job, now time.Time,
) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(order) == 0 {
		delete(s.deferred, key)
		return
	}
	if len(order) > firstJobDeferredLimit {
		order = order[:firstJobDeferredLimit]
	}
	state := &firstJobDeferredState{
		Order: append([]string(nil), order...),
		Items: make(map[string]firstJobDeferredEntry, len(order)),
	}
	for _, id := range order {
		state.Items[id] = firstJobDeferredEntry{
			ExpiresAt: now.Add(firstJobDeferredTTL),
			Job:       items[id],
		}
	}
	s.deferred[key] = state
}

func (s *FirstJobScraper) pruneDeferred(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, state := range s.deferred {
		order := state.Order[:0]
		alive := make(map[string]firstJobDeferredEntry, len(state.Items))
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
	for len(s.deferred) > firstJobDeferredMaxCtx {
		var oldestKey firstJobContext
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

func firstJobText(selection *goquery.Selection) string {
	if selection == nil {
		return ""
	}
	parts := make([]string, 0, 8)
	collectBoardText(selection, &parts)
	return strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
}

func firstJobLines(selection *goquery.Selection) []string {
	if selection == nil {
		return nil
	}
	lines := make([]string, 0, 8)
	collectFirstJobLines(selection, &lines)
	return lines
}

func collectFirstJobLines(selection *goquery.Selection, lines *[]string) {
	if selection == nil {
		return
	}
	selection.Contents().Each(func(_ int, child *goquery.Selection) {
		switch goquery.NodeName(child) {
		case "#text":
			for _, line := range strings.Split(child.Text(), "\n") {
				if text := strings.TrimSpace(line); text != "" {
					*lines = append(*lines, text)
				}
			}
		case "#comment":
			return
		default:
			collectFirstJobLines(child, lines)
		}
	})
}

func firstJobResolveURL(value, baseURL string) string {
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

func firstJobLogoURL(selection *goquery.Selection, baseURL string) string {
	if selection == nil {
		return ""
	}
	value, exists := selection.Attr("src")
	if !exists || strings.TrimSpace(value) == "" {
		value, _ = selection.Attr("data-src")
	}
	return firstJobResolveURL(value, baseURL)
}

func firstJobOrUnspecified(value string) string {
	if strings.TrimSpace(value) == "" {
		return "No especificada"
	}
	return value
}

func firstJobLooksBlocked(raw string) bool {
	runes := []rune(raw)
	if len(runes) > 4000 {
		runes = runes[:4000]
	}
	sample := strings.ToLower(string(runes))
	for _, marker := range firstJobBlockMarkers {
		if strings.Contains(sample, marker) {
			return true
		}
	}
	return false
}

func firstJobParseCards(raw, baseURL string) []job.Job {
	document, err := goquery.NewDocumentFromReader(strings.NewReader(raw))
	if err != nil {
		return nil
	}
	results := make([]job.Job, 0, 16)
	seen := make(map[string]struct{})
	document.Find(".card-job").Each(func(_ int, card *goquery.Selection) {
		link := card.Find(`a[href*="/oferta/"]`).First()
		if link.Length() == 0 {
			return
		}
		href, exists := link.Attr("href")
		if !exists || strings.TrimSpace(href) == "" {
			return
		}
		match := firstJobOfferIDPattern.FindStringSubmatch(href)
		if match == nil {
			return
		}
		id := match[1]
		if _, exists := seen[id]; exists {
			return
		}
		title := firstJobText(card.Find(".card-job-top--info-heading").First())
		if title == "" {
			return
		}
		jobURL := firstJobResolveURL(
			strings.TrimSpace(strings.SplitN(href, "#", 2)[0]), baseURL,
		)
		if jobURL == "" {
			return
		}

		company := firstJobText(card.Find(".card-job-top--company").First())
		location := firstJobText(card.Find(".card-job-top--location").First())
		modality := strings.ToLower(firstJobText(card.Find(".card-job-bottom").First()))
		if strings.Contains(modality, "remoto") ||
			strings.Contains(modality, "home office") {
			alreadyRemote := strings.Contains(
				strings.ToLower(location),
				strings.ToLower(strings.TrimSpace(firstJobRemoteSuffix)),
			)
			if !alreadyRemote {
				if location == "" {
					location = "Remoto"
				} else {
					location += firstJobRemoteSuffix
				}
			}
		}

		seen[id] = struct{}{}
		results = append(results, job.Job{
			ID:          id,
			Title:       title,
			Company:     firstJobOrUnspecified(company),
			Location:    firstJobOrUnspecified(location),
			URL:         jobURL,
			Source:      FirstJobSource,
			PublishedAt: "",
			CompanyLogoURL: firstJobLogoURL(
				card.Find(".card-job-top--image img").First(), baseURL,
			),
			Description: firstJobText(card.Find(".card-job-description").First()),
		})
	})
	return results
}

func firstJobParseDetail(raw string, card job.Job) job.Job {
	document, err := goquery.NewDocumentFromReader(strings.NewReader(raw))
	if err != nil {
		return card
	}
	detailed := card
	if title := firstJobText(document.Find(".job-single-header h3").First()); title != "" {
		detailed.Title = title
	}
	if company := firstJobText(document.Find(".job-meta a.company").First()); company != "" {
		detailed.Company = company
	}
	if location := firstJobText(document.Find(".job-meta span.location").First()); location != "" {
		detailed.Location = location
	}
	blocks := make([]string, 0, 4)
	document.Find(".content-single.content-offer").Each(func(_ int, block *goquery.Selection) {
		lines := firstJobLines(block)
		if len(lines) == 0 {
			return
		}
		blocks = append(blocks, strings.Join(lines, "\n"))
	})
	if description := strings.Join(blocks, "\n\n"); description != "" {
		detailed.Description = description
	}
	if logo := firstJobLogoURL(
		document.Find(".avatar-sidebar img").First(), firstJobBaseURL,
	); logo != "" {
		detailed.CompanyLogoURL = logo
	}
	return detailed
}
