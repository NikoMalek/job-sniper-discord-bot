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
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"

	"github.com/NikoMalek/job-sniper-discord-bot/internal/data"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/httpclient"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/job"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/locations"
	"github.com/PuerkitoBio/goquery"
)

const (
	// ChileTrabajosSlug is the stable identifier of the portal.
	ChileTrabajosSlug = "chiletrabajos"
	// ChileTrabajosSource is the human-readable name shown in notifications.
	ChileTrabajosSource = "ChileTrabajos"

	chileBaseURL    = "https://www.chiletrabajos.cl"
	chileSearchPath = "/encuentra-un-empleo"

	// ChileTrabajosProxyMode and ChileTrabajosProxyHost describe the Worker fallback.
	ChileTrabajosProxyMode = "always"
	ChileTrabajosProxyHost = "www.chiletrabajos.cl"

	chilePageSize           = 30
	chileMaxPagesPerKeyword = 5
	chileMaxDetailsPerCycle = 25
	chileCityTTL            = 6 * time.Hour
	chileDetailTTL          = 6 * time.Hour
	chileDetailLimit        = 500
	chileSearchTTL          = 4 * time.Minute
	chileDeferredTTL        = 3 * time.Hour
	chileDeferredLimit      = 200
	chileDeferredContexts   = 50
	chileMinDetailDelay     = 400 * time.Millisecond
	chileMaxDetailDelay     = 900 * time.Millisecond
	chileMinSearchDelay     = 500 * time.Millisecond
	chileMaxSearchDelay     = 1000 * time.Millisecond
)

var (
	chileIDPattern      = regexp.MustCompile(`-(\d+)/?$`)
	chileTotalPattern   = regexp.MustCompile(`(?i)([\d.]+)\s+Trabajos`)
	chileCSSURLPattern  = regexp.MustCompile(`(?i)url\(\s*['"]?([^'")]+)`)
	chileRemotePrefixes = []string{
		"anywhere", "remote", "remoto", "todo chile", "todo el mundo", "worldwide",
	}
	chileUserAgents = []string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Safari/537.36",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15",
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Safari/537.36",
	}
	chileMonths = [...]string{
		"ene", "feb", "mar", "abr", "may", "jun",
		"jul", "ago", "sep", "oct", "nov", "dic",
	}
)

// The blank time/tzdata import keeps Chile date handling available without OS
// zone data. The recorded fixtures were produced by Python's fixed UTC-4
// fallback, so naive datePosted values keep that offset for fixture parity.
var chileLocation = time.FixedZone("America/Santiago", -4*60*60)

// ChileTrabajosOptions holds the deterministic dependencies of the scraper.
type ChileTrabajosOptions struct {
	Fetcher    httpclient.Fetcher
	Clock      func() time.Time
	CacheClock func() time.Time
	Sleep      func(context.Context, time.Duration) error
	Random     func() float64
}

type chileCityMapping struct {
	Values map[string]string
	Order  []string
}

func newChileCityMapping() chileCityMapping {
	return chileCityMapping{Values: make(map[string]string)}
}

func (m chileCityMapping) clone() chileCityMapping {
	clone := newChileCityMapping()
	clone.Order = append(clone.Order, m.Order...)
	for name, value := range m.Values {
		clone.Values[name] = value
	}
	return clone
}

func (m *chileCityMapping) set(name, value string) {
	if name == "" || value == "" {
		return
	}
	if _, exists := m.Values[name]; !exists {
		m.Order = append(m.Order, name)
	}
	m.Values[name] = value
}

type chileCityCache struct {
	ExpiresAt time.Time
	Mapping   chileCityMapping
}

type chileCityCall struct {
	done    chan struct{}
	mapping chileCityMapping
	err     error
}

type chileCandidateSet struct {
	Records map[string]job.Job
	Order   []string
}

func newChileCandidateSet() chileCandidateSet {
	return chileCandidateSet{Records: make(map[string]job.Job)}
}

func (c chileCandidateSet) clone() chileCandidateSet {
	clone := newChileCandidateSet()
	for id, record := range c.Records {
		clone.Records[id] = record
	}
	clone.Order = append(clone.Order, c.Order...)
	return clone
}

type chileCachedSearch struct {
	ExpiresAt    time.Time
	Candidates   chileCandidateSet
	PagesFetched int
	FoundCount   int
}

type chileContext struct {
	TermsKey   string
	LocationID string
	Fecha      int
}

type chileSearchCall struct {
	done       chan struct{}
	candidates chileCandidateSet
	pages      int
	found      int
	err        error
}

type chileDetailEntry struct {
	ExpiresAt time.Time
	Job       job.Job
	Posted    time.Time
}

type chileDeferredEntry struct {
	ExpiresAt time.Time
	Job       job.Job
}

type chileDeferredState struct {
	Order []string
	Items map[string]chileDeferredEntry
}

// ChileTrabajosScraper fetches search cards from ChileTrabajos and enriches
// them with JSON-LD details. Searches, details and failed detail fetches are
// cached across cycles and guilds.
type ChileTrabajosScraper struct {
	BaseScraper

	fetcher    httpclient.Fetcher
	clock      func() time.Time
	cacheClock func() time.Time
	sleep      func(context.Context, time.Duration) error
	random     func() float64

	staticCities chileCityMapping

	mu          sync.Mutex
	cityCache   *chileCityCache
	cityCall    *chileCityCall
	detailCache map[string]chileDetailEntry
	detailOrder []string
	searchCache map[chileContext]chileCachedSearch
	searchCalls map[chileContext]*chileSearchCall
	deferred    map[chileContext]*chileDeferredState
}

// NewChileTrabajosScraper builds a scraper. Missing dependencies receive the
// production defaults; tests inject a replay fetcher, clocks and sleeper.
func NewChileTrabajosScraper(options ChileTrabajosOptions) *ChileTrabajosScraper {
	fetcher := options.Fetcher
	if fetcher == nil {
		fetcher = httpclient.NewClient(httpclient.Options{
			Source:     ChileTrabajosSource,
			ProxyMode:  ChileTrabajosProxyMode,
			ProxyHosts: []string{ChileTrabajosProxyHost},
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

	static := newChileCityMapping()
	catalog, err := data.Load(context.Background())
	if err != nil {
		slog.Warn("ChileTrabajos built-in city list is unavailable", "error", err)
	} else {
		for _, city := range catalog.ChileCities() {
			static.set(locations.Normalize(city.Name), city.Bucket)
		}
	}

	return &ChileTrabajosScraper{
		BaseScraper: NewBaseScraper(
			ChileTrabajosSlug, ChileTrabajosSource, []string{"CL"},
		),
		fetcher:      fetcher,
		clock:        clock,
		cacheClock:   cacheClock,
		sleep:        sleeper,
		random:       random,
		staticCities: static,
		detailCache:  make(map[string]chileDetailEntry),
		searchCache:  make(map[chileContext]chileCachedSearch),
		searchCalls:  make(map[chileContext]*chileSearchCall),
		deferred:     make(map[chileContext]*chileDeferredState),
	}
}

// Plan maps the neutral location intent to the portal's city filter.
func (s *ChileTrabajosScraper) Plan(intent *locations.LocationIntent) PortalPlan {
	if !s.Covers(intent) {
		return PortalPlan{Reason: s.PauseReason(intent)}
	}
	if intent == nil {
		return PortalPlan{
			Supported:      true,
			SearchLocation: "Chile",
			Note:           "solo puede buscar ofertas en Chile y no tiene filtro de modalidad.",
		}
	}
	switch intent.Kind {
	case "worldwide", "remote":
		if intent.Country() == nil {
			return PortalPlan{
				Supported:      true,
				SearchLocation: "Chile",
				Note:           "solo puede buscar ofertas en Chile y no tiene filtro de modalidad.",
			}
		}
		return PortalPlan{
			Supported:      true,
			SearchLocation: "Chile",
			Note:           "no tiene filtro remoto; se buscan todas las ofertas de Chile.",
		}
	case "country":
		return PortalPlan{Supported: true, SearchLocation: "Chile"}
	}
	if intent.ChileBucket != "" {
		note := ""
		if intent.CityAlias {
			note = fmt.Sprintf(
				"no tiene una opción para %s; se usa %s como equivalente.",
				intent.City, intent.ChileBucketCity,
			)
		}
		return PortalPlan{
			Supported:      true,
			SearchLocation: intent.ChileBucketCity,
			Note:           note,
		}
	}
	return PortalPlan{
		Supported:      true,
		SearchLocation: "Chile",
		Note: fmt.Sprintf(
			"no tiene una opción para %s; se buscará en todo Chile. Puedes acotar con `/add-location` en ciudades cercanas.",
			intent.City,
		),
	}
}

// FetchJobs fetches ChileTrabajos cards and enriches the recent ones with
// JSON-LD details. Known jobs and failed details are skipped.
func (s *ChileTrabajosScraper) FetchJobs(
	ctx context.Context,
	keywords []string,
	location string,
	maxAgeHours float64,
	knownJobIDs map[string]struct{},
) ([]job.Job, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("chiletrabajos fetch jobs: %w", err)
	}

	terms := cleanBoardTerms(keywords)
	if len(terms) == 0 {
		return []job.Job{}, nil
	}

	cutoff := s.now().Add(-time.Duration(maxAgeHours * float64(time.Hour)))
	fecha := chileFechaBucket(maxAgeHours)
	headers := map[string]string{
		"User-Agent":      s.userAgent(s.random()),
		"Accept-Language": "es-CL,es;q=0.9,en;q=0.8",
	}
	s.fetcher.BeginCycle()
	slog.Info("ChileTrabajos transport: proxy always")

	locationID := ""
	normalizedLocation := locations.Normalize(location)
	if normalizedLocation != "" && normalizedLocation != "chile" &&
		!chileHasRemotePrefix(normalizedLocation) {
		mapping, err := s.cityMapping(ctx, headers)
		if err != nil {
			return nil, err
		}
		locationID = chileMatchLocation(location, mapping)
	}

	contextKey := chileContext{
		TermsKey:   boardTermsKey(terms),
		LocationID: locationID,
		Fecha:      fecha,
	}
	candidates, pagesFetched, foundCount, err := s.cachedCandidates(
		ctx, terms, locationID, fecha, contextKey, headers,
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

	pendingOrder, pending := s.buildPending(contextKey, candidates, known)
	enriched := make([]job.Job, 0, len(pendingOrder))
	remainingOrder := make([]string, 0)
	remaining := make(map[string]job.Job)
	cachedCount, fetchedCount := 0, 0
	tooOldCount, expiredCount, errorCount := 0, 0, 0

	for index, id := range pendingOrder {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("chiletrabajos detail pass: %w", err)
		}
		if s.fetcher.CircuitOpen() {
			chileAppendRemaining(pendingOrder[index:], pending, &remainingOrder, remaining)
			break
		}
		if fetchedCount >= chileMaxDetailsPerCycle {
			chileAppendRemaining(pendingOrder[index:], pending, &remainingOrder, remaining)
			break
		}

		cardJob, exists := pending[id]
		if !exists {
			continue
		}
		if cached, ok := s.cachedDetail(id); ok {
			cachedCount++
			if chileIsRecent(cached.Posted, cutoff) {
				enriched = append(enriched, cached.Job)
			} else {
				tooOldCount++
			}
			continue
		}

		response, err := s.fetcher.Fetch(ctx, httpclient.Request{
			URL:     cardJob.URL,
			Headers: headers,
			Retries: chileRetryCount(2),
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

		detail := chileParseDetail(response.Text())
		if detail == nil {
			slog.Warn("ChileTrabajos job has no JobPosting data; skipping", "job", id)
			errorCount++
			continue
		}
		detailed, posted, ok := chileJobFromDetail(
			cardJob, detail, chileDetailLogoURL(response.Text()),
		)
		if !ok {
			slog.Warn("ChileTrabajos job has no parseable date; skipping", "job", id)
			errorCount++
			continue
		}
		s.storeDetail(id, detailed, posted)
		if chileIsRecent(posted, cutoff) {
			enriched = append(enriched, detailed)
		} else {
			tooOldCount++
		}
		if err := s.sleep(ctx, s.detailDelay()); err != nil {
			return nil, fmt.Errorf("chiletrabajos detail delay: %w", err)
		}
	}

	now := s.cacheClock()
	s.storeDeferred(contextKey, remainingOrder, remaining, now)
	s.pruneDeferred(now)

	slog.Info(fmt.Sprintf(
		"ChileTrabajos cycle: keywords=%d pages=%d found=%d unique=%d known=%d cached=%d fetched=%d deferred=%d too_old=%d expired=%d errors=%d",
		len(terms), pagesFetched, foundCount, len(candidates.Records), knownCount,
		cachedCount, fetchedCount, len(remaining), tooOldCount, expiredCount, errorCount,
	))
	return enriched, nil
}

func (s *ChileTrabajosScraper) now() time.Time {
	return s.clock().UTC()
}

func chileRetryCount(retries int) *int { return &retries }

func (s *ChileTrabajosScraper) userAgent(value float64) string {
	index := int(math.Floor(value * float64(len(chileUserAgents))))
	if index < 0 {
		index = 0
	}
	if index >= len(chileUserAgents) {
		index = len(chileUserAgents) - 1
	}
	return chileUserAgents[index]
}

func (s *ChileTrabajosScraper) detailDelay() time.Duration {
	return chileDelay(chileMinDetailDelay, chileMaxDetailDelay, s.random())
}

func (s *ChileTrabajosScraper) searchDelay() time.Duration {
	return chileDelay(chileMinSearchDelay, chileMaxSearchDelay, s.random())
}

func chileDelay(minimum, maximum time.Duration, value float64) time.Duration {
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

func chileFechaBucket(hours float64) int {
	switch {
	case hours <= 24:
		return 2
	case hours <= 72:
		return 3
	case hours <= 144:
		return 4
	case hours <= 168:
		return 5
	default:
		return 6
	}
}

func chileHasRemotePrefix(value string) bool {
	for _, prefix := range chileRemotePrefixes {
		if strings.HasPrefix(value, prefix) {
			return true
		}
	}
	return false
}

func chileIsRecent(posted, cutoff time.Time) bool {
	return !posted.UTC().Before(cutoff)
}

func chileAppendRemaining(
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

func (s *ChileTrabajosScraper) buildPending(
	contextKey chileContext, candidates chileCandidateSet, known map[string]struct{},
) ([]string, map[string]job.Job) {
	now := s.cacheClock()
	deferredOrder, deferredItems := s.deferredSnapshot(contextKey, now, known)
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
		pending[id] = record
		order = append(order, id)
	}
	return order, pending
}

func (s *ChileTrabajosScraper) deferredSnapshot(
	contextKey chileContext, now time.Time, known map[string]struct{},
) ([]string, map[string]chileDeferredEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.deferred[contextKey]
	if state == nil {
		return nil, nil
	}
	order := make([]string, 0, len(state.Order))
	items := make(map[string]chileDeferredEntry, len(state.Order))
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

func (s *ChileTrabajosScraper) cityMapping(
	ctx context.Context, headers map[string]string,
) (chileCityMapping, error) {
	if err := ctx.Err(); err != nil {
		return chileCityMapping{}, fmt.Errorf("chiletrabajos city mapping: %w", err)
	}

	now := s.cacheClock()
	s.mu.Lock()
	if s.cityCache != nil && s.cityCache.ExpiresAt.After(now) {
		mapping := s.cityCache.Mapping.clone()
		s.mu.Unlock()
		return mapping, nil
	}
	if call := s.cityCall; call != nil {
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return chileCityMapping{}, fmt.Errorf("wait for city list: %w", ctx.Err())
		case <-call.done:
			if call.err != nil {
				return chileCityMapping{}, call.err
			}
			return call.mapping.clone(), nil
		}
	}
	call := &chileCityCall{done: make(chan struct{})}
	s.cityCall = call
	s.mu.Unlock()

	mapping := s.staticCities.clone()
	response, err := s.fetcher.Fetch(ctx, httpclient.Request{
		URL:     chileBaseURL + chileSearchPath,
		Headers: headers,
		Retries: chileRetryCount(1),
		Label:   "city list",
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			call.err = fmt.Errorf("chiletrabajos city list: %w", ctxErr)
			s.mu.Lock()
			s.cityCall = nil
			s.mu.Unlock()
			close(call.done)
			return chileCityMapping{}, call.err
		}
		slog.Warn("ChileTrabajos city list unavailable; using built-in city names")
	} else if !response.IsSuccess() {
		slog.Warn("ChileTrabajos city list unavailable; using built-in city names")
	} else {
		parsed := chileParseCities(response.Text())
		for _, name := range parsed.Order {
			mapping.set(name, parsed.Values[name])
		}
	}

	expiresAt := s.cacheClock().Add(chileCityTTL)
	s.mu.Lock()
	s.cityCache = &chileCityCache{ExpiresAt: expiresAt, Mapping: mapping.clone()}
	s.cityCall = nil
	s.mu.Unlock()
	call.mapping = mapping.clone()
	close(call.done)
	return mapping, nil
}

func (s *ChileTrabajosScraper) cachedDetail(id string) (chileDetailEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, exists := s.detailCache[id]
	if !exists {
		return chileDetailEntry{}, false
	}
	if !entry.ExpiresAt.After(s.cacheClock()) {
		delete(s.detailCache, id)
		s.removeDetailOrder(id)
		return chileDetailEntry{}, false
	}
	return entry, true
}

func (s *ChileTrabajosScraper) removeDetailOrder(id string) {
	remaining := s.detailOrder[:0]
	for _, value := range s.detailOrder {
		if value != id {
			remaining = append(remaining, value)
		}
	}
	s.detailOrder = remaining
}

func (s *ChileTrabajosScraper) storeDetail(
	id string, detailed job.Job, posted time.Time,
) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.cacheClock()
	if len(s.detailCache) >= chileDetailLimit {
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
		for len(s.detailCache) >= chileDetailLimit && len(s.detailOrder) > 0 {
			oldest := s.detailOrder[0]
			delete(s.detailCache, oldest)
			s.detailOrder = s.detailOrder[1:]
		}
	}
	if _, exists := s.detailCache[id]; !exists {
		s.detailOrder = append(s.detailOrder, id)
	}
	s.detailCache[id] = chileDetailEntry{
		ExpiresAt: now.Add(chileDetailTTL),
		Job:       detailed,
		Posted:    posted,
	}
}

func (s *ChileTrabajosScraper) cachedCandidates(
	ctx context.Context,
	terms []string,
	locationID string,
	fecha int,
	key chileContext,
	headers map[string]string,
) (chileCandidateSet, int, int, error) {
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
			return chileCandidateSet{}, 0, 0, fmt.Errorf("wait for ChileTrabajos search: %w", ctx.Err())
		case <-call.done:
			return call.candidates.clone(), call.pages, call.found, call.err
		}
	}
	call := &chileSearchCall{done: make(chan struct{})}
	s.searchCalls[key] = call
	s.mu.Unlock()

	candidates, pages, found, complete, err := s.searchCandidates(
		ctx, terms, locationID, fecha, headers,
	)
	if err == nil && complete {
		expiresAt := s.cacheClock().Add(chileSearchTTL)
		s.mu.Lock()
		s.pruneSearchCache(s.cacheClock())
		s.searchCache[key] = chileCachedSearch{
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
	call.err = err
	s.mu.Lock()
	delete(s.searchCalls, key)
	s.mu.Unlock()
	close(call.done)
	return candidates, pages, found, err
}

func (s *ChileTrabajosScraper) pruneSearchCache(now time.Time) {
	for key, cached := range s.searchCache {
		if !cached.ExpiresAt.After(now) {
			delete(s.searchCache, key)
		}
	}
}

func (s *ChileTrabajosScraper) searchCandidates(
	ctx context.Context,
	terms []string,
	locationID string,
	fecha int,
	headers map[string]string,
) (chileCandidateSet, int, int, bool, error) {
	candidates := newChileCandidateSet()
	pagesFetched, foundCount := 0, 0
	complete := true

	for _, keyword := range terms {
		if s.fetcher.CircuitOpen() {
			slog.Warn(
				"ChileTrabajos search circuit open; keeping jobs from previous keywords",
				"retained", len(candidates.Records),
			)
			complete = false
			break
		}
		maxPages := chileMaxPagesPerKeyword
		for page := 0; page < maxPages; page++ {
			if err := ctx.Err(); err != nil {
				return chileCandidateSet{}, 0, 0, false, fmt.Errorf("chiletrabajos search: %w", err)
			}
			requestURL := chileBaseURL + chileSearchPath
			if page > 0 {
				requestURL += "/" + strconv.Itoa(page*chilePageSize)
			}
			params := map[string]string{
				"2":     keyword,
				"fecha": strconv.Itoa(fecha),
			}
			if locationID != "" {
				params["13"] = locationID
			}
			response, err := s.fetcher.Fetch(ctx, httpclient.Request{
				URL:     requestURL,
				Params:  params,
				Headers: headers,
				Retries: chileRetryCount(2),
				Label:   fmt.Sprintf("search '%s' page %d", keyword, page+1),
			})
			if err != nil {
				slog.Warn(
					"ChileTrabajos search stopped for keyword at page",
					"keyword", keyword, "page", page+1, "error", err,
				)
				complete = false
				break
			}
			if !response.IsSuccess() {
				slog.Warn(
					"ChileTrabajos search returned a non-success status",
					"keyword", keyword, "page", page+1, "status", response.StatusCode,
				)
				complete = false
				break
			}

			pageJobs := chileParseCards(response.Text())
			pagesFetched++
			foundCount += len(pageJobs)
			if page == 0 {
				if total, ok := chileParseTotal(response.Text()); ok {
					pageCount := (total + chilePageSize - 1) / chilePageSize
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
			if page+1 >= maxPages || len(pageJobs) < chilePageSize || newJobs == 0 {
				break
			}
			if err := s.sleep(ctx, s.searchDelay()); err != nil {
				return chileCandidateSet{}, 0, 0, false, fmt.Errorf("chiletrabajos search delay: %w", err)
			}
		}
	}
	return candidates, pagesFetched, foundCount, complete, nil
}

func (s *ChileTrabajosScraper) storeDeferred(
	key chileContext, order []string, items map[string]job.Job, now time.Time,
) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(order) == 0 {
		delete(s.deferred, key)
		return
	}
	if len(order) > chileDeferredLimit {
		order = order[:chileDeferredLimit]
	}
	state := &chileDeferredState{
		Order: append([]string(nil), order...),
		Items: make(map[string]chileDeferredEntry, len(order)),
	}
	for _, id := range order {
		state.Items[id] = chileDeferredEntry{
			ExpiresAt: now.Add(chileDeferredTTL),
			Job:       items[id],
		}
	}
	s.deferred[key] = state
}

func (s *ChileTrabajosScraper) pruneDeferred(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, state := range s.deferred {
		order := state.Order[:0]
		alive := make(map[string]chileDeferredEntry, len(state.Items))
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

	for len(s.deferred) > chileDeferredContexts {
		var oldestKey chileContext
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
				(expiry.Equal(oldestExpiry) && chileContextKey(key) < chileContextKey(oldestKey)) {
				oldestKey = key
				oldestExpiry = expiry
				haveOldest = true
			}
		}
		delete(s.deferred, oldestKey)
	}
}

func chileContextKey(key chileContext) string {
	return key.TermsKey + "\x00" + key.LocationID + "\x00" + strconv.Itoa(key.Fecha)
}

func chileParseCities(raw string) chileCityMapping {
	mapping := newChileCityMapping()
	document, err := goquery.NewDocumentFromReader(strings.NewReader(raw))
	if err != nil {
		return mapping
	}
	selectElement := document.Find("select[name='13']").First()
	if selectElement.Length() == 0 {
		return mapping
	}
	selectElement.Find("option").Each(func(_ int, option *goquery.Selection) {
		value, exists := option.Attr("value")
		value = strings.TrimSpace(value)
		if !exists || value == "" {
			return
		}
		name := locations.Normalize(chileText(option))
		if name != "" && name != "--" {
			mapping.set(name, value)
		}
		seo, exists := option.Attr("data-seo")
		if exists && strings.TrimSpace(seo) != "" {
			mapping.set(
				locations.Normalize(strings.ReplaceAll(seo, "-", " ")), value,
			)
		}
	})
	return mapping
}

func chileMatchLocation(location string, mapping chileCityMapping) string {
	normalized := locations.Normalize(location)
	if normalized == "" {
		return ""
	}
	if value, exists := mapping.Values[normalized]; exists {
		return value
	}
	segments := make([]string, 0, 2)
	for _, segment := range strings.Split(normalized, ",") {
		if trimmed := strings.TrimSpace(segment); trimmed != "" {
			segments = append(segments, trimmed)
		}
	}
	for _, segment := range segments {
		if value, exists := mapping.Values[segment]; exists {
			return value
		}
	}
	if normalized == "chile" || chileHasRemotePrefix(normalized) {
		return ""
	}
	for _, segment := range segments {
		if segment == "chile" || chileHasRemotePrefix(segment) {
			return ""
		}
	}

	padded := " " + normalized + " "
	bestName := ""
	for _, name := range mapping.Order {
		if strings.Contains(padded, " "+name+" ") && len(name) > len(bestName) {
			bestName = name
		}
	}
	if bestName == "" {
		return ""
	}
	return mapping.Values[bestName]
}

func chileText(selection *goquery.Selection) string {
	if selection == nil {
		return ""
	}
	parts := make([]string, 0, 8)
	collectBoardText(selection, &parts)
	return strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
}

func chileResolveURL(value string) string {
	base, err := url.Parse(chileBaseURL)
	if err != nil {
		return ""
	}
	reference, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return ""
	}
	resolved := base.ResolveReference(reference)
	if !isHTTPScheme(resolved.Scheme) {
		return ""
	}
	return resolved.String()
}

func chileImageURL(selection *goquery.Selection) string {
	if selection == nil {
		return ""
	}
	for _, attribute := range []string{"src", "data-src", "data-original", "data-lazy-src"} {
		value, exists := selection.Attr(attribute)
		if !exists || strings.TrimSpace(value) == "" {
			continue
		}
		if resolved := chileResolveURL(value); resolved != "" {
			return resolved
		}
	}
	srcset, exists := selection.Attr("srcset")
	if !exists || strings.TrimSpace(srcset) == "" {
		return ""
	}
	first := strings.TrimSpace(strings.Split(srcset, ",")[0])
	fields := strings.Fields(first)
	if len(fields) == 0 {
		return ""
	}
	return chileResolveURL(fields[0])
}

func chileBackgroundURL(selection *goquery.Selection) string {
	if selection == nil {
		return ""
	}
	style, exists := selection.Attr("style")
	if !exists {
		return ""
	}
	match := chileCSSURLPattern.FindStringSubmatch(style)
	if match == nil {
		return ""
	}
	return chileResolveURL(strings.TrimSpace(match[1]))
}

func chileLogoURL(card *goquery.Selection) string {
	image := card.Find("img.image-oferta").First()
	if value := chileImageURL(image); value != "" {
		return value
	}
	thumb := card.Find("div.thumb").First()
	if thumb.Length() == 0 {
		return ""
	}
	thumbImage := thumb.Find("img").First()
	if value := chileImageURL(thumbImage); value != "" {
		return value
	}
	return chileBackgroundURL(thumb)
}

func chileDetailLogoURL(raw string) string {
	document, err := goquery.NewDocumentFromReader(strings.NewReader(raw))
	if err != nil {
		return ""
	}
	image := document.Find("img.image-oferta").First()
	if value := chileImageURL(image); value != "" {
		return value
	}
	thumb := document.Find("div.job-item.detalle div.thumb").First()
	if thumb.Length() == 0 {
		return ""
	}
	thumbImage := thumb.Find("img").First()
	if value := chileImageURL(thumbImage); value != "" {
		return value
	}
	return chileBackgroundURL(thumb)
}

func chileParseCards(raw string) []job.Job {
	document, err := goquery.NewDocumentFromReader(strings.NewReader(raw))
	if err != nil {
		return nil
	}
	document.Find(".featured").Remove()

	results := make([]job.Job, 0, 16)
	seen := make(map[string]struct{})
	document.Find("div.job-item").Each(func(_ int, card *goquery.Selection) {
		link := card.Find("h2.title a").First()
		href, exists := link.Attr("href")
		if !exists || strings.TrimSpace(href) == "" {
			return
		}
		jobURL := chileResolveURL(href)
		if jobURL == "" {
			return
		}
		parsedURL, err := url.Parse(jobURL)
		if err != nil {
			return
		}
		match := chileIDPattern.FindStringSubmatch(parsedURL.Path)
		if match == nil {
			return
		}
		id := match[1]
		if _, exists := seen[id]; exists {
			return
		}
		title := chileText(link)
		if title == "" {
			return
		}

		company, location, published := "", "", ""
		metas := card.Find("h3.meta")
		if metas.Length() > 0 {
			first := metas.Eq(0)
			city := first.Find("a[href*='/ciudad/']").First()
			location = chileText(city)
			companyText := chileText(first)
			if location != "" {
				pattern := regexp.MustCompile(`\s*` + regexp.QuoteMeta(location) + `\s*$`)
				companyText = pattern.ReplaceAllString(companyText, "")
			}
			company = strings.Trim(companyText, " ,")
		}
		for index := 0; index < metas.Length(); index++ {
			meta := metas.Eq(index)
			if meta.Find("i.fa-calendar").Length() > 0 {
				published = chileText(meta)
				break
			}
		}

		description := ""
		descriptionElement := card.Find("p.description").First()
		if descriptionElement.Length() > 0 {
			descriptionElement.Find(".ver-mas-btn").Remove()
			description = chileText(descriptionElement)
		}

		seen[id] = struct{}{}
		results = append(results, job.Job{
			ID:             id,
			Title:          title,
			Company:        chileOrUnspecified(company),
			Location:       chileOrUnspecified(location),
			URL:            jobURL,
			Source:         ChileTrabajosSource,
			PublishedAt:    published,
			CompanyLogoURL: chileLogoURL(card),
			Description:    description,
		})
	})
	return results
}

func chileOrUnspecified(value string) string {
	if strings.TrimSpace(value) == "" {
		return "No especificada"
	}
	return value
}

func chileParseTotal(raw string) (int, bool) {
	document, err := goquery.NewDocumentFromReader(strings.NewReader(raw))
	if err != nil {
		return 0, false
	}
	total, found := 0, false
	document.Find("h2").EachWithBreak(func(_ int, heading *goquery.Selection) bool {
		match := chileTotalPattern.FindStringSubmatch(chileText(heading))
		if match == nil {
			return true
		}
		value, err := strconv.Atoi(strings.ReplaceAll(match[1], ".", ""))
		if err != nil {
			return true
		}
		total = value
		found = true
		return false
	})
	if !found {
		return 0, false
	}
	return total, true
}

func chileObject(value any) map[string]any {
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	return object
}

func chileString(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return text
}

func chileUnescape(value any) string {
	return html.UnescapeString(strings.TrimSpace(chileString(value)))
}

func chileParseDetail(raw string) map[string]any {
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
			if list, ok := payload.([]any); ok {
				items = list
			}
			for _, item := range items {
				object := chileObject(item)
				if object == nil {
					continue
				}
				if chileString(object["@type"]) == "JobPosting" {
					result = object
					return false
				}
			}
			return true
		},
	)
	return result
}

func chileParsePostedDate(value any) (time.Time, bool) {
	text := strings.TrimSpace(chileString(value))
	if text == "" {
		return time.Time{}, false
	}
	text = strings.ReplaceAll(text, "Z", "+00:00")
	if index := strings.IndexByte(text, ' '); index >= 0 {
		text = text[:index] + "T" + text[index+1:]
	}
	for _, layout := range []string{
		time.RFC3339,
		"2006-01-02T15:04:05-0700",
	} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed, true
		}
	}
	for _, layout := range []string{
		"2006-01-02T15:04:05",
		"2006-01-02T15:04:05.999999999",
		"2006-01-02",
	} {
		if parsed, err := time.ParseInLocation(layout, text, chileLocation); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func chileFormatPublished(value time.Time) string {
	return fmt.Sprintf(
		"%d %s %d, %02d:%02d",
		value.Day(), chileMonths[int(value.Month())-1], value.Year(),
		value.Hour(), value.Minute(),
	)
}

func chileJobFromDetail(
	card job.Job, detail map[string]any, logoURL string,
) (job.Job, time.Time, bool) {
	posted, ok := chileParsePostedDate(detail["datePosted"])
	if !ok {
		return job.Job{}, time.Time{}, false
	}

	organization := chileObject(detail["hiringOrganization"])
	jobLocation := chileObject(detail["jobLocation"])
	address := chileObject(jobLocation["address"])

	title := chileUnescape(detail["title"])
	if title == "" {
		title = card.Title
	}
	company := chileUnescape(organization["name"])
	if company == "" {
		company = card.Company
	}
	organizationLogo := chileUnescape(organization["logo"])
	if organizationLogo != "" {
		organizationLogo = chileResolveURL(organizationLogo)
	}

	locality := chileUnescape(address["addressLocality"])
	location := locality
	if location == "" {
		location = card.Location
	}
	if strings.EqualFold(chileString(detail["jobLocationType"]), "TELECOMMUTE") {
		if location != "" && location != "No especificada" {
			location += " (Remoto)"
		} else {
			location = "Remoto"
		}
	}

	description := chileUnescape(detail["description"])
	if description == "" {
		description = card.Description
	}
	description = strings.ReplaceAll(description, "\r\n", "\n")
	description = strings.ReplaceAll(description, "\r", "\n")
	description = strings.TrimSpace(description)

	companyLogo := card.CompanyLogoURL
	if companyLogo == "" {
		companyLogo = organizationLogo
	}
	if companyLogo == "" {
		companyLogo = logoURL
	}

	detailed := card
	detailed.Title = title
	detailed.Company = chileOrUnspecified(company)
	detailed.Location = chileOrUnspecified(location)
	detailed.CompanyLogoURL = companyLogo
	detailed.Description = description
	detailed.PublishedAt = chileFormatPublished(posted)
	publishedAt := posted
	detailed.PublishedAtDatetime = &publishedAt
	return detailed, posted, true
}

var _ Scraper = (*ChileTrabajosScraper)(nil)
