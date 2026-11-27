package scrapers

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/NikoMalek/job-sniper-discord-bot/internal/httpclient"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/job"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/locations"
)

const fixtureRoot = "../../fixtures"

type boardFixtureMeta struct {
	Portal string `json:"portal"`
	Search struct {
		Keywords []string `json:"keywords"`
		Location string   `json:"location"`
	} `json:"search"`
	MaxAgeHours    float64   `json:"max_age_hours"`
	SearchLocation string    `json:"search_location"`
	CapturedAt     time.Time `json:"captured_at"`
	RequestCount   int       `json:"request_count"`
	JobCount       int       `json:"job_count"`
}

func boardFixtureDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(fixtureRoot, "getonbrd")
	if _, err := os.Stat(dir); err != nil {
		if os.IsNotExist(err) {
			t.Skip("getonbrd fixtures are not available")
		}
		t.Fatalf("stat %s: %v", dir, err)
	}
	return dir
}

func loadBoardFixture(t *testing.T, search string) (boardFixtureMeta, []job.Job) {
	t.Helper()
	dir := filepath.Join(boardFixtureDir(t), search)

	rawMeta, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		t.Fatalf("read meta.json: %v", err)
	}
	var meta boardFixtureMeta
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

func newFixtureScraper(
	t *testing.T, ctx context.Context, search string, meta boardFixtureMeta,
) (*GetOnBoardScraper, *httpclient.ReplayFetcher) {
	t.Helper()
	fetcher, err := httpclient.NewReplayFetcher(ctx, fixtureRoot, "getonbrd", search)
	if err != nil {
		t.Fatalf("new replay fetcher: %v", err)
	}
	scraper := NewGetOnBoardScraper(GetOnBoardOptions{
		Fetcher:    fetcher,
		Clock:      func() time.Time { return meta.CapturedAt },
		CacheClock: func() time.Time { return meta.CapturedAt },
		Sleep:      func(context.Context, time.Duration) error { return nil },
		Random:     func() float64 { return 0.5 },
	})
	return scraper, fetcher
}

func requireSameJobs(t *testing.T, got, want []job.Job) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d jobs, want %d", len(got), len(want))
	}
	for index := range want {
		gotJob, wantJob := got[index], want[index]
		strings := []struct {
			field string
			got   string
			want  string
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
		for _, comparison := range strings {
			if comparison.got != comparison.want {
				t.Errorf("job %d %s = %q, want %q", index, comparison.field, comparison.got, comparison.want)
			}
		}
		gotTime, wantTime := gotJob.PublishedAtDatetime, wantJob.PublishedAtDatetime
		if (gotTime == nil) != (wantTime == nil) {
			t.Errorf("job %d published_at_datetime presence differs", index)
			continue
		}
		if gotTime != nil && !gotTime.Equal(*wantTime) {
			t.Errorf("job %d published_at_datetime = %s, want %s", index, gotTime, wantTime)
		}
	}
}

func TestGetOnBoardReplaysFixtures(t *testing.T) {
	searches := []string{"python-santiago", "backend-remoto", "sin-resultados"}
	for _, search := range searches {
		t.Run(search, func(t *testing.T) {
			ctx := context.Background()
			meta, expected := loadBoardFixture(t, search)
			scraper, fetcher := newFixtureScraper(t, ctx, search, meta)

			intent, err := locations.Parse(meta.Search.Location)
			if err != nil {
				t.Fatalf("parse fixture location: %v", err)
			}
			plan := scraper.Plan(&intent)
			if !plan.Supported || plan.SearchLocation != meta.SearchLocation {
				t.Fatalf("plan = %+v, want search location %q", plan, meta.SearchLocation)
			}

			got, err := scraper.FetchJobs(
				ctx, meta.Search.Keywords, meta.Search.Location, meta.MaxAgeHours, nil,
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
		})
	}
}

func TestGetOnBoardFiltersKnownAndOldJobs(t *testing.T) {
	ctx := context.Background()
	search := "backend-remoto"
	meta, expected := loadBoardFixture(t, search)
	if len(expected) == 0 {
		t.Fatal("fixture must contain jobs")
	}
	scraper, fetcher := newFixtureScraper(t, ctx, search, meta)

	known := map[string]struct{}{expected[0].ID: {}}
	got, err := scraper.FetchJobs(
		ctx, meta.Search.Keywords, meta.Search.Location, meta.MaxAgeHours, known,
	)
	if err != nil {
		t.Fatalf("fetch jobs with known id: %v", err)
	}
	if len(got) != len(expected)-1 {
		t.Fatalf("got %d jobs with one known id, want %d", len(got), len(expected)-1)
	}
	for _, result := range got {
		if result.ID == expected[0].ID {
			t.Fatalf("known job %q was returned", result.ID)
		}
	}

	got, err = scraper.FetchJobs(
		ctx, meta.Search.Keywords, meta.Search.Location, meta.MaxAgeHours, nil,
	)
	if err != nil {
		t.Fatalf("fetch jobs from cache: %v", err)
	}
	requireSameJobs(t, got, expected)
	if calls := fetcher.Calls(); len(calls) != 0 {
		t.Fatalf("cached search made %d extra fetcher calls", len(calls))
	}

	oldScraper, _ := newFixtureScraper(t, ctx, search, meta)
	got, err = oldScraper.FetchJobs(
		ctx, meta.Search.Keywords, meta.Search.Location, 0, nil,
	)
	if err != nil {
		t.Fatalf("fetch jobs with zero age: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("zero-hour window returned %d jobs", len(got))
	}
}

func TestGetOnBoardCacheExpires(t *testing.T) {
	ctx := context.Background()
	meta, _ := loadBoardFixture(t, "sin-resultados")
	fetcher, err := httpclient.NewReplayFetcher(ctx, fixtureRoot, "getonbrd", "sin-resultados")
	if err != nil {
		t.Fatalf("new replay fetcher: %v", err)
	}
	cacheNow := meta.CapturedAt
	scraper := NewGetOnBoardScraper(GetOnBoardOptions{
		Fetcher:    fetcher,
		Clock:      func() time.Time { return meta.CapturedAt },
		CacheClock: func() time.Time { return cacheNow },
		Sleep:      func(context.Context, time.Duration) error { return nil },
		Random:     func() float64 { return 0.5 },
	})

	if _, err := scraper.FetchJobs(
		ctx, meta.Search.Keywords, meta.Search.Location, meta.MaxAgeHours, nil,
	); err != nil {
		t.Fatalf("first fetch: %v", err)
	}
	if calls := fetcher.Calls(); len(calls) != 1 {
		t.Fatalf("first fetch made %d calls, want 1", len(calls))
	}

	cacheNow = cacheNow.Add(getOnBoardCacheTTL + time.Second)
	if _, err := scraper.FetchJobs(
		ctx, meta.Search.Keywords, meta.Search.Location, meta.MaxAgeHours, nil,
	); err != nil {
		t.Fatalf("second fetch after expiry: %v", err)
	}
	if calls := fetcher.Calls(); len(calls) != 1 {
		t.Fatalf("expired cache made %d calls, want 1", len(calls))
	}
	if remaining := fetcher.Remaining(); remaining != 0 {
		t.Fatalf("%d recorded requests were not replayed", remaining)
	}
}

func TestGetOnBoardIgnoresEmptyKeywords(t *testing.T) {
	ctx := context.Background()
	meta, _ := loadBoardFixture(t, "sin-resultados")
	scraper, fetcher := newFixtureScraper(t, ctx, "sin-resultados", meta)

	got, err := scraper.FetchJobs(ctx, []string{"", "   "}, "Worldwide", 24, nil)
	if err != nil {
		t.Fatalf("fetch jobs: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("empty keywords returned %d jobs", len(got))
	}
	if calls := fetcher.Calls(); len(calls) != 0 {
		t.Fatalf("empty keywords made %d fetcher calls", len(calls))
	}
}

func TestGetOnBoardPlansLocations(t *testing.T) {
	scraper := NewGetOnBoardScraper(GetOnBoardOptions{})
	tests := []struct {
		location string
		want     string
	}{
		{"Worldwide", "Worldwide"},
		{"Remoto", "Remoto"},
		{"Remoto, Chile", "Remoto, Chile"},
		{"Chile", "Chile"},
		{"Santiago", "Santiago, Chile"},
		{"Santiago, Chile", "Santiago, Chile"},
	}
	for _, test := range tests {
		t.Run(test.location, func(t *testing.T) {
			intent, err := locations.Parse(test.location)
			if err != nil {
				t.Fatalf("parse %q: %v", test.location, err)
			}
			plan := scraper.Plan(&intent)
			if !plan.Supported {
				t.Fatalf("Get on Board must support %q", test.location)
			}
			if plan.SearchLocation != test.want {
				t.Fatalf("search location = %q, want %q", plan.SearchLocation, test.want)
			}
			if plan.Note == "" {
				t.Fatal("plan note must not be empty")
			}
		})
	}
}

func TestBoardHelpers(t *testing.T) {
	published := boardPublishedAt(json.Number("1795095444"))
	if published == nil {
		t.Fatal("valid timestamp was rejected")
	}
	if got := formatBoardPublished(*published); got != "19 nov 2026, 13:37" {
		t.Fatalf("formatted timestamp = %q", got)
	}
	if boardPublishedAt(true) != nil || boardPublishedAt("abc") != nil || boardPublishedAt(json.Number("0")) != nil {
		t.Fatal("invalid timestamp was accepted")
	}
	if got := boardHTMLText("<ul><li> Uno </li><li>Dos<br>Tres</li></ul>"); got != "Uno Dos Tres" {
		t.Fatalf("HTML text = %q", got)
	}
	if got := resolveBoardURL("/img/logo.png"); got != fmt.Sprintf("%s/img/logo.png", getOnBoardBaseURL) {
		t.Fatalf("resolved logo = %q", got)
	}
	if got := resolveBoardURL("javascript:alert(1)"); got != "" {
		t.Fatalf("non-HTTP logo = %q", got)
	}
	if bucket, err := boardAgeBucket(24.2); err != nil || bucket != 25 {
		t.Fatalf("age bucket = %d, %v", bucket, err)
	}
	if bucket, err := boardAgeBucket(-3); err != nil || bucket != 1 {
		t.Fatalf("negative age bucket = %d, %v", bucket, err)
	}
}
