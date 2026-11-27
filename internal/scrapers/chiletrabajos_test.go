package scrapers

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/NikoMalek/job-sniper-discord-bot/internal/httpclient"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/job"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/locations"
)

type chileFixtureMeta struct {
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

func chileFixtureDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(fixtureRoot, "chiletrabajos")
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			t.Skip("chiletrabajos fixtures are not available")
		}
		t.Fatalf("stat %s: %v", dir, err)
	}
	return dir
}

func loadChileFixture(t *testing.T, search string) (chileFixtureMeta, []job.Job) {
	t.Helper()
	dir := filepath.Join(chileFixtureDir(t), search)

	rawMeta, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		t.Fatalf("read meta.json: %v", err)
	}
	var meta chileFixtureMeta
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

type switchingFetcher struct {
	mu      sync.Mutex
	current httpclient.Fetcher
}

func (f *switchingFetcher) set(current httpclient.Fetcher) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.current = current
}

func (f *switchingFetcher) Fetch(
	ctx context.Context, request httpclient.Request,
) (*httpclient.Response, error) {
	f.mu.Lock()
	current := f.current
	f.mu.Unlock()
	return current.Fetch(ctx, request)
}

func (f *switchingFetcher) BeginCycle() {
	f.mu.Lock()
	current := f.current
	f.mu.Unlock()
	current.BeginCycle()
}

func (f *switchingFetcher) CircuitOpen() bool {
	f.mu.Lock()
	current := f.current
	f.mu.Unlock()
	return current.CircuitOpen()
}

func chileNoSleep(context.Context, time.Duration) error { return nil }

func newChileFixtureScraper(
	fetcher httpclient.Fetcher, wallNow *time.Time, cacheNow *time.Time,
) *ChileTrabajosScraper {
	return NewChileTrabajosScraper(ChileTrabajosOptions{
		Fetcher:    fetcher,
		Clock:      func() time.Time { return *wallNow },
		CacheClock: func() time.Time { return *cacheNow },
		Sleep:      chileNoSleep,
		Random:     func() float64 { return 0.5 },
	})
}

func TestChileTrabajosReplaysFixtures(t *testing.T) {
	ctx := context.Background()
	searches := []string{"python-santiago", "backend-remoto", "sin-resultados"}
	switcher := &switchingFetcher{}
	wallNow := time.Time{}
	cacheNow := time.Unix(0, 0).UTC()
	scraper := newChileFixtureScraper(switcher, &wallNow, &cacheNow)

	for _, search := range searches {
		t.Run(search, func(t *testing.T) {
			meta, expected := loadChileFixture(t, search)
			wallNow = meta.CapturedAt
			fetcher, err := httpclient.NewReplayFetcher(
				ctx, fixtureRoot, "chiletrabajos", search,
			)
			if err != nil {
				t.Fatalf("new replay fetcher: %v", err)
			}
			switcher.set(fetcher)

			intent, err := locations.Parse(meta.Search.Location)
			if err != nil {
				t.Fatalf("parse fixture location: %v", err)
			}
			plan := scraper.Plan(&intent)
			if !plan.Supported || plan.SearchLocation != meta.SearchLocation {
				t.Fatalf("plan = %+v, want search location %q", plan, meta.SearchLocation)
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
				t.Fatalf("fixture job_count = %d, expected_jobs.json has %d", meta.JobCount, len(expected))
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

func TestChileTrabajosCachesCycle(t *testing.T) {
	ctx := context.Background()
	wallNow := time.Time{}
	cacheNow := time.Unix(0, 0).UTC()
	switcher := &switchingFetcher{}
	scraper := newChileFixtureScraper(switcher, &wallNow, &cacheNow)

	primeMeta, primeExpected := loadChileFixture(t, "python-santiago")
	wallNow = primeMeta.CapturedAt
	primeFetcher, err := httpclient.NewReplayFetcher(
		ctx, fixtureRoot, "chiletrabajos", "python-santiago",
	)
	if err != nil {
		t.Fatalf("new prime replay fetcher: %v", err)
	}
	switcher.set(primeFetcher)
	primeGot, err := scraper.FetchJobs(
		ctx, primeMeta.Search.Keywords, primeMeta.Search.Location,
		primeMeta.MaxAgeHours, nil,
	)
	if err != nil {
		t.Fatalf("prime fetch: %v", err)
	}
	requireSameJobs(t, primeGot, primeExpected)

	meta, expected := loadChileFixture(t, "backend-remoto")
	wallNow = meta.CapturedAt
	fetcher, err := httpclient.NewReplayFetcher(ctx, fixtureRoot, "chiletrabajos", "backend-remoto")
	if err != nil {
		t.Fatalf("new replay fetcher: %v", err)
	}
	switcher.set(fetcher)

	got, err := scraper.FetchJobs(
		ctx, meta.Search.Keywords, meta.Search.Location, meta.MaxAgeHours, nil,
	)
	if err != nil {
		t.Fatalf("first backend fetch: %v", err)
	}
	requireSameJobs(t, got, expected)
	if calls := fetcher.Calls(); len(calls) != meta.RequestCount {
		t.Fatalf("first backend fetch made %d calls, want %d", len(calls), meta.RequestCount)
	}

	got, err = scraper.FetchJobs(
		ctx, meta.Search.Keywords, meta.Search.Location, meta.MaxAgeHours, nil,
	)
	if err != nil {
		t.Fatalf("cached backend fetch: %v", err)
	}
	requireSameJobs(t, got, expected)
	if calls := fetcher.Calls(); len(calls) != 0 {
		t.Fatalf("cached backend fetch made %d calls", len(calls))
	}
}

func TestChileTrabajosPlansLocations(t *testing.T) {
	scraper := NewChileTrabajosScraper(ChileTrabajosOptions{})
	tests := []struct {
		location string
		want     string
		note     string
	}{
		{"Worldwide", "Chile", "solo puede buscar ofertas en Chile y no tiene filtro de modalidad."},
		{"Remoto", "Chile", "solo puede buscar ofertas en Chile y no tiene filtro de modalidad."},
		{"Remoto, Chile", "Chile", "no tiene filtro remoto; se buscan todas las ofertas de Chile."},
		{"Chile", "Chile", ""},
		{"Santiago, Chile", "Santiago", ""},
		{"Viña del Mar", "Valparaíso", "no tiene una opción para Viña del Mar; se usa Valparaíso como equivalente."},
		{"Atlantis, Chile", "Chile", "no tiene una opción para Atlantis; se buscará en todo Chile. Puedes acotar con `/add-location` en ciudades cercanas."},
	}
	for _, test := range tests {
		t.Run(test.location, func(t *testing.T) {
			intent, err := locations.Parse(test.location)
			if err != nil {
				t.Fatalf("parse %q: %v", test.location, err)
			}
			plan := scraper.Plan(&intent)
			if !plan.Supported {
				t.Fatalf("plan for %q is unsupported: %+v", test.location, plan)
			}
			if plan.SearchLocation != test.want {
				t.Fatalf("search location = %q, want %q", plan.SearchLocation, test.want)
			}
			if plan.Note != test.note {
				t.Fatalf("note = %q, want %q", plan.Note, test.note)
			}
		})
	}

	spain, err := locations.Parse("España")
	if err != nil {
		t.Fatalf("parse España: %v", err)
	}
	if plan := scraper.Plan(&spain); plan.Supported || plan.Reason == "" {
		t.Fatalf("Spain must be unsupported: %+v", plan)
	}
}

func TestChileFechaBucket(t *testing.T) {
	tests := []struct {
		hours float64
		want  int
	}{
		{24, 2}, {72, 3}, {144, 4}, {168, 5}, {168.1, 6}, {math.NaN(), 6},
	}
	for _, test := range tests {
		if got := chileFechaBucket(test.hours); got != test.want {
			t.Errorf("chileFechaBucket(%v) = %d, want %d", test.hours, got, test.want)
		}
	}
}

func TestChilePostedDateAndFormatting(t *testing.T) {
	naive, ok := chileParsePostedDate("2026-11-22 19:25:34")
	if !ok {
		t.Fatal("naive date was rejected")
	}
	if _, offset := naive.Zone(); offset != -4*60*60 {
		t.Fatalf("Chile offset = %d, want -14400", offset)
	}
	if got := chileFormatPublished(naive); got != "22 nov 2026, 19:25" {
		t.Fatalf("formatted date = %q", got)
	}
	if got := naive.UTC().Format(time.RFC3339); got != "2026-11-22T23:25:34Z" {
		t.Fatalf("UTC date = %q", got)
	}

	utc, ok := chileParsePostedDate("2026-11-22T23:25:34Z")
	if !ok || !utc.Equal(naive) {
		t.Fatalf("UTC date = %s, %v", utc, ok)
	}
	if _, ok := chileParsePostedDate("not a date"); ok {
		t.Fatal("invalid date was accepted")
	}
}

func TestChileCityMappingAndMatching(t *testing.T) {
	raw := `<select name="13">
		<option value="">--</option>
		<option value="1022" data-seo="santiago">Santiago</option>
		<option value="1014">Valparaíso</option>
		<option value="9999" data-seo="santiago-centro">Santiago Centro</option>
	</select>`
	mapping := chileParseCities(raw)
	if mapping.Values["santiago"] != "1022" || mapping.Values["valparaiso"] != "1014" {
		t.Fatalf("unexpected mapping: %+v", mapping.Values)
	}
	if mapping.Values["santiago centro"] != "9999" {
		t.Fatalf("data-seo mapping missing: %+v", mapping.Values)
	}
	tests := []struct {
		location string
		want     string
	}{
		{"Santiago, Chile", "1022"},
		{"Valparaíso", "1014"},
		{"Remoto", ""},
		{"Santiago Centro, Chile", "9999"},
		{"Todo Chile", ""},
		{"Atlantis, Chile", ""},
	}
	for _, test := range tests {
		if got := chileMatchLocation(test.location, mapping); got != test.want {
			t.Errorf("chileMatchLocation(%q) = %q, want %q", test.location, got, test.want)
		}
	}
}

func TestChileParseCardsAndDetail(t *testing.T) {
	raw := `<div class="job-item featured"><h2 class="title"><a href="/trabajo/old-1">Hidden</a></h2></div>
	<div class="job-item">
		<h2 class="title"><a href="https://www.chiletrabajos.cl/trabajo/dev-123">Dev &#8230;</a></h2>
		<h3 class="meta">Acme, <a href="https://www.chiletrabajos.cl/ciudad/santiago.html">Santiago</a></h3>
		<h3 class="meta"><i class="far fa-calendar"></i> 22 de Noviembre de 2026</h3>
		<p class="description">Texto <a class="ver-mas-btn">Ver más</a></p>
		<div class="thumb" style="background: url('/img/logo.png')"><span>A</span></div>
	</div>`
	cards := chileParseCards(raw)
	if len(cards) != 1 {
		t.Fatalf("parsed %d cards, want 1", len(cards))
	}
	card := cards[0]
	if card.ID != "123" || card.Title != "Dev …" || card.Company != "Acme" ||
		card.Location != "Santiago" || card.Description != "Texto" {
		t.Fatalf("unexpected card: %+v", card)
	}
	if card.CompanyLogoURL != chileBaseURL+"/img/logo.png" {
		t.Fatalf("logo = %q", card.CompanyLogoURL)
	}

	detailHTML := `<script type='application/ld+json'>
	{"@type":"JobPosting","datePosted":"2026-11-22 19:25:34",
	 "title":"Dev &amp; Ops","description":"Línea 1\r\nLínea 2",
	 "hiringOrganization":{"name":"Nueva Acme","logo":"/logo.png"},
	 "jobLocation":{"address":{"addressLocality":"Valparaíso"}},
	 "jobLocationType":"TELECOMMUTE"}
	</script>`
	detail := chileParseDetail(detailHTML)
	if detail == nil {
		t.Fatal("JobPosting detail was not parsed")
	}
	card.CompanyLogoURL = ""
	built, posted, ok := chileJobFromDetail(card, detail, "")
	if !ok {
		t.Fatal("detail job was rejected")
	}
	if built.Title != "Dev & Ops" || built.Company != "Nueva Acme" ||
		built.Location != "Valparaíso (Remoto)" || built.Description != "Línea 1\nLínea 2" {
		t.Fatalf("unexpected detail job: %+v", built)
	}
	if built.CompanyLogoURL != chileBaseURL+"/logo.png" {
		t.Fatalf("detail logo = %q", built.CompanyLogoURL)
	}
	if built.PublishedAt != "22 nov 2026, 19:25" || !posted.Equal(naiveDate(t)) {
		t.Fatalf("published = %q, %s", built.PublishedAt, posted)
	}
}

func naiveDate(t *testing.T) time.Time {
	t.Helper()
	posted, ok := chileParsePostedDate("2026-11-22 19:25:34")
	if !ok {
		t.Fatal("date helper failed")
	}
	return posted
}

func TestChileCachesAndDeferredPruning(t *testing.T) {
	now := time.Unix(0, 0).UTC()
	scraper := NewChileTrabajosScraper(ChileTrabajosOptions{
		Clock:      func() time.Time { return now },
		CacheClock: func() time.Time { return now },
	})
	posted, ok := chileParsePostedDate("2026-11-22 19:25:34")
	if !ok {
		t.Fatal("date helper failed")
	}
	first := job.Job{ID: "first", Title: "First"}
	scraper.storeDetail("first", first, posted)
	if cached, ok := scraper.cachedDetail("first"); !ok || cached.Job.ID != "first" {
		t.Fatal("detail was not cached")
	}
	now = now.Add(chileDetailTTL + time.Second)
	if _, ok := scraper.cachedDetail("first"); ok {
		t.Fatal("expired detail was returned")
	}

	key := chileContext{TermsKey: "python", LocationID: "1022", Fecha: 5}
	scraper.storeDeferred(
		key, []string{"a", "b"},
		map[string]job.Job{"a": {ID: "a"}, "b": {ID: "b"}}, now,
	)
	order, items := scraper.deferredSnapshot(key, now, nil)
	if len(order) != 2 || len(items) != 2 {
		t.Fatalf("deferred snapshot = %v, %v", order, items)
	}
	order, _ = scraper.deferredSnapshot(key, now, map[string]struct{}{"a": {}})
	if len(order) != 1 || order[0] != "b" {
		t.Fatalf("known deferred job was not skipped: %v", order)
	}
	now = now.Add(chileDeferredTTL + time.Second)
	scraper.pruneDeferred(now)
	if _, exists := scraper.deferred[key]; exists {
		t.Fatal("expired deferred context was kept")
	}
}
