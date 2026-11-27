package scrapers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/rand"
	"net/url"
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
	// GetOnBoardSlug is the stable identifier of the portal.
	GetOnBoardSlug = "getonbrd"
	// GetOnBoardSource is the human-readable name shown in notifications.
	GetOnBoardSource = "Get on Board"

	getOnBoardBaseURL   = "https://www.getonbrd.com"
	getOnBoardSearchURL = getOnBoardBaseURL + "/api/v0/search/jobs"
	getOnBoardExpand    = `["company","tags","location_cities","location_regions","location_tenants"]`

	// GetOnBoardProxyMode and GetOnBoardProxyHost describe the Worker fallback.
	GetOnBoardProxyMode = "fallback"
	GetOnBoardProxyHost = "www.getonbrd.com"

	getOnBoardPageSize     = 50
	getOnBoardMaxPages     = 3
	getOnBoardCacheTTL     = 240 * time.Second
	getOnBoardCacheLimit   = 32
	getOnBoardSearchMinGap = 400 * time.Millisecond
	getOnBoardSearchMaxGap = 900 * time.Millisecond
	getOnBoardUserAgent    = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Safari/537.36"
)

var boardRelationKeys = []string{
	"description", "functions", "desirable", "benefits", "projects",
}

var boardMonthsES = [...]string{
	"ene", "feb", "mar", "abr", "may", "jun",
	"jul", "ago", "sep", "oct", "nov", "dic",
}

// GetOnBoardOptions holds the deterministic dependencies of the scraper.
type GetOnBoardOptions struct {
	Fetcher    httpclient.Fetcher
	Clock      func() time.Time
	CacheClock func() time.Time
	Sleep      func(context.Context, time.Duration) error
	Random     func() float64
}

type searchCacheKey struct {
	termsKey  string
	ageBucket int
}

type boardJob struct {
	Job       job.Job
	Countries []string
	Tenants   []string
	Cities    []string
	Remote    bool
}

type boardResult struct {
	Records      map[string]boardJob
	Order        []string
	PagesFetched int
	FoundCount   int
	Complete     bool
}

func (r boardResult) clone() boardResult {
	records := make(map[string]boardJob, len(r.Records))
	for id, record := range r.Records {
		records[id] = record
	}
	return boardResult{
		Records:      records,
		Order:        append([]string(nil), r.Order...),
		PagesFetched: r.PagesFetched,
		FoundCount:   r.FoundCount,
		Complete:     r.Complete,
	}
}

type cachedSearch struct {
	ExpiresAt    time.Time
	Records      map[string]boardJob
	Order        []string
	PagesFetched int
	FoundCount   int
}

func (c cachedSearch) result() boardResult {
	return boardResult{
		Records:      c.Records,
		Order:        c.Order,
		PagesFetched: c.PagesFetched,
		FoundCount:   c.FoundCount,
		Complete:     true,
	}
}

type searchCall struct {
	done   chan struct{}
	result boardResult
	err    error
}

// GetOnBoardScraper fetches the public Get on Board search API and applies
// location filtering locally. Search results are cached across cycles and
// guilds, keyed by the normalized terms and the bucketed age window.
type GetOnBoardScraper struct {
	BaseScraper

	fetcher    httpclient.Fetcher
	clock      func() time.Time
	cacheClock func() time.Time
	sleep      func(context.Context, time.Duration) error
	random     func() float64

	mu        sync.Mutex
	cache     map[searchCacheKey]cachedSearch
	cacheKeys []searchCacheKey
	inFlight  map[searchCacheKey]*searchCall
}

// NewGetOnBoardScraper builds a scraper. Missing dependencies receive the
// production defaults; tests inject a replay fetcher, clock and sleeper.
func NewGetOnBoardScraper(options GetOnBoardOptions) *GetOnBoardScraper {
	fetcher := options.Fetcher
	if fetcher == nil {
		fetcher = httpclient.NewClient(httpclient.Options{
			Source:     GetOnBoardSource,
			ProxyMode:  GetOnBoardProxyMode,
			ProxyHosts: []string{GetOnBoardProxyHost},
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
	return &GetOnBoardScraper{
		BaseScraper: NewBaseScraper(GetOnBoardSlug, GetOnBoardSource, nil),
		fetcher:     fetcher,
		clock:       clock,
		cacheClock:  cacheClock,
		sleep:       sleeper,
		random:      random,
		cache:       make(map[searchCacheKey]cachedSearch),
		inFlight:    make(map[searchCacheKey]*searchCall),
	}
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("search page delay: %w", ctx.Err())
	}
}

func (s *GetOnBoardScraper) now() time.Time {
	return s.clock().UTC()
}

// Plan maps the neutral location intent to the API's unfiltered query vocabulary.
func (s *GetOnBoardScraper) Plan(intent *locations.LocationIntent) PortalPlan {
	if intent == nil {
		return PortalPlan{
			Supported:      true,
			SearchLocation: "Worldwide",
			Note:           "sin filtro geográfico ni de modalidad: incluye ofertas remotas y presenciales.",
		}
	}
	switch intent.Kind {
	case "worldwide":
		return PortalPlan{
			Supported:      true,
			SearchLocation: "Worldwide",
			Note:           "sin filtro geográfico ni de modalidad: incluye ofertas remotas y presenciales.",
		}
	case "remote":
		country := intent.Country()
		if country == nil {
			return PortalPlan{
				Supported:      true,
				SearchLocation: "Remoto",
				Note:           "solo ofertas remotas de cualquier país.",
			}
		}
		return PortalPlan{
			Supported:      true,
			SearchLocation: "Remoto, " + country.NameES,
			Note:           "solo ofertas remotas disponibles para ese país; la API no distingue país en las remotas globales.",
		}
	case "country":
		if country := intent.Country(); country != nil {
			return PortalPlan{
				Supported:      true,
				SearchLocation: country.NameES,
				Note:           "incluye ofertas presenciales del país y remotas disponibles para él.",
			}
		}
	}
	if intent.Country() == nil {
		searchLocation := intent.City
		if searchLocation == "" {
			searchLocation = "Worldwide"
		}
		return PortalPlan{
			Supported:      true,
			SearchLocation: searchLocation,
			Note:           "la API no filtra por ubicación; se buscan todas las ofertas.",
		}
	}
	countryName := intent.Country().NameES
	place := intent.City
	if place == "" {
		place = countryName
	}
	return PortalPlan{
		Supported:      true,
		SearchLocation: intent.Label(),
		Note: fmt.Sprintf(
			"la API no filtra por ciudad; se muestran ofertas de %s, las que no informan ciudad y las remotas disponibles en %s.",
			place, countryName,
		),
	}
}

// FetchJobs fetches and filters Get on Board results for one location.
func (s *GetOnBoardScraper) FetchJobs(
	ctx context.Context,
	keywords []string,
	location string,
	maxAgeHours float64,
	knownJobIDs map[string]struct{},
) ([]job.Job, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("getonbrd fetch jobs: %w", err)
	}

	terms := cleanBoardTerms(keywords)
	if len(terms) == 0 {
		return []job.Job{}, nil
	}
	intent, err := locations.ParseOrNone(location)
	if err != nil {
		return nil, fmt.Errorf("parse location %q: %w", location, err)
	}
	ageBucket, err := boardAgeBucket(maxAgeHours)
	if err != nil {
		return nil, fmt.Errorf("getonbrd fetch jobs: %w", err)
	}
	key := searchCacheKey{
		termsKey:  boardTermsKey(terms),
		ageBucket: ageBucket,
	}

	s.fetcher.BeginCycle()
	slog.Info("Get on Board transport: direct", "source", s.Source())

	result, err := s.cachedCandidates(ctx, terms, key)
	if err != nil {
		return nil, err
	}

	cutoff := s.now().Add(-time.Duration(maxAgeHours * float64(time.Hour)))
	results := make([]job.Job, 0, len(result.Order))
	knownCount, tooOldCount, locationSkips := 0, 0, 0
	for _, id := range result.Order {
		record, ok := result.Records[id]
		if !ok {
			continue
		}
		if _, known := knownJobIDs[id]; known {
			knownCount++
			continue
		}
		published := record.Job.PublishedAtDatetime
		if published == nil || published.Before(cutoff) {
			tooOldCount++
			continue
		}
		if !s.matchesLocation(record, intent) {
			locationSkips++
			continue
		}
		results = append(results, record.Job)
	}

	slog.Info(
		fmt.Sprintf(
			"Get on Board cycle: keywords=%d pages=%d found=%d unique=%d known=%d too_old=%d location_skips=%d returned=%d",
			len(terms), result.PagesFetched, result.FoundCount, len(result.Records),
			knownCount, tooOldCount, locationSkips, len(results),
		),
		"source", s.Source(),
	)
	return results, nil
}

func cleanBoardTerms(keywords []string) []string {
	seen := make(map[string]struct{}, len(keywords))
	terms := make([]string, 0, len(keywords))
	for _, keyword := range keywords {
		cleaned := strings.TrimSpace(keyword)
		normalized := locations.Normalize(cleaned)
		if cleaned == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		terms = append(terms, cleaned)
	}
	return terms
}

func boardAgeBucket(maxAgeHours float64) (int, error) {
	if math.IsNaN(maxAgeHours) || math.IsInf(maxAgeHours, 0) {
		return 0, fmt.Errorf("invalid max age %.2f", maxAgeHours)
	}
	bucket := int(math.Ceil(maxAgeHours))
	if bucket < 1 {
		bucket = 1
	}
	return bucket, nil
}

func boardTermsKey(terms []string) string {
	normalized := make([]string, len(terms))
	for index, term := range terms {
		normalized[index] = locations.Normalize(term)
	}
	sort.Strings(normalized)
	return strings.Join(normalized, "\x00")
}

func (s *GetOnBoardScraper) cachedCandidates(
	ctx context.Context, terms []string, key searchCacheKey,
) (boardResult, error) {
	now := s.cacheClock()
	s.mu.Lock()
	if cached, ok := s.cache[key]; ok && cached.ExpiresAt.After(now) {
		s.mu.Unlock()
		return cached.result(), nil
	}
	if call, ok := s.inFlight[key]; ok {
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return boardResult{}, fmt.Errorf("wait for Get on Board search: %w", ctx.Err())
		case <-call.done:
			return call.result.clone(), call.err
		}
	}
	call := &searchCall{done: make(chan struct{})}
	s.inFlight[key] = call
	s.mu.Unlock()

	result, err := s.searchCandidates(ctx, terms)
	if err == nil && result.Complete {
		expiresAt := s.cacheClock().Add(getOnBoardCacheTTL)
		stored := cachedSearch{
			ExpiresAt:    expiresAt,
			Records:      result.Records,
			Order:        result.Order,
			PagesFetched: result.PagesFetched,
			FoundCount:   result.FoundCount,
		}
		s.mu.Lock()
		s.pruneCache(s.cacheClock())
		if _, exists := s.cache[key]; !exists {
			s.cacheKeys = append(s.cacheKeys, key)
		}
		s.cache[key] = stored
		s.mu.Unlock()
	}

	s.mu.Lock()
	delete(s.inFlight, key)
	s.mu.Unlock()
	call.result = result.clone()
	call.err = err
	close(call.done)
	return result, err
}

func (s *GetOnBoardScraper) pruneCache(now time.Time) {
	for _, key := range s.cacheKeys {
		if cached, ok := s.cache[key]; ok && !cached.ExpiresAt.After(now) {
			delete(s.cache, key)
		}
	}
	remaining := make([]searchCacheKey, 0, len(s.cacheKeys))
	for _, key := range s.cacheKeys {
		if _, ok := s.cache[key]; ok {
			remaining = append(remaining, key)
		}
	}
	s.cacheKeys = remaining
	for len(s.cacheKeys) > getOnBoardCacheLimit {
		oldest := s.cacheKeys[0]
		delete(s.cache, oldest)
		s.cacheKeys = s.cacheKeys[1:]
	}
}

func (s *GetOnBoardScraper) searchCandidates(
	ctx context.Context, terms []string,
) (boardResult, error) {
	result := boardResult{
		Records:  make(map[string]boardJob),
		Complete: true,
	}
	headers := map[string]string{
		"User-Agent":      getOnBoardUserAgent,
		"Accept":          "application/json",
		"Accept-Language": "es-CL,es;q=0.9,en;q=0.8",
	}

	for _, keyword := range terms {
		if s.fetcher.CircuitOpen() {
			slog.Warn(
				"Get on Board search circuit open; keeping jobs from previous keywords",
				"source", s.Source(),
				"retained", len(result.Records),
			)
			result.Complete = false
			break
		}
		for page := 0; page < getOnBoardMaxPages; page++ {
			params := map[string]string{
				"query":    keyword,
				"page":     strconv.Itoa(page + 1),
				"per_page": strconv.Itoa(getOnBoardPageSize),
				"lang":     "es",
				"expand":   getOnBoardExpand,
			}
			label := fmt.Sprintf("search '%s' page %d", keyword, page+1)
			response, err := s.fetcher.Fetch(ctx, httpclient.Request{
				URL:     getOnBoardSearchURL,
				Params:  params,
				Headers: headers,
				Label:   label,
			})
			if err != nil {
				slog.Warn(
					"Get on Board search stopped for keyword at page",
					"source", s.Source(),
					"keyword", keyword,
					"page", page+1,
					"error", err,
				)
				result.Complete = false
				break
			}
			if !response.IsSuccess() {
				slog.Warn(
					"Get on Board search returned a non-success status",
					"source", s.Source(),
					"keyword", keyword,
					"page", page+1,
					"status", response.StatusCode,
				)
				result.Complete = false
				break
			}

			payload, err := decodeBoardJSON(response.Body)
			if err != nil {
				slog.Warn(
					"Get on Board returned invalid JSON",
					"source", s.Source(),
					"keyword", keyword,
					"page", page+1,
					"error", err,
				)
				result.Complete = false
				break
			}

			data, hasData := boardData(payload)
			pageJobs := parseBoardPayload(payload)
			result.PagesFetched++
			result.FoundCount += len(data)

			newJobs := 0
			for _, record := range pageJobs {
				if _, exists := result.Records[record.Job.ID]; exists {
					continue
				}
				result.Records[record.Job.ID] = record
				result.Order = append(result.Order, record.Job.ID)
				newJobs++
			}

			if !hasData || len(data) < getOnBoardPageSize {
				break
			}
			if newJobs == 0 {
				break
			}
			if page+1 < getOnBoardMaxPages {
				if err := s.sleep(ctx, s.pageDelay()); err != nil {
					return boardResult{}, fmt.Errorf("getonbrd page delay: %w", err)
				}
			}
		}
	}
	return result, nil
}

func (s *GetOnBoardScraper) pageDelay() time.Duration {
	value := s.random()
	if math.IsNaN(value) {
		value = 0.5
	}
	if value < 0 {
		value = 0
	}
	if value > 1 {
		value = 1
	}
	span := getOnBoardSearchMaxGap - getOnBoardSearchMinGap
	return getOnBoardSearchMinGap + time.Duration(value*float64(span))
}

func decodeBoardJSON(body []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var payload any
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("unexpected trailing JSON data")
	}
	return payload, nil
}

func boardData(payload any) ([]any, bool) {
	object, ok := payload.(map[string]any)
	if !ok {
		return nil, false
	}
	data, ok := object["data"].([]any)
	if !ok {
		return nil, false
	}
	return data, true
}

func parseBoardPayload(payload any) []boardJob {
	data, ok := boardData(payload)
	if !ok {
		return nil
	}
	records := make([]boardJob, 0, len(data))
	for _, item := range data {
		if record, ok := boardJobFromItem(item); ok {
			records = append(records, record)
		}
	}
	return records
}

func boardJobFromItem(item any) (boardJob, bool) {
	object, ok := item.(map[string]any)
	if !ok {
		return boardJob{}, false
	}
	rawID, ok := object["id"].(string)
	id := strings.TrimSpace(rawID)
	if !ok || id == "" {
		return boardJob{}, false
	}
	attributes, ok := object["attributes"].(map[string]any)
	if !ok {
		return boardJob{}, false
	}
	rawTitle, ok := attributes["title"].(string)
	if !ok || strings.TrimSpace(rawTitle) == "" {
		return boardJob{}, false
	}
	published := boardPublishedAt(attributes["published_at"])
	if published == nil {
		return boardJob{}, false
	}

	publicURL := ""
	links, _ := object["links"].(map[string]any)
	if value, ok := links["public_url"].(string); ok {
		parsed, err := url.Parse(value)
		if err == nil && isHTTPScheme(parsed.Scheme) {
			publicURL = value
		}
	}
	if publicURL == "" {
		publicURL = getOnBoardBaseURL + "/jobs/" + id
	}

	countries := boardStringList(attributes["countries"])
	tenants := boardRelationNames(attributes["location_tenants"])
	cities := boardRelationNames(attributes["location_cities"])
	regions := boardRelationNames(attributes["location_regions"])
	remote := boardTruthy(attributes["remote"])
	company, logo := boardCompany(attributes)

	record := job.Job{
		ID:                  id,
		Title:               strings.Join(strings.Fields(rawTitle), " "),
		Company:             company,
		Location:            boardLocationText(remote, countries, tenants, cities, regions, attributes["remote_zone"]),
		URL:                 publicURL,
		Source:              GetOnBoardSource,
		PublishedAt:         formatBoardPublished(*published),
		CompanyLogoURL:      logo,
		Description:         boardDescription(attributes),
		PublishedAtDatetime: published,
	}
	return boardJob{
		Job:       record,
		Countries: countries,
		Tenants:   tenants,
		Cities:    cities,
		Remote:    remote,
	}, true
}

func boardPublishedAt(value any) *time.Time {
	var seconds int64
	switch typed := value.(type) {
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			seconds = parsed
		} else {
			parsedFloat, err := typed.Float64()
			if err != nil || math.IsNaN(parsedFloat) || math.IsInf(parsedFloat, 0) {
				return nil
			}
			seconds = int64(parsedFloat)
		}
	case float64:
		if math.IsNaN(typed) || math.IsInf(typed, 0) {
			return nil
		}
		seconds = int64(typed)
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err != nil {
			return nil
		}
		seconds = parsed
	default:
		return nil
	}
	if seconds <= 0 {
		return nil
	}
	published := time.Unix(seconds, 0).UTC()
	return &published
}

func formatBoardPublished(value time.Time) string {
	utc := value.UTC()
	return fmt.Sprintf(
		"%d %s %d, %02d:%02d",
		utc.Day(), boardMonthsES[int(utc.Month())-1], utc.Year(), utc.Hour(), utc.Minute(),
	)
}

func isHTTPScheme(scheme string) bool {
	return strings.EqualFold(scheme, "http") || strings.EqualFold(scheme, "https")
}

func boardStringList(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	values := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			if trimmed := strings.TrimSpace(text); trimmed != "" {
				values = append(values, trimmed)
			}
		}
	}
	return values
}

func boardRelationNames(value any) []string {
	object, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	var items []any
	switch data := object["data"].(type) {
	case map[string]any:
		items = []any{data}
	case []any:
		items = data
	default:
		return nil
	}

	seen := make(map[string]struct{}, len(items))
	names := make([]string, 0, len(items))
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		attributes, ok := entry["attributes"].(map[string]any)
		if !ok {
			continue
		}
		rawName, ok := attributes["name"].(string)
		if !ok {
			continue
		}
		name := strings.Join(strings.Fields(rawName), " ")
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names
}

func boardTruthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case float64:
		return typed != 0
	case json.Number:
		parsed, err := typed.Float64()
		return err != nil || parsed != 0
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}

func boardCompany(attributes map[string]any) (string, string) {
	company, ok := attributes["company"].(map[string]any)
	if !ok {
		return "No especificada", ""
	}
	data, ok := company["data"].(map[string]any)
	if !ok {
		return "No especificada", ""
	}
	companyAttributes, ok := data["attributes"].(map[string]any)
	if !ok {
		return "No especificada", ""
	}
	name := strings.TrimSpace(boardString(companyAttributes["name"]))
	logo := strings.TrimSpace(boardString(companyAttributes["logo"]))
	if logo != "" {
		logo = resolveBoardURL(logo)
	}
	if name == "" {
		name = "No especificada"
	}
	return name, logo
}

func boardString(value any) string {
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return text
}

func resolveBoardURL(value string) string {
	base, err := url.Parse(getOnBoardBaseURL)
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

func boardDescription(attributes map[string]any) string {
	sections := make([]string, 0, len(boardRelationKeys)+1)
	for _, key := range boardRelationKeys {
		if text := boardHTMLText(attributes[key]); text != "" {
			sections = append(sections, text)
		}
	}
	if tags := boardRelationNames(attributes["tags"]); len(tags) > 0 {
		sections = append(sections, "Tecnologías: "+strings.Join(tags, ", "))
	}
	return strings.Join(sections, "\n\n")
}

func boardHTMLText(value any) string {
	raw, ok := value.(string)
	if !ok || strings.TrimSpace(raw) == "" {
		return ""
	}
	document, err := goquery.NewDocumentFromReader(strings.NewReader(raw))
	if err != nil {
		return strings.Join(strings.Fields(raw), " ")
	}
	parts := make([]string, 0, 8)
	collectBoardText(document.Selection, &parts)
	return strings.Join(strings.Fields(strings.Join(parts, " ")), " ")
}

func collectBoardText(selection *goquery.Selection, parts *[]string) {
	if selection == nil {
		return
	}
	selection.Contents().Each(func(_ int, child *goquery.Selection) {
		switch goquery.NodeName(child) {
		case "#text":
			if text := strings.TrimSpace(child.Text()); text != "" {
				*parts = append(*parts, text)
			}
		case "#comment":
			return
		default:
			collectBoardText(child, parts)
		}
	})
}

func boardLocationText(
	remote bool,
	countries, tenants, cities, regions []string,
	remoteZone any,
) string {
	if remote {
		zone := strings.TrimSpace(boardString(remoteZone))
		detail := zone
		if detail == "" {
			if len(tenants) > 0 {
				detail = strings.Join(tenants, ", ")
			} else {
				detail = strings.Join(regions, ", ")
			}
		}
		if detail == "" {
			filtered := make([]string, 0, len(countries))
			for _, country := range countries {
				normalized := locations.Normalize(country)
				if normalized != "remote" && normalized != "remoto" {
					filtered = append(filtered, country)
				}
			}
			detail = strings.Join(filtered, ", ")
		}
		if detail != "" {
			return "Remoto · " + detail
		}
		return "Remoto"
	}
	if len(cities) > 0 {
		if len(countries) > 0 {
			return strings.Join(cities, ", ") + " · " + strings.Join(countries, ", ")
		}
		return strings.Join(cities, ", ")
	}
	if len(regions) > 0 {
		return strings.Join(regions, ", ")
	}
	if len(countries) > 0 {
		return strings.Join(countries, ", ")
	}
	return "No especificada"
}

func (s *GetOnBoardScraper) matchesLocation(
	record boardJob, intent *locations.LocationIntent,
) bool {
	if intent == nil || intent.Kind == "worldwide" {
		return true
	}
	if intent.Kind == "remote" {
		if !record.Remote {
			return false
		}
		country := intent.Country()
		if country == nil {
			return true
		}
		return boardServesCountry(record, country)
	}

	country := intent.Country()
	if country == nil {
		return true
	}
	if intent.Kind == "country" {
		return boardServesCountry(record, country)
	}

	city := locations.Normalize(intent.City)
	if city != "" {
		for _, value := range record.Cities {
			if locations.Normalize(value) == city {
				return true
			}
		}
	}
	if len(record.Cities) == 0 && boardServesCountry(record, country) {
		return true
	}
	return record.Remote && boardServesCountry(record, country)
}

func boardServesCountry(record boardJob, country *data.Country) bool {
	if country == nil {
		return false
	}
	needles := map[string]struct{}{
		locations.Normalize(country.NameEN): {},
		locations.Normalize(country.NameES): {},
	}
	for _, value := range record.Countries {
		if _, ok := needles[locations.Normalize(value)]; ok {
			return true
		}
	}
	for _, tenant := range record.Tenants {
		if _, ok := needles[locations.Normalize(tenant)]; ok {
			return true
		}
	}
	return record.Remote && len(record.Tenants) == 0
}

var _ Scraper = (*GetOnBoardScraper)(nil)
