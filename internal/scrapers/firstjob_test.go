package scrapers

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NikoMalek/job-sniper-discord-bot/internal/httpclient"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/job"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/locations"
)

type firstJobFixtureMeta struct {
	Portal string `json:"portal"`
	Search struct {
		Name     string   `json:"name"`
		Keywords []string `json:"keywords"`
		Location string   `json:"location"`
	} `json:"search"`
	MaxAgeHours    float64   `json:"max_age_hours"`
	SearchLocation string    `json:"search_location"`
	CapturedAt     time.Time `json:"captured_at"`
	RequestCount   int       `json:"request_count"`
	JobCount       int       `json:"job_count"`
}

func firstJobFixtureDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(fixtureRoot, "firstjob")
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			t.Skip("firstjob fixtures are not available")
		}
		t.Fatalf("stat %s: %v", dir, err)
	}
	return dir
}

func loadFirstJobFixture(
	t *testing.T, search string,
) (firstJobFixtureMeta, []job.Job) {
	t.Helper()
	dir := filepath.Join(firstJobFixtureDir(t), search)

	rawMeta, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		t.Fatalf("read meta.json: %v", err)
	}
	var meta firstJobFixtureMeta
	if err := json.Unmarshal(rawMeta, &meta); err != nil {
		t.Fatalf("parse meta.json: %v", err)
	}

	rawExpected, err := os.ReadFile(filepath.Join(dir, "expected_jobs.json"))
	if err != nil {
		t.Fatalf("read expected_jobs.json: %v", err)
	}
	var expected []job.Job
	if err := json.Unmarshal(rawExpected, &expected); err != nil {
		t.Fatalf("parse expected_jobs.json: %v", err)
	}
	return meta, expected
}

func firstJobNoSleep(context.Context, time.Duration) error { return nil }

type firstJobStubFetcher struct {
	mu           sync.Mutex
	searchBodies map[int]string
	detailBody   string
	calls        []httpclient.Request
	totalCalls   int
}

func (f *firstJobStubFetcher) Fetch(
	ctx context.Context, request httpclient.Request,
) (*httpclient.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	f.mu.Lock()
	f.calls = append(f.calls, request)
	f.totalCalls++
	body := f.detailBody
	if strings.HasPrefix(request.Label, "search ") {
		page := 1
		if value := request.Params["page"]; value != "" {
			if parsed, err := strconv.Atoi(value); err == nil {
				page = parsed
			}
		}
		body = f.searchBodies[page]
	}
	f.mu.Unlock()
	return &httpclient.Response{StatusCode: 200, Body: []byte(body)}, nil
}

func (f *firstJobStubFetcher) BeginCycle() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}

func (f *firstJobStubFetcher) CircuitOpen() bool { return false }

func (f *firstJobStubFetcher) Calls() []httpclient.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]httpclient.Request(nil), f.calls...)
}

func (f *firstJobStubFetcher) TotalCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.totalCalls
}

func newFirstJobFixtureScraper(
	fetcher httpclient.Fetcher, cacheNow *time.Time,
) *FirstJobScraper {
	return NewFirstJobScraper(FirstJobOptions{
		Fetcher:    fetcher,
		Clock:      func() time.Time { return *cacheNow },
		CacheClock: func() time.Time { return *cacheNow },
		Sleep:      firstJobNoSleep,
		Random:     func() float64 { return 0.5 },
	})
}

func TestFirstJobReplaysFixtures(t *testing.T) {
	ctx := context.Background()
	searches := []string{"python-santiago", "backend-remoto", "sin-resultados"}
	cacheNow := time.Unix(0, 0).UTC()

	for _, search := range searches {
		t.Run(search, func(t *testing.T) {
			meta, expected := loadFirstJobFixture(t, search)
			fetcher, err := httpclient.NewReplayFetcher(
				ctx, fixtureRoot, "firstjob", search,
			)
			if err != nil {
				t.Fatalf("new replay fetcher: %v", err)
			}
			scraper := newFirstJobFixtureScraper(fetcher, &cacheNow)

			intent, err := locations.Parse(meta.Search.Location)
			if err != nil {
				t.Fatalf("parse fixture location: %v", err)
			}
			plan := scraper.Plan(&intent)
			if !plan.Supported || plan.SearchLocation != meta.SearchLocation {
				t.Fatalf(
					"plan = %+v, want search location %q", plan, meta.SearchLocation,
				)
			}

			got, err := scraper.FetchJobs(
				ctx, meta.Search.Keywords, meta.Search.Location,
				meta.MaxAgeHours, nil,
			)
			if err != nil {
				t.Fatalf("fetch jobs: %v", err)
			}
			requireSameJobs(t, got, expected)
			if meta.JobCount != len(expected) {
				t.Fatalf(
					"fixture job_count = %d, expected_jobs.json has %d",
					meta.JobCount, len(expected),
				)
			}
			if calls := fetcher.Calls(); len(calls) != meta.RequestCount {
				t.Fatalf("fetcher made %d calls, want %d", len(calls), meta.RequestCount)
			}
			if remaining := fetcher.Remaining(); remaining != 0 {
				t.Fatalf("%d recorded requests were not replayed", remaining)
			}
			cacheNow = cacheNow.Add(time.Minute)
		})
	}
}

func TestFirstJobPlansAndResolvesLocations(t *testing.T) {
	scraper := NewFirstJobScraper(FirstJobOptions{})
	defaultNote := "FirstJob no filtra por modalidad: se buscará en Chile por defecto. " +
		firstJobNoDatesNote
	tests := []struct {
		location string
		want     string
		note     string
	}{
		{"Worldwide", "Chile", defaultNote},
		{"Remoto", "Chile", defaultNote},
		{"Chile", "Chile", firstJobNoDatesNote},
		{
			"Santiago, Chile", "Región Metropolitana de Santiago, Chile",
			firstJobNoDatesNote,
		},
		{
			"Atlantis, Chile", "Chile",
			"No reconoció «Atlantis» entre las regiones de Chile; se buscará en todo el país. " +
				firstJobNoDatesNote,
		},
		{
			"Costa Rica", "Centroamérica",
			"FirstJob agrupa Costa Rica dentro de Centroamérica y no tiene regiones para esa opción; " +
				"se buscará en toda la región (Belice, Costa Rica, El Salvador, Guatemala, Honduras, Nicaragua, Panamá). " +
				firstJobNoDatesNote,
		},
	}
	for _, test := range tests {
		t.Run(test.location, func(t *testing.T) {
			intent, err := locations.Parse(test.location)
			if err != nil {
				t.Fatalf("parse %q: %v", test.location, err)
			}
			if !scraper.Covers(&intent) {
				t.Fatalf("FirstJob does not cover %q", test.location)
			}
			plan := scraper.Plan(&intent)
			if !plan.Supported {
				t.Fatalf("plan for %q is unsupported: %+v", test.location, plan)
			}
			if plan.SearchLocation != test.want {
				t.Errorf("search location = %q, want %q", plan.SearchLocation, test.want)
			}
			if plan.Note != test.note {
				t.Errorf("note = %q, want %q", plan.Note, test.note)
			}
		})
	}

	spain, err := locations.Parse("España")
	if err != nil {
		t.Fatalf("parse España: %v", err)
	}
	if scraper.Covers(&spain) {
		t.Error("FirstJob must not cover Spain")
	}
	if plan := scraper.Plan(&spain); plan.Supported || plan.Reason == "" {
		t.Fatalf("Spain must be unsupported: %+v", plan)
	}

	resolvedTests := []struct {
		location string
		code     string
		regionID string
		matched  bool
	}{
		{"Santiago, Chile", "CL", "13", true},
		{"Chile", "CL", "", true},
		{"Centroamérica", "CAM", "", true},
		{"Costa Rica", "CL", "", false},
		{"Remoto", "CL", "", false},
		{"Atlantis, Chile", "CL", "", false},
	}
	for _, test := range resolvedTests {
		t.Run("resolve "+test.location, func(t *testing.T) {
			resolved := scraper.resolveSearchLocation(test.location)
			if resolved.Country.Code != test.code ||
				resolved.RegionID != test.regionID ||
				resolved.Matched != test.matched {
				t.Fatalf(
					"resolved = %+v, want code=%q region=%q matched=%v",
					resolved, test.code, test.regionID, test.matched,
				)
			}
		})
	}
}

func TestFirstJobSearchParams(t *testing.T) {
	scraper := NewFirstJobScraper(FirstJobOptions{})
	country := scraper.catalog.CountriesByCode["CL"]
	got := firstJobSearchParams("python", country, "13", 2)
	want := map[string]string{
		"title":     "python",
		"country[]": "43",
		"region[]":  "13",
		"page":      "2",
	}
	if len(got) != len(want) {
		t.Fatalf("params = %v, want %v", got, want)
	}
	for key, value := range want {
		if got[key] != value {
			t.Errorf("param %q = %q, want %q", key, got[key], value)
		}
	}
	firstPage := firstJobSearchParams("backend", country, "", 1)
	if _, exists := firstPage["region[]"]; exists {
		t.Error("first page without region must not send region[]")
	}
	if _, exists := firstPage["page"]; exists {
		t.Error("first page must not send page")
	}
}

func TestFirstJobBlockMarkers(t *testing.T) {
	for _, text := range []string{
		"CAPTCHA", "Por favor, no soy un robot", "Verifica que eres humano",
		"Acceso denegado", "Request blocked", "Unusual traffic",
	} {
		if !firstJobLooksBlocked(strings.Repeat("x", 3900) + text) {
			t.Errorf("%q inside first 4000 characters must be detected", text)
		}
	}
	if firstJobLooksBlocked(strings.Repeat("x", 4000) + "captcha") {
		t.Error("marker after the first 4000 characters must be ignored")
	}
	if firstJobLooksBlocked("ofertas recientes") {
		t.Error("ordinary search text must not be treated as a challenge")
	}
}

func TestFirstJobParsesCardsAndDetail(t *testing.T) {
	cardsHTML := `<div class="card-job">
		<a href="/oferta/123?from=list#section">ir</a>
		<div class="card-job-top--info-heading">Dev <span>Python</span></div>
		<div class="card-job-top--company">Acme</div>
		<div class="card-job-top--location">Santiago</div>
		<div class="card-job-bottom">Modalidad: Home Office</div>
		<div class="card-job-top--image"><img data-src="/logo.png"></div>
		<div class="card-job-description">Descripción breve</div>
	</div>
	<div class="card-job">
		<a href="/oferta/123">duplicado</a>
		<div class="card-job-top--info-heading">Otro cargo</div>
	</div>
	<div class="card-job">
		<a href="/oferta/999">sin título</a>
	</div>`
	cards := firstJobParseCards(cardsHTML, firstJobBaseURL)
	if len(cards) != 1 {
		t.Fatalf("parsed %d cards, want 1", len(cards))
	}
	card := cards[0]
	if card.ID != "123" || card.Title != "Dev Python" ||
		card.Company != "Acme" || card.Location != "Santiago (Remoto)" ||
		card.URL != "https://firstjob.me/oferta/123?from=list" ||
		card.CompanyLogoURL != "https://firstjob.me/logo.png" ||
		card.Description != "Descripción breve" || card.Source != FirstJobSource ||
		card.PublishedAt != "" || card.PublishedAtDatetime != nil {
		t.Fatalf("unexpected card: %+v", card)
	}

	emptyCompanyHTML := `<div class="card-job">
		<a href="/oferta/7">ir</a>
		<div class="card-job-top--info-heading">Analista</div>
		<div class="card-job-bottom">Remoto</div>
	</div>`
	remote := firstJobParseCards(emptyCompanyHTML, firstJobBaseURL)
	if len(remote) != 1 || remote[0].Company != "No especificada" ||
		remote[0].Location != "Remoto" {
		t.Fatalf("unexpected remote card: %+v", remote)
	}

	detailHTML := `<div class="job-single-header"><h3> Nuevo <span>título</span> </h3></div>
	<div class="job-meta">
		<a class="company">Nueva Empresa</a>
		<span class="location">Valparaíso</span>
	</div>
	<div class="content-single content-offer">
		<p>Primera línea</p><p>Segunda línea</p>
	</div>
	<div class="content-single content-offer"><p>Tercera línea</p></div>
	<div class="avatar-sidebar"><img src="/avatar.png"></div>`
	detailed := firstJobParseDetail(detailHTML, card)
	if detailed.Title != "Nuevo título" || detailed.Company != "Nueva Empresa" ||
		detailed.Location != "Valparaíso" ||
		detailed.Description != "Primera línea\nSegunda línea\n\nTercera línea" ||
		detailed.CompanyLogoURL != "https://firstjob.me/avatar.png" {
		t.Fatalf("unexpected detail: %+v", detailed)
	}
	if detailed.PublishedAt != "" || detailed.PublishedAtDatetime != nil {
		t.Error("FirstJob detail must not invent a publication date")
	}
	unchanged := firstJobParseDetail("<html></html>", card)
	if unchanged != card {
		t.Fatalf("empty detail changed the card: %+v", unchanged)
	}
}

func firstJobCardHTML(id int) string {
	return fmt.Sprintf(`<div class="card-job">
		<a href="/oferta/%d">ir</a>
		<div class="card-job-top--info-heading">Puesto %d</div>
		<div class="card-job-top--company">Empresa</div>
		<div class="card-job-top--location">Santiago</div>
	</div>`, id, id)
}

func TestFirstJobDefersDetailsBeyondCycleLimit(t *testing.T) {
	ctx := context.Background()
	var builder strings.Builder
	for id := 1; id <= firstJobMaxDetails+1; id++ {
		builder.WriteString(firstJobCardHTML(id))
	}
	fetcher := &firstJobStubFetcher{
		searchBodies: map[int]string{1: builder.String(), 2: "<html></html>"},
		detailBody:   "<html></html>",
	}
	cacheNow := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	scraper := newFirstJobFixtureScraper(fetcher, &cacheNow)

	got, err := scraper.FetchJobs(ctx, []string{"python"}, "Chile", 24, nil)
	if err != nil {
		t.Fatalf("fetch jobs: %v", err)
	}
	if len(got) != firstJobMaxDetails {
		t.Fatalf("got %d jobs, want %d", len(got), firstJobMaxDetails)
	}
	wantCalls := 2 + firstJobMaxDetails
	if calls := fetcher.Calls(); len(calls) != wantCalls {
		t.Fatalf("fetcher made %d calls, want %d", len(calls), wantCalls)
	}
	deferredCount := 0
	for _, state := range scraper.deferred {
		deferredCount += len(state.Order)
	}
	if deferredCount != 1 {
		t.Fatalf("deferred %d jobs, want 1", deferredCount)
	}
}

func TestFirstJobCachesAndDeferredPruning(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	cacheNow := now
	cardHTML := firstJobCardHTML(1)
	fetcher := &firstJobStubFetcher{
		searchBodies: map[int]string{1: cardHTML},
		detailBody:   `<div class="job-single-header"><h3>Detallado</h3></div>`,
	}
	scraper := newFirstJobFixtureScraper(fetcher, &cacheNow)

	got, err := scraper.FetchJobs(ctx, []string{"python"}, "Chile", 24, nil)
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if len(got) != 1 || got[0].Title != "Detallado" {
		t.Fatalf("first fetch returned %+v", got)
	}
	if calls := fetcher.Calls(); len(calls) != 2 {
		t.Fatalf("first fetch made %d calls, want 2", len(calls))
	}

	got, err = scraper.FetchJobs(ctx, []string{"python"}, "Chile", -1, nil)
	if err != nil {
		t.Fatalf("cached fetch: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("cached fetch returned %d jobs, want 1", len(got))
	}
	if calls := fetcher.Calls(); len(calls) != 0 {
		t.Fatalf("cached fetch made %d calls, want 0", len(calls))
	}

	cacheNow = cacheNow.Add(firstJobSearchTTL + time.Second)
	got, err = scraper.FetchJobs(ctx, []string{"python"}, "Chile", 24, nil)
	if err != nil {
		t.Fatalf("expired search cache fetch: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expired search cache fetch returned %d jobs, want 1", len(got))
	}
	if calls := fetcher.Calls(); len(calls) != 1 {
		t.Fatalf("expired search cache fetch made %d calls, want 1 detail-free search", len(calls))
	}

	cacheNow = cacheNow.Add(firstJobSearchTTL + time.Second)
	got, err = scraper.FetchJobs(
		ctx, []string{"python"}, "Chile", 24,
		map[string]struct{}{"1": {}},
	)
	if err != nil {
		t.Fatalf("known id fetch: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("known id fetch returned %d jobs, want 0", len(got))
	}
	if calls := fetcher.Calls(); len(calls) != 1 {
		t.Fatalf("known id fetch made %d calls, want 1 search", len(calls))
	}

	cacheNow = now
	scraper.storeDetail("detail", job.Job{ID: "detail", Title: "First"})
	if cached, ok := scraper.cachedDetail("detail"); !ok || cached.Job.ID != "detail" {
		t.Fatal("detail was not cached")
	}
	cacheNow = now.Add(firstJobDetailTTL + time.Second)
	if _, ok := scraper.cachedDetail("detail"); ok {
		t.Fatal("expired detail was returned")
	}

	cacheNow = now
	key := firstJobContext{TermsKey: "python", CountryCode: "CL", RegionID: "13"}
	scraper.storeDeferred(
		key, []string{"a", "b"},
		map[string]job.Job{"a": {ID: "a"}, "b": {ID: "b"}}, now,
	)
	order, items := scraper.deferredSnapshot(key, now, nil)
	if len(order) != 2 || len(items) != 2 {
		t.Fatalf("deferred snapshot = %v, %v", order, items)
	}
	order, _ = scraper.deferredSnapshot(
		key, now, map[string]struct{}{"a": {}},
	)
	if len(order) != 1 || order[0] != "b" {
		t.Fatalf("known deferred job was not skipped: %v", order)
	}
	cacheNow = now.Add(firstJobDeferredTTL + time.Second)
	scraper.pruneDeferred(cacheNow)
	if _, exists := scraper.deferred[key]; exists {
		t.Fatal("expired deferred context was kept")
	}

	for index := 0; index < firstJobDeferredMaxCtx+1; index++ {
		contextKey := firstJobContext{TermsKey: strconv.Itoa(index)}
		scraper.storeDeferred(
			contextKey, []string{"a"},
			map[string]job.Job{"a": {ID: "a"}}, now,
		)
	}
	scraper.pruneDeferred(now)
	if len(scraper.deferred) != firstJobDeferredMaxCtx {
		t.Fatalf("deferred contexts = %d, want %d", len(scraper.deferred), firstJobDeferredMaxCtx)
	}
}

func TestFirstJobHelpersAndCancellation(t *testing.T) {
	if got := firstJobAgeText(24); got != "24.0" {
		t.Errorf("firstJobAgeText(24) = %q, want %q", got, "24.0")
	}
	if got := firstJobAgeText(24.5); got != "24.5" {
		t.Errorf("firstJobAgeText(24.5) = %q, want %q", got, "24.5")
	}
	for index, value := range []float64{0, 0.5, 0.99} {
		if got := firstJobUserAgent(value); got != firstJobUserAgents[index] {
			t.Errorf("firstJobUserAgent(%v) = %q, want user agent %d", value, got, index)
		}
	}
	if got := firstJobDelay(400*time.Millisecond, time.Second, 0.5); got != 700*time.Millisecond {
		t.Errorf("firstJobDelay(0.5) = %s, want 700ms", got)
	}
	if got := firstJobDelay(400*time.Millisecond, time.Second, -1); got != 400*time.Millisecond {
		t.Errorf("firstJobDelay(-1) = %s, want 400ms", got)
	}
	if got := firstJobDelay(400*time.Millisecond, time.Second, 2); got != time.Second {
		t.Errorf("firstJobDelay(2) = %s, want 1s", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	scraper := NewFirstJobScraper(FirstJobOptions{})
	if _, err := scraper.FetchJobs(ctx, []string{"python"}, "Chile", 24, nil); err == nil {
		t.Error("canceled context must abort the fetch")
	}
}

func TestFirstJobDoesNotCacheAbortedSearch(t *testing.T) {
	ctx := context.Background()
	cacheNow := time.Date(2026, 11, 25, 0, 0, 0, 0, time.UTC)
	fetcher := &firstJobStubFetcher{
		searchBodies: map[int]string{1: "<html><body>CAPTCHA</body></html>"},
	}
	scraper := newFirstJobFixtureScraper(fetcher, &cacheNow)
	for attempt := 0; attempt < 2; attempt++ {
		got, err := scraper.FetchJobs(ctx, []string{"python"}, "Chile", 24, nil)
		if err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		if len(got) != 0 {
			t.Fatalf("attempt %d returned %d jobs, want 0", attempt, len(got))
		}
	}
	if calls := fetcher.TotalCalls(); calls != 2 {
		t.Fatalf("fetcher made %d calls, want one search per attempt", calls)
	}
}

func TestFirstJobProxyHostsAndRegistry(t *testing.T) {
	hosts := FirstJobProxyHosts()
	want := []string{"firstjob.me", "www.firstjob.me"}
	if len(hosts) != len(want) {
		t.Fatalf("got %d proxy hosts, want %d", len(hosts), len(want))
	}
	for index := range want {
		if hosts[index] != want[index] {
			t.Fatalf("proxy host %d = %q, want %q", index, hosts[index], want[index])
		}
	}
	scraper := DefaultRegistry().Get(FirstJobSlug)
	if scraper == nil || scraper.Source() != FirstJobSource {
		t.Fatalf("default registry FirstJob = %+v", scraper)
	}
}
