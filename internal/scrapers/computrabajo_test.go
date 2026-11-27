package scrapers

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/NikoMalek/job-sniper-discord-bot/internal/httpclient"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/job"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/locations"
)

type computrabajoFixtureMeta struct {
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

func computrabajoFixtureDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(fixtureRoot, "computrabajo")
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			t.Skip("computrabajo fixtures are not available")
		}
		t.Fatalf("stat %s: %v", dir, err)
	}
	return dir
}

func loadComputrabajoFixture(
	t *testing.T, search string,
) (computrabajoFixtureMeta, []job.Job) {
	t.Helper()
	dir := filepath.Join(computrabajoFixtureDir(t), search)

	rawMeta, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		t.Fatalf("read meta.json: %v", err)
	}
	var meta computrabajoFixtureMeta
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

func computrabajoNoSleep(context.Context, time.Duration) error { return nil }

type computrabajoClockFetcher struct {
	base     httpclient.Fetcher
	pageTime map[string]time.Time
	wallNow  *time.Time
}

func (f *computrabajoClockFetcher) Fetch(
	ctx context.Context, request httpclient.Request,
) (*httpclient.Response, error) {
	if now, ok := f.pageTime[request.Label]; ok {
		*f.wallNow = now
	}
	return f.base.Fetch(ctx, request)
}

func (f *computrabajoClockFetcher) BeginCycle() { f.base.BeginCycle() }

func (f *computrabajoClockFetcher) CircuitOpen() bool { return f.base.CircuitOpen() }

func computrabajoRelativeOffset(text string) (time.Duration, bool) {
	normalized := locations.Normalize(text)
	switch {
	case normalized == "":
		return 0, false
	case strings.Contains(normalized, "minuto"):
		return time.Duration(computrabajoFirstNumber(normalized)) * time.Minute, true
	case strings.Contains(normalized, "hora"):
		return time.Duration(computrabajoFirstNumber(normalized)) * time.Hour, true
	case strings.Contains(normalized, "ayer"):
		return 24 * time.Hour, true
	case strings.Contains(normalized, "hoy"):
		return 0, true
	case strings.Contains(normalized, "dia"):
		if !computrabajoHasNumber(normalized) {
			return 0, false
		}
		return time.Duration(computrabajoFirstNumber(normalized)) * 24 * time.Hour, true
	default:
		return 0, false
	}
}

// computrabajoPageTimes recovers the wall clock used while each recorded
// search page was parsed. The fixture stores relative card dates, so the exact
// page time is the expected datetime plus the relative offset.
func computrabajoPageTimes(
	t *testing.T, search string, meta computrabajoFixtureMeta, expected []job.Job,
) map[string]time.Time {
	t.Helper()
	dir := filepath.Join(computrabajoFixtureDir(t), search)
	rawRequests, err := os.ReadFile(filepath.Join(dir, "requests.json"))
	if err != nil {
		t.Fatalf("read requests.json: %v", err)
	}
	var requests []httpclient.RecordedRequest
	if err := json.Unmarshal(rawRequests, &requests); err != nil {
		t.Fatalf("parse requests.json: %v", err)
	}
	expectedTimes := make(map[string]time.Time, len(expected))
	for _, record := range expected {
		if record.PublishedAtDatetime != nil {
			expectedTimes[record.ID] = *record.PublishedAtDatetime
		}
	}

	pageTimes := make(map[string]time.Time)
	for _, request := range requests {
		if !strings.HasPrefix(request.Label, "search ") {
			continue
		}
		rawHTML, err := os.ReadFile(filepath.Join(dir, request.File))
		if err != nil {
			t.Fatalf("read %s: %v", request.File, err)
		}
		cards := computrabajoParseCards(string(rawHTML), "https://cl.computrabajo.com", time.Time{})
		for _, card := range cards {
			offset, ok := computrabajoRelativeOffset(card.PublishedAt)
			if !ok {
				continue
			}
			expectedTime, exists := expectedTimes[card.ID]
			if !exists {
				continue
			}
			pageTimes[request.Label] = expectedTime.Add(offset)
			break
		}
		if _, ok := pageTimes[request.Label]; !ok {
			// Pages with no expected jobs only contain offers that are too old;
			// their exact parse time does not affect the recorded output.
			pageTimes[request.Label] = meta.CapturedAt
		}
	}
	return pageTimes
}

func newComputrabajoFixtureScraper(
	fetcher httpclient.Fetcher, wallNow *time.Time, cacheNow *time.Time,
) *ComputrabajoScraper {
	return NewComputrabajoScraper(ComputrabajoOptions{
		Fetcher:    fetcher,
		Clock:      func() time.Time { return *wallNow },
		CacheClock: func() time.Time { return *cacheNow },
		Sleep:      computrabajoNoSleep,
		Random:     func() float64 { return 0.5 },
	})
}

func TestComputrabajoReplaysFixtures(t *testing.T) {
	ctx := context.Background()
	searches := []string{"python-santiago", "backend-remoto", "sin-resultados"}
	switcher := &switchingFetcher{}
	wallNow := time.Time{}
	cacheNow := time.Unix(0, 0).UTC()
	scraper := newComputrabajoFixtureScraper(switcher, &wallNow, &cacheNow)

	for _, search := range searches {
		t.Run(search, func(t *testing.T) {
			meta, expected := loadComputrabajoFixture(t, search)
			wallNow = meta.CapturedAt
			fetcher, err := httpclient.NewReplayFetcher(
				ctx, fixtureRoot, "computrabajo", search,
			)
			if err != nil {
				t.Fatalf("new replay fetcher: %v", err)
			}
			switcher.set(&computrabajoClockFetcher{
				base:     fetcher,
				pageTime: computrabajoPageTimes(t, search, meta, expected),
				wallNow:  &wallNow,
			})

			intent, err := locations.Parse(meta.Search.Location)
			if err != nil {
				t.Fatalf("parse fixture location: %v", err)
			}
			plan := scraper.Plan(&intent)
			if !plan.Supported || plan.SearchLocation != meta.SearchLocation {
				t.Fatalf(
					"plan = %+v, want search location %q",
					plan, meta.SearchLocation,
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

func TestComputrabajoPlansAndResolvesLocations(t *testing.T) {
	scraper := NewComputrabajoScraper(ComputrabajoOptions{})
	tests := []struct {
		location string
		want     string
		note     string
	}{
		{
			"Worldwide", "Chile",
			"Computrabajo filtra por ubicación geográfica, no por modalidad: se buscará en Chile (`cl.computrabajo.com`).",
		},
		{
			"Remoto", "Chile",
			"Computrabajo filtra por ubicación geográfica, no por modalidad: se buscará en Chile (`cl.computrabajo.com`).",
		},
		{"Chile", "Chile", ""},
		{"Santiago, Chile", "Santiago Centro, R. Metropolitana, Chile", ""},
		{
			"Atlantis, Chile", "Chile",
			" No reconoció «Atlantis» entre las ciudades registradas de Chile; se buscará en todo el país.",
		},
		{
			"Guadalajara, México", "Guadalajara, Jalisco, México",
			"",
		},
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

	resolvedTests := []struct {
		location  string
		subdomain string
		slug      string
		matched   bool
	}{
		{"Santiago Centro, R. Metropolitana, Chile", "cl", "rmetropolitana-en-santiago-centro", true},
		{"Chile", "cl", "", true},
		{"México", "mx", "", true},
		{"Atlantis, Chile", "cl", "", false},
	}
	for _, test := range resolvedTests {
		t.Run("resolve "+test.location, func(t *testing.T) {
			resolved := scraper.resolveSearchLocation(test.location)
			if resolved.Country.Subdomain != test.subdomain ||
				resolved.Slug != test.slug || resolved.Matched != test.matched {
				t.Fatalf(
					"resolved = %+v, want subdomain=%q slug=%q matched=%v",
					resolved, test.subdomain, test.slug, test.matched,
				)
			}
		})
	}
}

func TestComputrabajoSlugAndSearchPath(t *testing.T) {
	tests := []struct {
		keyword string
		slug    string
	}{
		{"Python", "python"},
		{"  Full  Stack+Dev  ", "full-stack-dev"},
		{"C++", "c"},
		{"áéíóú", "aeiou"},
	}
	for _, test := range tests {
		if got := computrabajoSlug(test.keyword); got != test.slug {
			t.Errorf("computrabajoSlug(%q) = %q, want %q", test.keyword, got, test.slug)
		}
	}
	if got := computrabajoSearchPath("Backend", ""); got != "/trabajo-de-backend" {
		t.Fatalf("search path without slug = %q", got)
	}
	want := "/trabajo-de-backend-en-rmetropolitana-en-santiago-centro"
	if got := computrabajoSearchPath("Backend", "rmetropolitana-en-santiago-centro"); got != want {
		t.Fatalf("search path with slug = %q, want %q", got, want)
	}
}

func TestComputrabajoRelativeDates(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		text string
		want time.Time
	}{
		{"Hace  3  minutos", now.Add(-3 * time.Minute)},
		{"Hace 2 horas", now.Add(-2 * time.Hour)},
		{"Ayer", now.Add(-24 * time.Hour)},
		{"Hoy", now},
		{"Hace 4 días", now.Add(-4 * 24 * time.Hour)},
		{"22 de noviembre", time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC)},
		{"25 de diciembre", time.Date(2025, 12, 25, 0, 0, 0, 0, time.UTC)},
	}
	for _, test := range tests {
		t.Run(test.text, func(t *testing.T) {
			got := computrabajoRelativeDate(test.text, now)
			if got == nil {
				t.Fatal("relative date was not parsed")
			}
			if !got.Equal(test.want) {
				t.Fatalf("relative date = %s, want %s", got, test.want)
			}
		})
	}
	for _, text := range []string{"", "pronunciamiento", "hace días", "3 de nonsenso"} {
		if got := computrabajoRelativeDate(text, now); got != nil {
			t.Errorf("computrabajoRelativeDate(%q) = %s, want nil", text, got)
		}
	}
}

func TestComputrabajoBlockMarkersAndTotals(t *testing.T) {
	for _, text := range []string{
		"CAPTCHA", "Por favor, no soy un robot", "Verifica que eres humano",
		"Acceso denegado", "Request blocked", "Unusual traffic",
	} {
		if !computrabajoLooksBlocked(strings.Repeat("x", 3900) + text) {
			t.Errorf("%q inside first 4000 characters must be detected", text)
		}
	}
	if computrabajoLooksBlocked(strings.Repeat("x", 4000) + "captcha") {
		t.Error("marker after the first 4000 characters must be ignored")
	}
	if computrabajoLooksBlocked("ofertas recientes") {
		t.Error("ordinary search text must not be treated as a challenge")
	}

	tests := []struct {
		raw  string
		want int
		ok   bool
	}{
		{"Consulta las 1.234 ofertas", 1234, true},
		{"consulta las 20 ofertas", 20, true},
		{"sin resultados", 0, false},
	}
	for _, test := range tests {
		got, ok := computrabajoParseTotal(test.raw)
		if got != test.want || ok != test.ok {
			t.Errorf("computrabajoParseTotal(%q) = %d, %v; want %d, %v", test.raw, got, ok, test.want, test.ok)
		}
	}
}

func TestComputrabajoParsesCardsAndDetail(t *testing.T) {
	now := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	cardsHTML := `<article class="box_offer" data-id="ABC">
		<h2><a class="js-o-link" href="/job/abc#lc=1">Dev &amp; Ops</a></h2>
		<p class="dFlex"><a>Acme</a></p>
		<p class="fs16 fc_base mt5"><span>Santiago</span></p>
		<div class="fs13 mt15">Presencial y remoto</div>
		<p class="fs13 fc_aux mt15">Hace  3  días</p>
	</article>`
	cards := computrabajoParseCards(cardsHTML, "https://cl.computrabajo.com", now)
	if len(cards) != 1 {
		t.Fatalf("parsed %d cards, want 1", len(cards))
	}
	card := cards[0]
	if card.ID != "ABC" || card.Title != "Dev & Ops" || card.Company != "Acme" ||
		card.Location != "Santiago (Remoto)" ||
		card.URL != "https://cl.computrabajo.com/job/abc" ||
		card.PublishedAt != "Hace 3 días" || card.Source != ComputrabajoSource {
		t.Fatalf("unexpected card: %+v", card)
	}
	if card.PublishedAtDatetime == nil ||
		!card.PublishedAtDatetime.Equal(now.Add(-3*24*time.Hour)) {
		t.Fatalf("card date = %v", card.PublishedAtDatetime)
	}

	detailHTML := `<script type="application/ld+json">{"@graph":[
		{"@type":"Organization","name":"Portal"},
		{"@type":["Thing","JobPosting"],"title":"Dev &amp; Ops",
		 "description":"Línea 1&lt;br/&gt;Línea 2","datePosted":"2026-11-20",
		 "hiringOrganization":{"name":"Nueva Acme","logo":"/logo.png"},
		 "jobLocation":{"address":{"addressLocality":"Valparaíso","addressRegion":"Región"}},
		 "jobLocationType":"TELECOMMUTE"}
	]}</script>`
	detail := computrabajoParseDetail(detailHTML)
	if detail == nil {
		t.Fatal("JobPosting detail was not parsed")
	}
	card.Location = "No especificada"
	card.CompanyLogoURL = ""
	built, posted, ok := computrabajoJobFromDetail(
		card, detail, "https://cl.computrabajo.com",
	)
	if !ok {
		t.Fatal("detail job was rejected")
	}
	if built.Title != "Dev & Ops" || built.Company != "Nueva Acme" ||
		built.Location != "Valparaíso, Región (Remoto)" ||
		built.Description != "Línea 1\nLínea 2" {
		t.Fatalf("unexpected detail job: %+v", built)
	}
	if built.CompanyLogoURL != "https://cl.computrabajo.com/logo.png" {
		t.Fatalf("logo = %q", built.CompanyLogoURL)
	}
	if !posted.Equal(now.Add(-3 * 24 * time.Hour)) {
		t.Fatalf("posted = %s", posted)
	}

	fallbackHTML := `<main class="detail_fs"><div class="container">
		<p class="fs16"> Nueva Acme  -  Santiago </p></div></main>
		<div class="box_detail"><div class="mb40 pb40 bb1">
		<p>Texto visible</p><p>Más texto</p></div></div>`
	fallback := computrabajoApplyFallback(
		card, computrabajoFallbackDetail(fallbackHTML),
	)
	if fallback.Company != "Nueva Acme" || fallback.Location != "Santiago" ||
		fallback.Description != "Texto visible\nMás texto" {
		t.Fatalf("unexpected fallback job: %+v", fallback)
	}
}

func TestComputrabajoPostedDateFormats(t *testing.T) {
	tests := []struct {
		value string
		want  time.Time
	}{
		{"2026-11-19", time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC)},
		{"2026-11-19T18:30:00Z", time.Date(2026, 11, 19, 18, 30, 0, 0, time.UTC)},
		{"2026-11-19 18:30:00", time.Date(2026, 11, 19, 18, 30, 0, 0, time.UTC)},
		{"2026-11-19T18:30:00-03:00", time.Date(2026, 11, 19, 21, 30, 0, 0, time.UTC)},
	}
	for _, test := range tests {
		got, ok := computrabajoParsePostedDate(test.value)
		if !ok || !got.Equal(test.want) {
			t.Errorf("computrabajoParsePostedDate(%q) = %s, %v; want %s", test.value, got, ok, test.want)
		}
	}
	if _, ok := computrabajoParsePostedDate("not a date"); ok {
		t.Error("invalid date was accepted")
	}
}

func TestComputrabajoCachesAndDeferredPruning(t *testing.T) {
	now := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	scraper := NewComputrabajoScraper(ComputrabajoOptions{
		Clock:      func() time.Time { return now },
		CacheClock: func() time.Time { return now },
	})
	posted := now.Add(-24 * time.Hour)
	first := job.Job{ID: "first", Title: "First"}
	scraper.storeDetail("first", first, posted)
	if cached, ok := scraper.cachedDetail("first"); !ok || cached.Job.ID != "first" {
		t.Fatal("detail was not cached")
	}
	now = now.Add(computrabajoDetailTTL + time.Second)
	if _, ok := scraper.cachedDetail("first"); ok {
		t.Fatal("expired detail was returned")
	}

	key := computrabajoContext{TermsKey: "python", Subdomain: "cl", Slug: "centro"}
	scraper.storeDeferred(
		key, []string{"a", "b"},
		map[string]job.Job{"a": {ID: "a"}, "b": {ID: "b"}}, now,
	)
	order, items := scraper.deferredSnapshot(
		key, now, now.Add(-48*time.Hour), nil,
	)
	if len(order) != 2 || len(items) != 2 {
		t.Fatalf("deferred snapshot = %v, %v", order, items)
	}
	order, _ = scraper.deferredSnapshot(
		key, now, now.Add(-48*time.Hour), map[string]struct{}{"a": {}},
	)
	if len(order) != 1 || order[0] != "b" {
		t.Fatalf("known deferred job was not skipped: %v", order)
	}
	now = now.Add(computrabajoDeferredTTL + time.Second)
	scraper.pruneDeferred(now)
	if _, exists := scraper.deferred[key]; exists {
		t.Fatal("expired deferred context was kept")
	}
}

func TestComputrabajoProxyHostsAndRegistry(t *testing.T) {
	hosts := ComputrabajoProxyHosts()
	want := []string{
		"ar.computrabajo.com", "bo.computrabajo.com", "cl.computrabajo.com",
		"co.computrabajo.com", "cr.computrabajo.com", "do.computrabajo.com",
		"ec.computrabajo.com", "gt.computrabajo.com", "hn.computrabajo.com",
		"mx.computrabajo.com", "ni.computrabajo.com", "pa.computrabajo.com",
		"pe.computrabajo.com", "py.computrabajo.com", "sv.computrabajo.com",
		"uy.computrabajo.com", "ve.computrabajo.com",
	}
	if len(hosts) != len(want) {
		t.Fatalf("got %d proxy hosts, want %d", len(hosts), len(want))
	}
	for index := range want {
		if hosts[index] != want[index] {
			t.Fatalf("proxy host %d = %q, want %q", index, hosts[index], want[index])
		}
	}

	scraper := DefaultRegistry().Get(ComputrabajoSlug)
	if scraper == nil || scraper.Source() != ComputrabajoSource {
		t.Fatalf("default registry Computrabajo = %+v", scraper)
	}
}
