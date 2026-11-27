package scrapers

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
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
	"github.com/PuerkitoBio/goquery"
)

type linkedInFixtureMeta struct {
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

// linkedInClockDrift accounts for the capture script writing captured_at just
// before the first search page was parsed. Relative dates were produced a few
// seconds later by the Python scraper.
const linkedInClockDrift = 60 * time.Second

func linkedInFixtureDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(fixtureRoot, "linkedin")
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			t.Skip("linkedin fixtures are not available")
		}
		t.Fatalf("stat %s: %v", dir, err)
	}
	return dir
}

func loadLinkedInFixture(
	t *testing.T, search string,
) (linkedInFixtureMeta, []job.Job) {
	t.Helper()
	dir := filepath.Join(linkedInFixtureDir(t), search)

	rawMeta, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		t.Fatalf("read meta.json: %v", err)
	}
	var meta linkedInFixtureMeta
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

// linkedInFixtureFetcher adapts the recorded search params to the replay
// fetcher. The capture script stored a reference to the mutable params dict,
// so every recorded LinkedIn search page shows the final start=90 value.
type linkedInFixtureFetcher struct {
	replay *httpclient.ReplayFetcher

	mu    sync.Mutex
	calls []httpclient.Request
}

func (f *linkedInFixtureFetcher) Fetch(
	ctx context.Context, request httpclient.Request,
) (*httpclient.Response, error) {
	f.mu.Lock()
	f.calls = append(f.calls, cloneLinkedInRequest(request))
	f.mu.Unlock()

	forwarded := cloneLinkedInRequest(request)
	if strings.HasPrefix(request.Label, "search page") {
		if forwarded.Params == nil {
			forwarded.Params = map[string]string{}
		}
		forwarded.Params["start"] = "90"
	}
	return f.replay.Fetch(ctx, forwarded)
}

func (f *linkedInFixtureFetcher) BeginCycle() {
	f.replay.BeginCycle()
	f.mu.Lock()
	f.calls = nil
	f.mu.Unlock()
}

func (f *linkedInFixtureFetcher) CircuitOpen() bool { return f.replay.CircuitOpen() }

func (f *linkedInFixtureFetcher) Calls() []httpclient.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]httpclient.Request, len(f.calls))
	for index, call := range f.calls {
		result[index] = cloneLinkedInRequest(call)
	}
	return result
}

func goqueryDocument(raw string) (*goquery.Selection, error) {
	document, err := goquery.NewDocumentFromReader(strings.NewReader(raw))
	if err != nil {
		return nil, err
	}
	return document.Selection, nil
}

func cloneLinkedInRequest(request httpclient.Request) httpclient.Request {
	clone := request
	clone.Params = make(map[string]string, len(request.Params))
	for name, value := range request.Params {
		clone.Params[name] = value
	}
	clone.Headers = make(map[string]string, len(request.Headers))
	for name, value := range request.Headers {
		clone.Headers[name] = value
	}
	return clone
}

func linkedInNoSleep(context.Context, time.Duration) error { return nil }

type linkedInSwitchingFetcher struct {
	mu      sync.Mutex
	current httpclient.Fetcher
}

func (f *linkedInSwitchingFetcher) set(current httpclient.Fetcher) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.current = current
}

func (f *linkedInSwitchingFetcher) currentFetcher() httpclient.Fetcher {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.current
}

func (f *linkedInSwitchingFetcher) Fetch(
	ctx context.Context, request httpclient.Request,
) (*httpclient.Response, error) {
	return f.currentFetcher().Fetch(ctx, request)
}

func (f *linkedInSwitchingFetcher) BeginCycle() { f.currentFetcher().BeginCycle() }

func (f *linkedInSwitchingFetcher) CircuitOpen() bool {
	return f.currentFetcher().CircuitOpen()
}

func requireSameLinkedInJobs(t *testing.T, got, want []job.Job) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d jobs, want %d", len(got), len(want))
	}
	for index := range want {
		gotJob, wantJob := got[index], want[index]
		fields := []struct {
			name string
			got  string
			want string
		}{
			{"id", gotJob.ID, wantJob.ID},
			{"title", gotJob.Title, wantJob.Title},
			{"company", gotJob.Company, wantJob.Company},
			{"location", gotJob.Location, wantJob.Location},
			{"url", gotJob.URL, wantJob.URL},
			{"source", gotJob.Source, wantJob.Source},
			{"published_at", gotJob.PublishedAt, wantJob.PublishedAt},
			{"company_logo_url", gotJob.CompanyLogoURL, wantJob.CompanyLogoURL},
			{"description", gotJob.Description, wantJob.Description},
		}
		for _, field := range fields {
			if field.got != field.want {
				t.Errorf(
					"job %d %s = %q, want %q",
					index, field.name, field.got, field.want,
				)
			}
		}
		gotTime, wantTime := gotJob.PublishedAtDatetime, wantJob.PublishedAtDatetime
		if (gotTime == nil) != (wantTime == nil) {
			t.Errorf("job %d published_at_datetime presence differs", index)
			continue
		}
		delta := time.Duration(0)
		if gotTime != nil && wantTime != nil {
			delta = gotTime.Sub(*wantTime)
		}
		if gotTime != nil && (delta < -linkedInClockDrift || delta > linkedInClockDrift) {
			t.Errorf(
				"job %d published_at_datetime = %s, want %s (±%s)",
				index, gotTime, wantTime, linkedInClockDrift,
			)
		}
	}
}

func TestLinkedInReplaysFixtures(t *testing.T) {
	ctx := context.Background()
	searches := []string{"python-santiago", "backend-remoto", "sin-resultados"}
	switcher := &linkedInSwitchingFetcher{}
	now := time.Time{}
	scraper := NewLinkedInScraper(LinkedInOptions{
		Fetcher:    switcher,
		Clock:      func() time.Time { return now },
		CacheClock: func() time.Time { return now },
		Sleep:      linkedInNoSleep,
		Random:     func() float64 { return 0.5 },
	})

	// The capture script used one scraper for every search, so descriptions
	// cached by an earlier search are reused by later searches.
	for _, search := range searches {
		t.Run(search, func(t *testing.T) {
			meta, expected := loadLinkedInFixture(t, search)
			replay, err := httpclient.NewReplayFetcher(
				ctx, fixtureRoot, "linkedin", search,
			)
			if err != nil {
				t.Fatalf("new replay fetcher: %v", err)
			}
			fetcher := &linkedInFixtureFetcher{replay: replay}
			switcher.set(fetcher)
			now = meta.CapturedAt
			scraper.BeginCycle()

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
				ctx, meta.Search.Keywords, meta.SearchLocation,
				meta.MaxAgeHours, nil,
			)
			if err != nil {
				t.Fatalf("fetch jobs: %v", err)
			}
			requireSameLinkedInJobs(t, got, expected)
			if meta.JobCount != len(expected) {
				t.Fatalf(
					"fixture job_count = %d, expected_jobs.json has %d",
					meta.JobCount, len(expected),
				)
			}
			calls := fetcher.Calls()
			if len(calls) != meta.RequestCount {
				t.Fatalf("fetcher made %d calls, want %d", len(calls), meta.RequestCount)
			}
			if remaining := fetcher.replay.Remaining(); remaining != 0 {
				t.Fatalf("%d recorded requests were not replayed", remaining)
			}
			for index := 0; index < linkedInMaxSearchPages; index++ {
				call := calls[index]
				if call.Label != fmt.Sprintf("search page %d", index+1) {
					t.Fatalf("call %d label = %q", index, call.Label)
				}
				wantStart := strconv.Itoa(index * linkedInSearchPageSize)
				if call.Params["start"] != wantStart {
					t.Errorf(
						"search page %d start = %q, want %q",
						index+1, call.Params["start"], wantStart,
					)
				}
			}
		})
	}
}

func TestLinkedInPlansLocations(t *testing.T) {
	scraper := NewLinkedInScraper(LinkedInOptions{})
	tests := []struct {
		name     string
		intent   locations.LocationIntent
		location string
		note     string
	}{
		{
			"worldwide",
			locations.LocationIntent{Kind: "worldwide"},
			"Worldwide",
			"sin filtro de modalidad: incluye ofertas remotas y presenciales de todo el mundo.",
		},
		{
			"remote without country",
			locations.LocationIntent{Kind: "remote"},
			"Remote",
			"solo ofertas remotas de todo el mundo.",
		},
		{
			"remote with country",
			locations.LocationIntent{Kind: "remote", CountryCode: "CL"},
			"Chile",
			"LinkedIn no permite filtrar solo remoto; se buscará todo el país.",
		},
		{
			"country",
			locations.LocationIntent{Kind: "country", CountryCode: "ES"},
			"Spain",
			"",
		},
		{
			"city without country",
			locations.LocationIntent{Kind: "city", City: "Santiago"},
			"Santiago",
			"",
		},
		{
			"city ending in country",
			locations.LocationIntent{Kind: "city", City: "Santiago de Chile", CountryCode: "CL"},
			"Santiago de Chile",
			"",
		},
		{
			"city with country",
			locations.LocationIntent{Kind: "city", City: "Santiago", CountryCode: "CL"},
			"Santiago, Chile",
			"",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := scraper.Plan(&test.intent)
			if !plan.Supported {
				t.Fatalf("plan is unsupported: %+v", plan)
			}
			if plan.SearchLocation != test.location || plan.Note != test.note {
				t.Fatalf(
					"plan = %+v, want location %q note %q",
					plan, test.location, test.note,
				)
			}
		})
	}

	if scraper.Slug() != LinkedInSlug || scraper.Source() != LinkedInSource {
		t.Fatalf("unexpected identity: %s / %s", scraper.Slug(), scraper.Source())
	}
	if len(scraper.CoveredCountries()) != 0 {
		t.Error("LinkedIn must be a global portal")
	}
	spain, err := locations.Parse("España")
	if err != nil {
		t.Fatalf("parse España: %v", err)
	}
	if !scraper.Covers(&spain) {
		t.Error("LinkedIn must cover every country")
	}
}

func TestLinkedInSearchParametersAndCacheKey(t *testing.T) {
	seconds, err := linkedInAgeSeconds(168)
	if err != nil || seconds != 604800 {
		t.Fatalf("linkedInAgeSeconds(168) = %d, %v", seconds, err)
	}
	if seconds, err = linkedInAgeSeconds(0.5); err != nil || seconds != 1800 {
		t.Fatalf("linkedInAgeSeconds(0.5) = %d, %v", seconds, err)
	}
	if _, err = linkedInAgeSeconds(math.NaN()); err == nil {
		t.Error("NaN max age must be rejected")
	}

	terms := linkedInKeywords([]string{" python ", "", "Backend", "python"})
	wantTerms := []string{"python", "Backend", "python"}
	if len(terms) != len(wantTerms) {
		t.Fatalf("terms = %v, want %v", terms, wantTerms)
	}
	for index := range wantTerms {
		if terms[index] != wantTerms[index] {
			t.Errorf("terms[%d] = %q, want %q", index, terms[index], wantTerms[index])
		}
	}
	if got := linkedInTermsKey(terms); got != "Backend\x00python\x00python" {
		t.Errorf("terms key = %q", got)
	}
	for index, value := range []float64{0, 0.5, 0.99} {
		if got := linkedInUserAgent(value); got != linkedInUserAgents[index] {
			t.Errorf("user agent %d = %q", index, got)
		}
	}
	if got := linkedInDelay(time.Second, 2*time.Second, 0.5); got != 1500*time.Millisecond {
		t.Errorf("delay = %s, want 1.5s", got)
	}
	if got := linkedInDelay(time.Second, 2*time.Second, -1); got != time.Second {
		t.Errorf("clamped low delay = %s, want 1s", got)
	}
	if got := linkedInDelay(time.Second, 2*time.Second, 2); got != 2*time.Second {
		t.Errorf("clamped high delay = %s, want 2s", got)
	}
}

func TestLinkedInCleansURLsAndLogos(t *testing.T) {
	raw := "https://cl.linkedin.com/jobs/view/software-engineer-python-%2B-kubernetes-remote-work-ref%23295748-at-bairesdev-4492959881?position=1&pageNum=0&refId=abc&trackingId=def#section"
	want := "https://cl.linkedin.com/jobs/view/software-engineer-python-%2B-kubernetes-remote-work-ref%23295748-at-bairesdev-4492959881?position=1&pageNum=0"
	if got := linkedInCleanURL(raw); got != want {
		t.Fatalf("cleaned URL = %q, want %q", got, want)
	}
	if got := linkedInJobID(want); got != "software-engineer-python-%2B-kubernetes-remote-work-ref%23295748-at-bairesdev-4492959881" {
		t.Errorf("job id = %q", got)
	}
	if !linkedInIsTrackingKey("TrackingThing") ||
		!linkedInIsTrackingKey("trk=foo") ||
		!linkedInIsTrackingKey("REFID=1") {
		t.Error("tracking key filter is incorrect")
	}

	tests := []struct {
		html string
		want string
	}{
		{`<img src="/logo.png">`, "https://www.linkedin.com/logo.png"},
		{`<img data-src="/data.png" srcset="/first.png 1x, /second.png 2x">`, "https://www.linkedin.com/data.png"},
		{`<img data-delayed-url="/delayed.png">`, "https://www.linkedin.com/delayed.png"},
		{`<img data-ghost-url="/ghost.png">`, "https://www.linkedin.com/ghost.png"},
		{`<img srcset="/one.png 1x, /two.png 2x">`, "https://www.linkedin.com/one.png"},
		{`<img src="ftp://example.com/logo.png">`, ""},
		{`<div></div>`, ""},
	}
	for _, test := range tests {
		t.Run(test.want, func(t *testing.T) {
			document, err := goqueryDocument(test.html)
			if err != nil {
				t.Fatalf("parse logo HTML: %v", err)
			}
			if got := linkedInLogoURL(document.Find("img").First()); got != test.want {
				t.Errorf("logo URL = %q, want %q", got, test.want)
			}
		})
	}
}

func TestLinkedInParsesCards(t *testing.T) {
	now := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	scraper := NewLinkedInScraper(LinkedInOptions{Clock: func() time.Time { return now }})
	raw := `<li>
		<a class="base-card__full-link" href="https://cl.linkedin.com/jobs/view/abc-123?position=1&amp;refId=x">ir</a>
		<img data-delayed-url="/logo.png">
		<h3 class="base-search-card__title"> Python <span>Developer</span> </h3>
		<h4 class="base-search-card__subtitle">Acme</h4>
		<span class="job-search-card__location">Santiago, Chile</span>
		<time>2 days ago</time>
		<div class="base-search-card__snippet">Breve descripción</div>
	</li>
	<li>
		<a href="https://www.linkedin.com/jobs/view/abc-123?trackingId=y">duplicado</a>
	</li>
	<li>
		<a href="https://www.linkedin.com/jobs/view/sin-titulo">sin título</a>
	</li>
	<li>
		<a href="https://www.linkedin.com/jobs/view/fallback-456">otro</a>
		<h3>Fallback title</h3>
		<h4>Fallback company</h4>
		<span>Fallback location</span>
	</li>`

	cards := scraper.parseJobs(raw)
	if len(cards) != 2 {
		t.Fatalf("parsed %d cards, want 2: %+v", len(cards), cards)
	}
	first := cards[0]
	if first.ID != "abc-123" || first.Title != "Python Developer" ||
		first.Company != "Acme" || first.Location != "Santiago, Chile" ||
		first.URL != "https://cl.linkedin.com/jobs/view/abc-123?position=1" ||
		first.PublishedAt != "2 days ago" ||
		first.CompanyLogoURL != "https://www.linkedin.com/logo.png" ||
		first.Description != "Breve descripción" || first.Source != LinkedInSource {
		t.Fatalf("unexpected first card: %+v", first)
	}
	if first.PublishedAtDatetime == nil ||
		!first.PublishedAtDatetime.Equal(now.AddDate(0, 0, -2)) {
		t.Errorf("unexpected published datetime: %v", first.PublishedAtDatetime)
	}
	second := cards[1]
	if second.ID != "fallback-456" || second.Title != "Fallback title" ||
		second.Company != "Fallback company" || second.Location != "Fallback location" {
		t.Fatalf("unexpected fallback card: %+v", second)
	}
}

func TestLinkedInRelativeDates(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 30, 0, 0, time.UTC)
	scraper := NewLinkedInScraper(LinkedInOptions{Clock: func() time.Time { return now }})
	tests := []struct {
		value string
		want  time.Time
	}{
		{"just now", now},
		{"AHORA", now},
		{"Hace unos segundos", now},
		{"11 hours ago", now.Add(-11 * time.Hour)},
		{"3+ weeks ago", now.AddDate(0, 0, -21)},
		{"2 months ago", now.AddDate(0, 0, -60)},
		{"1 año ago", now.AddDate(0, 0, -365)},
		{"15 minutos", now.Add(-15 * time.Minute)},
	}
	for _, test := range tests {
		t.Run(test.value, func(t *testing.T) {
			got := scraper.publishedDatetime(test.value)
			if got == nil || !got.Equal(test.want) {
				t.Errorf("publishedDatetime(%q) = %v, want %v", test.value, got, test.want)
			}
		})
	}
	for _, value := range []string{"", "recently", "mañana"} {
		if got := scraper.publishedDatetime(value); got != nil {
			t.Errorf("publishedDatetime(%q) = %v, want nil", value, got)
		}
	}
}

func TestLinkedInParsesDescriptions(t *testing.T) {
	tests := []struct {
		name string
		html string
		want string
	}{
		{
			"markup",
			`<div class="show-more-less-html__markup"><p> First </p><p>Second</p></div>`,
			"First Second",
		},
		{
			"description class",
			`<section class="jobs-description__content"><span>Detail</span></section>`,
			"Detail",
		},
		{
			"generic class",
			`<div class="something-description"><p>Generic</p></div>`,
			"Generic",
		},
		{
			"meta fallback",
			`<html><head><meta name="description" content=" Meta text "></head></html>`,
			"Meta text",
		},
		{"empty", `<html></html>`, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := linkedInParseDescription(test.html); got != test.want {
				t.Errorf("description = %q, want %q", got, test.want)
			}
		})
	}
}

type linkedInDetail struct {
	body     string
	status   int
	finalURL string
}

type linkedInStubFetcher struct {
	mu      sync.Mutex
	pages   map[int]string
	details map[string]linkedInDetail
	calls   []httpclient.Request
	circuit bool
}

func (f *linkedInStubFetcher) Fetch(
	ctx context.Context, request httpclient.Request,
) (*httpclient.Response, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, cloneLinkedInRequest(request))

	if strings.HasPrefix(request.Label, "search page") {
		start, _ := strconv.Atoi(request.Params["start"])
		body := f.pages[start/linkedInSearchPageSize+1]
		return &httpclient.Response{StatusCode: 200, Body: []byte(body)}, nil
	}
	id := strings.TrimPrefix(request.Label, "job ")
	detail := f.details[id]
	status := detail.status
	if status == 0 {
		status = 200
	}
	return &httpclient.Response{
		StatusCode: status,
		Body:       []byte(detail.body),
		FinalURL:   detail.finalURL,
	}, nil
}

func (f *linkedInStubFetcher) BeginCycle() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}

func (f *linkedInStubFetcher) CircuitOpen() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.circuit
}

func (f *linkedInStubFetcher) SetCircuit(open bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.circuit = open
}

func (f *linkedInStubFetcher) Calls() []httpclient.Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	result := make([]httpclient.Request, len(f.calls))
	for index, call := range f.calls {
		result[index] = cloneLinkedInRequest(call)
	}
	return result
}

func linkedInCardHTML(id string) string {
	return fmt.Sprintf(`<li>
		<a href="https://www.linkedin.com/jobs/view/%s">ir</a>
		<h3>Job %s</h3>
		<h4>Acme</h4>
		<span>Santiago</span>
	</li>`, id, id)
}

func linkedInScraperWithStub(now *time.Time) (*LinkedInScraper, *linkedInStubFetcher) {
	fetcher := &linkedInStubFetcher{
		pages:   map[int]string{},
		details: map[string]linkedInDetail{},
	}
	scraper := NewLinkedInScraper(LinkedInOptions{
		Fetcher:    fetcher,
		Clock:      func() time.Time { return *now },
		CacheClock: func() time.Time { return *now },
		Sleep:      linkedInNoSleep,
		Random:     func() float64 { return 0.5 },
	})
	return scraper, fetcher
}

func TestLinkedInCachesSearchAndDescriptions(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	scraper, fetcher := linkedInScraperWithStub(&now)
	fetcher.pages[1] = linkedInCardHTML("one")
	fetcher.details["one"] = linkedInDetail{
		body: `<div class="show-more-less-html__markup">Detallado</div>`,
	}

	got, err := scraper.FetchJobs(ctx, []string{"python"}, "Chile", 24, nil)
	if err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if len(got) != 1 || got[0].Description != "Detallado" {
		t.Fatalf("first fetch returned %+v", got)
	}
	if calls := fetcher.Calls(); len(calls) != 3 {
		t.Fatalf("first fetch made %d calls, want two searches and one detail", len(calls))
	}

	got, err = scraper.FetchJobs(ctx, []string{"python"}, "Chile", 24, nil)
	if err != nil {
		t.Fatalf("cached fetch: %v", err)
	}
	if len(got) != 1 || got[0].Description != "Detallado" {
		t.Fatalf("cached fetch returned %+v", got)
	}
	if calls := fetcher.Calls(); len(calls) != 0 {
		t.Fatalf("cached fetch made %d calls, want 0", len(calls))
	}

	scraper.BeginCycle()
	got, err = scraper.FetchJobs(ctx, []string{"python"}, "Chile", 24, nil)
	if err != nil {
		t.Fatalf("new cycle fetch: %v", err)
	}
	if len(got) != 1 || got[0].Description != "Detallado" {
		t.Fatalf("new cycle fetch returned %+v", got)
	}
	if calls := fetcher.Calls(); len(calls) != 2 {
		t.Fatalf("new cycle fetch made %d calls, want two searches", len(calls))
	}
	if source := scraper.lastSearchSource(); source != "live" {
		t.Fatalf("search source after BeginCycle = %q, want live", source)
	}
}

func TestLinkedInDefersFailuresAndSkipsKnownIDs(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	scraper, fetcher := linkedInScraperWithStub(&now)
	fetcher.pages[1] = linkedInCardHTML("one") + linkedInCardHTML("two") +
		linkedInCardHTML("three")
	fetcher.details["one"] = linkedInDetail{body: `<meta name="description" content="OK">`}
	fetcher.details["two"] = linkedInDetail{status: 500}
	fetcher.details["three"] = linkedInDetail{finalURL: "https://www.linkedin.com/authwall"}

	got, err := scraper.FetchJobs(
		ctx, []string{"python"}, "Chile", 24,
		map[string]struct{}{"one": {}},
	)
	if err != nil {
		t.Fatalf("fetch jobs: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("known job must be skipped, got %+v", got)
	}
	if calls := fetcher.Calls(); len(calls) != 4 {
		t.Fatalf("fetch made %d calls, want two searches and two failed details", len(calls))
	}

	got, err = scraper.FetchJobs(ctx, []string{"python"}, "Chile", 24, nil)
	if err != nil {
		t.Fatalf("second fetch: %v", err)
	}
	if len(got) != 1 || got[0].ID != "one" {
		t.Fatalf("second fetch returned %+v", got)
	}
	if calls := fetcher.Calls(); len(calls) != 3 {
		t.Fatalf("second fetch made %d calls, want one cached and two failed details", len(calls))
	}
}

func TestLinkedInEmptyDescriptionIsCachedAndReturned(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	scraper, fetcher := linkedInScraperWithStub(&now)
	fetcher.pages[1] = linkedInCardHTML("empty")
	fetcher.details["empty"] = linkedInDetail{body: `<html></html>`}

	got, err := scraper.FetchJobs(ctx, []string{"python"}, "Chile", 24, nil)
	if err != nil || len(got) != 1 || got[0].Description != "" {
		t.Fatalf("first fetch = %+v, %v", got, err)
	}
	scraper.BeginCycle()
	got, err = scraper.FetchJobs(ctx, []string{"python"}, "Chile", 24, nil)
	if err != nil || len(got) != 1 {
		t.Fatalf("second fetch = %+v, %v", got, err)
	}
	if calls := fetcher.Calls(); len(calls) != 2 {
		t.Fatalf("second fetch made %d calls, want two searches", len(calls))
	}
}

func TestLinkedInDoesNotCacheAbortedSearch(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	scraper, fetcher := linkedInScraperWithStub(&now)
	failingFetcher := &linkedInStatusFetcher{stub: fetcher, status: 500}
	scraper.fetcher = failingFetcher

	for attempt := 0; attempt < 2; attempt++ {
		got, err := scraper.FetchJobs(ctx, []string{"python"}, "Chile", 24, nil)
		if err != nil {
			t.Fatalf("attempt %d: %v", attempt, err)
		}
		if len(got) != 0 {
			t.Fatalf("attempt %d returned %d jobs, want 0", attempt, len(got))
		}
	}
	if failingFetcher.calls != 2 {
		t.Fatalf("fetcher made %d search calls, want one per attempt", failingFetcher.calls)
	}
}

type linkedInStatusFetcher struct {
	stub   *linkedInStubFetcher
	status int
	calls  int
}

func (f *linkedInStatusFetcher) Fetch(
	ctx context.Context, request httpclient.Request,
) (*httpclient.Response, error) {
	if strings.HasPrefix(request.Label, "search page") {
		f.calls++
		return &httpclient.Response{StatusCode: f.status}, nil
	}
	return f.stub.Fetch(ctx, request)
}

func (f *linkedInStatusFetcher) BeginCycle()       { f.stub.BeginCycle() }
func (f *linkedInStatusFetcher) CircuitOpen() bool { return f.stub.CircuitOpen() }

func TestLinkedInDescriptionCacheEviction(t *testing.T) {
	now := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	cacheNow := now
	scraper := NewLinkedInScraper(LinkedInOptions{
		CacheClock: func() time.Time { return cacheNow },
	})

	scraper.storeDescription("first", "one")
	if description, ok := scraper.cachedDescription("first"); !ok || description != "one" {
		t.Fatal("description was not cached")
	}
	cacheNow = cacheNow.Add(linkedInDescriptionTTL + time.Second)
	if _, ok := scraper.cachedDescription("first"); ok {
		t.Fatal("expired description was returned")
	}

	cacheNow = now
	for index := 0; index < linkedInDescriptionLimit+1; index++ {
		scraper.storeDescription(strconv.Itoa(index), "value")
	}
	if len(scraper.description) != linkedInDescriptionLimit {
		t.Fatalf("cache size = %d, want %d", len(scraper.description), linkedInDescriptionLimit)
	}
	if _, ok := scraper.cachedDescription("0"); ok {
		t.Error("oldest description was not evicted")
	}
	if _, ok := scraper.cachedDescription(strconv.Itoa(linkedInDescriptionLimit)); !ok {
		t.Error("newest description was evicted")
	}
}

func TestLinkedInCircuitAndCancellation(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	scraper, fetcher := linkedInScraperWithStub(&now)
	fetcher.pages[1] = linkedInCardHTML("one")
	fetcher.SetCircuit(true)
	got, err := scraper.FetchJobs(ctx, []string{"python"}, "Chile", 24, nil)
	if err != nil || len(got) != 0 {
		t.Fatalf("circuit-open fetch = %+v, %v", got, err)
	}
	if calls := fetcher.Calls(); len(calls) != 0 {
		t.Fatalf("circuit-open fetch made %d calls", len(calls))
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := scraper.FetchJobs(canceled, []string{"python"}, "Chile", 24, nil); err == nil {
		t.Error("canceled context must abort the fetch")
	}
}

func TestLinkedInProxyHostsAndRegistry(t *testing.T) {
	hosts := LinkedInProxyHosts()
	want := []string{"www.linkedin.com", "cl.linkedin.com"}
	if len(hosts) != len(want) {
		t.Fatalf("got %d proxy hosts, want %d", len(hosts), len(want))
	}
	for index := range want {
		if hosts[index] != want[index] {
			t.Fatalf("proxy host %d = %q, want %q", index, hosts[index], want[index])
		}
	}
	scraper := DefaultRegistry().Get(LinkedInSlug)
	if scraper == nil || scraper.Source() != LinkedInSource {
		t.Fatalf("default registry LinkedIn = %+v", scraper)
	}
}
