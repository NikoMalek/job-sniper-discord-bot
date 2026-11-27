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

	"github.com/NikoMalek/job-sniper-discord-bot/internal/httpclient"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/job"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/locations"
	"github.com/PuerkitoBio/goquery"
)

const (
	// LinkedInSlug is the stable identifier of the portal.
	LinkedInSlug = "linkedin"
	// LinkedInSource is the human-readable name shown in notifications.
	LinkedInSource = "LinkedIn"

	linkedInEndpoint = "https://www.linkedin.com/jobs-guest/jobs/api/seeMoreJobPostings/search"
	linkedInBaseURL  = "https://www.linkedin.com"
	// LinkedInProxyMode describes the Worker fallback transport.
	LinkedInProxyMode = "fallback"

	linkedInSearchRetries    = 2
	linkedInDetailRetries    = 2
	linkedInMaxSearchPages   = 10
	linkedInSearchPageSize   = 10
	linkedInDetailMinDelay   = 800 * time.Millisecond
	linkedInDetailMaxDelay   = 1400 * time.Millisecond
	linkedInSearchMinDelay   = time.Second
	linkedInSearchMaxDelay   = 2 * time.Second
	linkedInDescriptionTTL   = 30 * time.Minute
	linkedInDescriptionLimit = 500
	linkedInUnknownText      = "No especificada"
)

var (
	linkedInUserAgents = []string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Safari/537.36",
		"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15",
		"Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0 Safari/537.36",
	}
	linkedInRelativePattern = regexp.MustCompile(
		`(?i)(\d+)\+?\s*(minute|minutes|minuto|minutos|hour|hours|hora|horas|` +
			`day|days|día|días|week|weeks|semana|semanas|` +
			`month|months|mes|meses|year|years|año|años)\b`,
	)
	linkedInTrackingPrefixes = []string{"trk", "tracking", "refid"}
)

// LinkedInProxyHosts returns the fixed Worker fallback hosts.
func LinkedInProxyHosts() []string {
	return []string{"www.linkedin.com", "cl.linkedin.com"}
}

// LinkedInOptions holds the deterministic dependencies of the scraper.
type LinkedInOptions struct {
	Fetcher    httpclient.Fetcher
	Clock      func() time.Time
	CacheClock func() time.Time
	Sleep      func(context.Context, time.Duration) error
	Random     func() float64
}

type linkedInCacheKey struct {
	TermsKey    string
	Location    string
	MaxAgeHours float64
}

type linkedInSearchResult struct {
	Order    []string
	Cards    map[string]job.Job
	Pages    int
	Found    int
	Complete bool
}

func (r linkedInSearchResult) clone() linkedInSearchResult {
	clone := linkedInSearchResult{
		Order:    append([]string(nil), r.Order...),
		Cards:    make(map[string]job.Job, len(r.Cards)),
		Pages:    r.Pages,
		Found:    r.Found,
		Complete: r.Complete,
	}
	for id, record := range r.Cards {
		clone.Cards[id] = record
	}
	return clone
}

type linkedInSearchCall struct {
	done   chan struct{}
	result linkedInSearchResult
	err    error
}

type linkedInDescriptionEntry struct {
	ExpiresAt   time.Time
	Description string
}

// LinkedInScraper searches the guest jobs endpoint and enriches each new card
// with its visible description. Search results live for one cycle, while
// descriptions are cached across cycles.
type LinkedInScraper struct {
	BaseScraper

	fetcher    httpclient.Fetcher
	clock      func() time.Time
	cacheClock func() time.Time
	sleep      func(context.Context, time.Duration) error
	random     func() float64

	mu               sync.Mutex
	searchCache      map[linkedInCacheKey]linkedInSearchResult
	searchCalls      map[linkedInCacheKey]*linkedInSearchCall
	searchCacheHit   bool
	description      map[string]linkedInDescriptionEntry
	descriptionOrder []string
}

// NewLinkedInScraper builds a scraper. Missing dependencies receive the
// production defaults; tests inject a replay fetcher, clocks and sleeper.
func NewLinkedInScraper(options LinkedInOptions) *LinkedInScraper {
	fetcher := options.Fetcher
	if fetcher == nil {
		fetcher = httpclient.NewClient(httpclient.Options{
			Source:     LinkedInSource,
			ProxyMode:  LinkedInProxyMode,
			ProxyHosts: LinkedInProxyHosts(),
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
	return &LinkedInScraper{
		BaseScraper:      NewBaseScraper(LinkedInSlug, LinkedInSource, nil),
		fetcher:          fetcher,
		clock:            clock,
		cacheClock:       cacheClock,
		sleep:            sleeper,
		random:           random,
		searchCache:      make(map[linkedInCacheKey]linkedInSearchResult),
		searchCalls:      make(map[linkedInCacheKey]*linkedInSearchCall),
		description:      make(map[string]linkedInDescriptionEntry),
		descriptionOrder: make([]string, 0),
	}
}

// BeginCycle clears only the per-cycle search cache. Descriptions survive.
func (s *LinkedInScraper) BeginCycle() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.searchCache = make(map[linkedInCacheKey]linkedInSearchResult)
}

// Plan maps a neutral location intent to LinkedIn's search vocabulary.
func (s *LinkedInScraper) Plan(intent *locations.LocationIntent) PortalPlan {
	if intent == nil {
		return PortalPlan{Supported: true, SearchLocation: ""}
	}
	switch intent.Kind {
	case "worldwide":
		return PortalPlan{
			Supported:      true,
			SearchLocation: "Worldwide",
			Note: "sin filtro de modalidad: incluye ofertas remotas y " +
				"presenciales de todo el mundo.",
		}
	case "remote":
		country := intent.Country()
		if country == nil {
			return PortalPlan{
				Supported:      true,
				SearchLocation: "Remote",
				Note:           "solo ofertas remotas de todo el mundo.",
			}
		}
		return PortalPlan{
			Supported:      true,
			SearchLocation: country.NameEN,
			Note: "LinkedIn no permite filtrar solo remoto; se buscará " +
				"todo el país.",
		}
	case "country":
		if country := intent.Country(); country != nil {
			return PortalPlan{Supported: true, SearchLocation: country.NameEN}
		}
	}
	country := intent.Country()
	if country == nil {
		return PortalPlan{Supported: true, SearchLocation: intent.City}
	}
	if linkedInEndsWithCountry(intent.City, country.NameEN) {
		return PortalPlan{Supported: true, SearchLocation: intent.City}
	}
	return PortalPlan{
		Supported:      true,
		SearchLocation: intent.City + ", " + country.NameEN,
	}
}

func linkedInEndsWithCountry(city, country string) bool {
	suffix := "de " + country
	cityRunes, suffixRunes := []rune(city), []rune(suffix)
	if len(suffixRunes) > len(cityRunes) {
		return false
	}
	start := len(cityRunes) - len(suffixRunes)
	for index, char := range suffixRunes {
		if !equalFoldRune(cityRunes[start+index], char) {
			return false
		}
	}
	return true
}

func equalFoldRune(left, right rune) bool {
	return strings.EqualFold(string(left), string(right))
}

func (s *LinkedInScraper) now() time.Time { return s.clock().UTC() }

// FetchJobs searches LinkedIn and enriches every unknown job with its
// description. A failed detail fetch is deferred to the next cycle.
func (s *LinkedInScraper) FetchJobs(
	ctx context.Context,
	keywords []string,
	location string,
	maxAgeHours float64,
	knownJobIDs map[string]struct{},
) ([]job.Job, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("linkedin fetch jobs: %w", err)
	}
	ageSeconds, err := linkedInAgeSeconds(maxAgeHours)
	if err != nil {
		return nil, fmt.Errorf("linkedin fetch jobs: %w", err)
	}
	terms := linkedInKeywords(keywords)
	params := map[string]string{
		"keywords": strings.Join(terms, " OR "),
		"location": location,
		"f_TPR":    "r" + strconv.FormatInt(ageSeconds, 10),
		"sortBy":   "DD",
	}
	headers := map[string]string{
		"User-Agent":      linkedInUserAgent(s.random()),
		"Accept-Language": "en-US,en;q=0.9",
	}
	key := linkedInCacheKey{
		TermsKey:    linkedInTermsKey(terms),
		Location:    location,
		MaxAgeHours: maxAgeHours,
	}

	s.fetcher.BeginCycle()
	slog.Info("LinkedIn transport: proxy fallback (www.linkedin.com, cl.linkedin.com)")
	result, err := s.cachedSearch(ctx, params, key, headers)
	if err != nil {
		return nil, err
	}

	known := knownJobIDs
	if known == nil {
		known = map[string]struct{}{}
	}
	knownCount, cachedCount, fetchedCount := 0, 0, 0
	withoutDescription, deferredCount := 0, 0
	enriched := make([]job.Job, 0, len(result.Order))

	for _, id := range result.Order {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("linkedin detail pass: %w", err)
		}
		cardJob, exists := result.Cards[id]
		if !exists {
			continue
		}
		if _, exists := known[id]; exists {
			knownCount++
			continue
		}
		if s.fetcher.CircuitOpen() {
			deferredCount++
			continue
		}
		if description, ok := s.cachedDescription(id); ok {
			cachedCount++
			if description == "" {
				enriched = append(enriched, cardJob)
			} else {
				record := cardJob
				record.Description = description
				enriched = append(enriched, record)
			}
			continue
		}

		response, err := s.fetcher.Fetch(ctx, httpclient.Request{
			URL:     cardJob.URL,
			Headers: headers,
			Retries: linkedInRetryCount(linkedInDetailRetries),
			Label:   "job " + id,
		})
		switch {
		case err != nil || response == nil:
			slog.Warn(
				"LinkedIn description unavailable; deferring to next cycle",
				"job", id, "error", err,
			)
			deferredCount++
			continue
		case !response.IsSuccess():
			slog.Warn(
				"LinkedIn returned a non-success status; deferring to next cycle",
				"job", id, "status", response.StatusCode,
			)
			deferredCount++
			continue
		case httpclient.IsAuthwall(response):
			slog.Warn("LinkedIn authwall for job; deferring to next cycle", "job", id)
			deferredCount++
			continue
		}

		description := linkedInParseDescription(response.Text())
		fetchedCount++
		s.storeDescription(id, description)
		if description == "" {
			withoutDescription++
			enriched = append(enriched, cardJob)
		} else {
			record := cardJob
			record.Description = description
			enriched = append(enriched, record)
		}
		if err := s.sleep(ctx, s.detailDelay()); err != nil {
			return nil, fmt.Errorf("linkedin detail delay: %w", err)
		}
	}

	searchName := s.lastSearchSource()
	slog.Info(fmt.Sprintf(
		"LinkedIn cycle: search=%s pages=%d found=%d unique=%d known=%d cached=%d fetched=%d without_description=%d deferred=%d",
		searchName, result.Pages, result.Found, len(result.Order), knownCount,
		cachedCount, fetchedCount, withoutDescription, deferredCount,
	))
	return enriched, nil
}

// lastSearchSource is supplied by cachedSearch through a tiny piece of state
// so the cycle log can distinguish live and cached searches.
func (s *LinkedInScraper) lastSearchSource() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.searchCacheHit {
		return "cache"
	}
	return "live"
}

func linkedInAgeSeconds(maxAgeHours float64) (int64, error) {
	if math.IsNaN(maxAgeHours) || math.IsInf(maxAgeHours, 0) {
		return 0, fmt.Errorf("invalid max age %.2f", maxAgeHours)
	}
	return int64(math.Ceil(maxAgeHours * 60 * 60)), nil
}

func linkedInKeywords(keywords []string) []string {
	terms := make([]string, 0, len(keywords))
	for _, keyword := range keywords {
		if value := strings.TrimSpace(keyword); value != "" {
			terms = append(terms, value)
		}
	}
	return terms
}

func linkedInTermsKey(terms []string) string {
	sorted := append([]string(nil), terms...)
	sort.Strings(sorted)
	return strings.Join(sorted, "\x00")
}

func linkedInRetryCount(retries int) *int { return &retries }

func linkedInUserAgent(value float64) string {
	index := int(math.Floor(value * float64(len(linkedInUserAgents))))
	if index < 0 {
		index = 0
	}
	if index >= len(linkedInUserAgents) {
		index = len(linkedInUserAgents) - 1
	}
	return linkedInUserAgents[index]
}

func (s *LinkedInScraper) detailDelay() time.Duration {
	return linkedInDelay(linkedInDetailMinDelay, linkedInDetailMaxDelay, s.random())
}

func (s *LinkedInScraper) searchDelay() time.Duration {
	return linkedInDelay(linkedInSearchMinDelay, linkedInSearchMaxDelay, s.random())
}

func linkedInDelay(minimum, maximum time.Duration, value float64) time.Duration {
	if math.IsNaN(value) {
		value = 0.5
	}
	if value < 0 {
		value = 0
	}
	if value > 1 {
		value = 1
	}
	return minimum + time.Duration(value*float64(maximum-minimum))
}

func (s *LinkedInScraper) cachedSearch(
	ctx context.Context,
	params map[string]string,
	key linkedInCacheKey,
	headers map[string]string,
) (linkedInSearchResult, error) {
	s.mu.Lock()
	if cached, ok := s.searchCache[key]; ok {
		s.searchCacheHit = true
		s.mu.Unlock()
		return cached.clone(), nil
	}
	if call, ok := s.searchCalls[key]; ok {
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return linkedInSearchResult{}, fmt.Errorf("wait for LinkedIn search: %w", ctx.Err())
		case <-call.done:
			return call.result.clone(), call.err
		}
	}
	s.searchCacheHit = false
	call := &linkedInSearchCall{done: make(chan struct{})}
	s.searchCalls[key] = call
	s.mu.Unlock()

	result, err := s.searchJobs(ctx, params, headers)
	if err == nil && result.Complete {
		s.mu.Lock()
		s.searchCache[key] = result.clone()
		s.mu.Unlock()
	}
	call.result = result.clone()
	call.err = err
	s.mu.Lock()
	delete(s.searchCalls, key)
	s.mu.Unlock()
	close(call.done)
	return result, err
}

func (s *LinkedInScraper) searchJobs(
	ctx context.Context,
	params map[string]string,
	headers map[string]string,
) (linkedInSearchResult, error) {
	result := linkedInSearchResult{Cards: make(map[string]job.Job), Complete: true}
	pageParams := make(map[string]string, len(params)+1)

	for page := 0; page < linkedInMaxSearchPages; page++ {
		if err := ctx.Err(); err != nil {
			return linkedInSearchResult{}, fmt.Errorf("linkedin search: %w", err)
		}
		if s.fetcher.CircuitOpen() {
			slog.Warn(
				"LinkedIn search circuit open; keeping jobs from previous pages",
				"page", page+1, "retained", len(result.Cards),
			)
			result.Complete = false
			break
		}
		for name, value := range params {
			pageParams[name] = value
		}
		pageParams["start"] = strconv.Itoa(page * linkedInSearchPageSize)
		response, err := s.fetcher.Fetch(ctx, httpclient.Request{
			URL:     linkedInEndpoint,
			Params:  pageParams,
			Headers: headers,
			Retries: linkedInRetryCount(linkedInSearchRetries),
			Label:   fmt.Sprintf("search page %d", page+1),
		})
		if err != nil || response == nil {
			slog.Warn(
				"LinkedIn search stopped; keeping jobs from previous pages",
				"page", page+1, "retained", len(result.Cards), "error", err,
			)
			result.Complete = false
			break
		}
		if httpclient.IsAuthwall(response) {
			slog.Warn("LinkedIn search authwall; stopping", "page", page+1)
			result.Complete = false
			break
		}
		if !response.IsSuccess() {
			slog.Warn(
				"LinkedIn search returned a non-success status; stopping",
				"page", page+1, "status", response.StatusCode,
			)
			result.Complete = false
			break
		}

		pageJobs := s.parseJobs(response.Text())
		result.Pages++
		result.Found += len(pageJobs)
		if len(pageJobs) == 0 {
			break
		}
		newJobs := 0
		for _, record := range pageJobs {
			if _, exists := result.Cards[record.ID]; exists {
				continue
			}
			result.Cards[record.ID] = record
			result.Order = append(result.Order, record.ID)
			newJobs++
		}
		if newJobs == 0 {
			break
		}
		if page+1 < linkedInMaxSearchPages {
			if err := s.sleep(ctx, s.searchDelay()); err != nil {
				return linkedInSearchResult{}, fmt.Errorf("linkedin search delay: %w", err)
			}
		}
	}
	return result, nil
}

func (s *LinkedInScraper) parseJobs(raw string) []job.Job {
	document, err := goquery.NewDocumentFromReader(strings.NewReader(raw))
	if err != nil {
		return nil
	}
	results := make([]job.Job, 0, linkedInSearchPageSize)
	seen := make(map[string]struct{})

	document.Find("li").Each(func(_ int, card *goquery.Selection) {
		link := card.Find("a.base-card__full-link, a.base-card-card__full-link").First()
		if link.Length() == 0 {
			link = card.Find("a[href*='/jobs/view/']").First()
		}
		if link.Length() == 0 {
			return
		}
		href, exists := link.Attr("href")
		if !exists || strings.TrimSpace(href) == "" {
			return
		}
		cleaned := linkedInCleanURL(href)
		if cleaned == "" {
			return
		}
		id := linkedInJobID(cleaned)
		if id == "" {
			return
		}
		if _, exists := seen[id]; exists {
			return
		}
		title := linkedInTextOrFallback(
			card, "h3.base-search-card__title", "h3",
		)
		if title == "" {
			return
		}
		company := linkedInTextOrFallback(
			card, "h4.base-search-card__subtitle", "h4",
		)
		location := linkedInTextOrFallback(
			card, "span.job-search-card__location", "span",
		)
		published := linkedInText(card.Find("time").First())

		seen[id] = struct{}{}
		publishedAt := s.publishedDatetime(published)
		results = append(results, job.Job{
			ID:                  id,
			Title:               title,
			Company:             linkedInOrUnspecified(company),
			Location:            linkedInOrUnspecified(location),
			URL:                 cleaned,
			Source:              LinkedInSource,
			PublishedAt:         published,
			CompanyLogoURL:      linkedInLogoURL(card.Find("img").First()),
			Description:         linkedInText(card.Find(".base-search-card__snippet, .job-search-card__snippet, .base-card__snippet, p.description").First()),
			PublishedAtDatetime: publishedAt,
		})
	})
	return results
}

func linkedInTextOrFallback(selection *goquery.Selection, primary, fallback string) string {
	if text := linkedInText(selection.Find(primary).First()); text != "" {
		return text
	}
	return linkedInText(selection.Find(fallback).First())
}

func linkedInText(selection *goquery.Selection) string {
	if selection == nil {
		return ""
	}
	parts := make([]string, 0, 8)
	collectLinkedInText(selection, &parts)
	return strings.Join(parts, " ")
}

// collectLinkedInText mirrors BeautifulSoup's get_text(" ", strip=True):
// every text node is stripped at its edges but interior whitespace, including
// non-breaking spaces, is preserved.
func collectLinkedInText(selection *goquery.Selection, parts *[]string) {
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
			collectLinkedInText(child, parts)
		}
	})
}

func linkedInOrUnspecified(value string) string {
	if strings.TrimSpace(value) == "" {
		return linkedInUnknownText
	}
	return value
}

func linkedInLogoURL(image *goquery.Selection) string {
	if image == nil || image.Length() == 0 {
		return ""
	}
	for _, attribute := range []string{
		"src", "data-src", "data-delayed-url", "data-ghost-url",
	} {
		value, exists := image.Attr(attribute)
		if !exists || strings.TrimSpace(value) == "" {
			continue
		}
		if resolved := firstJobResolveURL(strings.TrimSpace(value), linkedInBaseURL); resolved != "" {
			return resolved
		}
	}
	value, exists := image.Attr("srcset")
	if exists {
		candidates := strings.Split(value, ",")
		if len(candidates) > 0 {
			fields := strings.Fields(candidates[0])
			if len(fields) > 0 {
				if resolved := firstJobResolveURL(fields[0], linkedInBaseURL); resolved != "" {
					return resolved
				}
			}
		}
	}
	return ""
}

func linkedInCleanURL(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return ""
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		resolved, err := url.Parse(linkedInBaseURL)
		if err != nil {
			return ""
		}
		parsed = resolved.ResolveReference(parsed)
	}
	if !isHTTPScheme(parsed.Scheme) {
		return ""
	}

	query := make([]string, 0, 4)
	for _, pair := range strings.Split(parsed.RawQuery, "&") {
		if pair == "" {
			continue
		}
		parts := strings.SplitN(pair, "=", 2)
		name, err := url.QueryUnescape(parts[0])
		if err != nil {
			continue
		}
		if linkedInIsTrackingKey(name) {
			continue
		}
		value := ""
		if len(parts) == 2 {
			value, err = url.QueryUnescape(parts[1])
			if err != nil {
				continue
			}
		}
		query = append(query, url.QueryEscape(name)+"="+url.QueryEscape(value))
	}

	cleaned := parsed.Scheme + "://" + parsed.Host + parsed.EscapedPath()
	if len(query) > 0 {
		cleaned += "?" + strings.Join(query, "&")
	}
	return cleaned
}

func linkedInIsTrackingKey(name string) bool {
	lowered := strings.ToLower(name)
	for _, prefix := range linkedInTrackingPrefixes {
		if strings.HasPrefix(lowered, prefix) {
			return true
		}
	}
	return false
}

func linkedInJobID(cleanedURL string) string {
	parsed, err := url.Parse(cleanedURL)
	if err != nil {
		return ""
	}
	parts := strings.Split(parsed.EscapedPath(), "/")
	for index := len(parts) - 1; index >= 0; index-- {
		if parts[index] != "" {
			return parts[index]
		}
	}
	return ""
}

func (s *LinkedInScraper) publishedDatetime(value string) *time.Time {
	now := s.now()
	text := strings.Join(strings.Fields(strings.ToLower(value)), " ")
	if text == "" {
		return nil
	}
	switch text {
	case "just now", "ahora", "hace unos segundos":
		return &now
	}
	match := linkedInRelativePattern.FindStringSubmatch(text)
	if match == nil {
		return nil
	}
	amount, err := strconv.Atoi(match[1])
	if err != nil || amount < 0 {
		return nil
	}
	published := linkedInSubtract(now, match[2], amount)
	return &published
}

func linkedInSubtract(now time.Time, unit string, amount int) time.Time {
	switch strings.ToLower(unit) {
	case "minute", "minutes", "minuto", "minutos":
		return now.Add(-time.Duration(amount) * time.Minute)
	case "hour", "hours", "hora", "horas":
		return now.Add(-time.Duration(amount) * time.Hour)
	case "day", "days", "día", "días":
		return now.AddDate(0, 0, -amount)
	case "week", "weeks", "semana", "semanas":
		return now.AddDate(0, 0, -7*amount)
	case "month", "months", "mes", "meses":
		return now.AddDate(0, 0, -30*amount)
	default:
		return now.AddDate(0, 0, -365*amount)
	}
}

func linkedInParseDescription(raw string) string {
	document, err := goquery.NewDocumentFromReader(strings.NewReader(raw))
	if err != nil {
		return ""
	}
	selection := document.Find(
		".show-more-less-html__markup, .description__text, " +
			".jobs-description__content, [class*='description']",
	).First()
	if selection.Length() > 0 {
		return linkedInText(selection)
	}
	meta := document.Find("meta[name='description']").First()
	if meta.Length() == 0 {
		return ""
	}
	value, exists := meta.Attr("content")
	if !exists {
		return ""
	}
	return strings.TrimSpace(value)
}

func (s *LinkedInScraper) cachedDescription(id string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, exists := s.description[id]
	if !exists {
		return "", false
	}
	if !entry.ExpiresAt.After(s.cacheClock()) {
		delete(s.description, id)
		s.removeDescriptionOrder(id)
		return "", false
	}
	return entry.Description, true
}

func (s *LinkedInScraper) removeDescriptionOrder(id string) {
	remaining := s.descriptionOrder[:0]
	for _, value := range s.descriptionOrder {
		if value != id {
			remaining = append(remaining, value)
		}
	}
	s.descriptionOrder = remaining
}

func (s *LinkedInScraper) storeDescription(id, description string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.cacheClock()
	if len(s.description) >= linkedInDescriptionLimit {
		for _, key := range s.descriptionOrder {
			entry := s.description[key]
			if !entry.ExpiresAt.After(now) {
				delete(s.description, key)
			}
		}
		remaining := s.descriptionOrder[:0]
		for _, key := range s.descriptionOrder {
			if _, exists := s.description[key]; exists {
				remaining = append(remaining, key)
			}
		}
		s.descriptionOrder = remaining
		for len(s.description) >= linkedInDescriptionLimit && len(s.descriptionOrder) > 0 {
			oldest := s.descriptionOrder[0]
			delete(s.description, oldest)
			s.descriptionOrder = s.descriptionOrder[1:]
		}
	}
	if _, exists := s.description[id]; !exists {
		s.descriptionOrder = append(s.descriptionOrder, id)
	}
	s.description[id] = linkedInDescriptionEntry{
		ExpiresAt:   now.Add(linkedInDescriptionTTL),
		Description: description,
	}
}
