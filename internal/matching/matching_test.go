package matching

import (
	"context"
	"reflect"
	"testing"

	"github.com/NikoMalek/job-sniper-discord-bot/internal/data"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/job"
)

func newTestMatcher(t *testing.T) *Matcher {
	t.Helper()
	catalog, err := data.Load(context.Background())
	if err != nil {
		t.Fatalf("data.Load() unexpected error: %v", err)
	}
	return NewWithCatalog(catalog, nil)
}

func makeJob(title, description string) job.Job {
	return job.Job{
		ID:          "1",
		Title:       title,
		Company:     "Acme",
		Location:    "Santiago, Chile",
		URL:         "https://example.com/jobs/1",
		Source:      "LinkedIn",
		PublishedAt: "hace 1 hora",
		Description: description,
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestNormalize(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		want  string
	}{
		{name: "accents and case", value: "  Ingeniería   de Software ", want: "ingenieria de software"},
		{name: "sharp s", value: "Straße", want: "strasse"},
		{name: "punctuation preserved", value: "Front-End", want: "front-end"},
		{name: "empty", value: "   ", want: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := Normalize(test.value); got != test.want {
				t.Fatalf("Normalize(%q) = %q, want %q", test.value, got, test.want)
			}
		})
	}
}

func TestKeywordVariants(t *testing.T) {
	matcher := newTestMatcher(t)
	for _, test := range []struct {
		keyword string
		want    []string
	}{
		{keyword: "Ingeniero de Software", want: []string{"ingeniero de software", "software engineer"}},
		{keyword: "Frontend", want: []string{"frontend", "front-end", "front end"}},
		{keyword: "   ", want: nil},
	} {
		got := matcher.KeywordVariants(test.keyword)
		if !reflect.DeepEqual(got, test.want) {
			t.Errorf("KeywordVariants(%q) = %v, want %v", test.keyword, got, test.want)
		}
	}
}

func TestContainsTerm(t *testing.T) {
	for _, test := range []struct {
		text string
		term string
		want bool
	}{
		{text: "Alumno en Práctica Desarrollador", term: "practica", want: true},
		{text: "Ingeniero de Software Internals", term: "intern", want: false},
		{text: "Desarrollador frontend", term: "front-end", want: false},
		{text: "Desarrollador front-end", term: "front-end", want: true},
	} {
		if got := ContainsTerm(test.text, test.term); got != test.want {
			t.Errorf("ContainsTerm(%q, %q) = %v, want %v", test.text, test.term, got, test.want)
		}
	}
}

func TestMatchesKeyword(t *testing.T) {
	matcher := newTestMatcher(t)
	for _, test := range []struct {
		name    string
		job     job.Job
		keyword string
		want    bool
	}{
		{
			name:    "title term",
			job:     makeJob("Desarrollador Python", ""),
			keyword: "python",
			want:    true,
		},
		{
			name:    "alias overlap with title",
			job:     makeJob("We need an engineer", ""),
			keyword: "ingeniero de software",
			want:    true,
		},
		{
			name:    "single word in description",
			job:     makeJob("Backend Developer", "Python required"),
			keyword: "python",
			want:    true,
		},
		{
			name:    "multiword description is ignored",
			job:     makeJob("Backend Developer", "software engineer"),
			keyword: "ingeniero de software",
			want:    false,
		},
		{
			name:    "unrelated",
			job:     makeJob("Diseñador gráfico", "Trabajo con Photoshop"),
			keyword: "python",
			want:    false,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := matcher.MatchesKeyword(test.job, test.keyword); got != test.want {
				t.Fatalf("MatchesKeyword() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestMatchesBlacklistedKeywordInTitleWithAccentsAndCase(t *testing.T) {
	matcher := newTestMatcher(t)
	j := makeJob("Alumno en Práctica Desarrollador Python", "")

	if got := matcher.MatchesBlacklistedKeyword(j, []string{"practica"}); got != "practica" {
		t.Fatalf("MatchesBlacklistedKeyword(practica) = %q, want practica", got)
	}
	if got := matcher.MatchesBlacklistedKeyword(j, []string{"PRÁCTICA"}); got != "PRÁCTICA" {
		t.Fatalf("MatchesBlacklistedKeyword(PRÁCTICA) = %q, want PRÁCTICA", got)
	}
}

func TestBlacklistedKeywordIgnoresDescriptionOnly(t *testing.T) {
	matcher := newTestMatcher(t)
	j := makeJob(
		"Desarrollador Backend Python",
		"Trabajarás junto a un docente y apoyarás a un practicante.",
	)
	if got := matcher.MatchesBlacklistedKeyword(j, []string{"docente", "practicante"}); got != "" {
		t.Fatalf("MatchesBlacklistedKeyword() = %q, want no match", got)
	}
}

func TestBlacklistedKeywordRespectsWordBoundaries(t *testing.T) {
	matcher := newTestMatcher(t)
	j := makeJob("Ingeniero de Software Internals", "")
	if got := matcher.MatchesBlacklistedKeyword(j, []string{"intern"}); got != "" {
		t.Fatalf("MatchesBlacklistedKeyword(intern) = %q, want no match", got)
	}
}

func TestExpandBlacklistKeywordsWithoutAliases(t *testing.T) {
	matcher := newTestMatcher(t)
	expanded, autoAliases := matcher.ExpandBlacklistKeywords(
		[]string{"Práctica", "docente", "práctica"}, false,
	)
	if !reflect.DeepEqual(expanded, []string{"Práctica", "docente"}) {
		t.Fatalf("expanded = %v, want [Práctica docente]", expanded)
	}
	if len(autoAliases) != 0 {
		t.Fatalf("autoAliases = %v, want empty", autoAliases)
	}
}

func TestExpandBlacklistKeywordsWithAliases(t *testing.T) {
	matcher := newTestMatcher(t)
	expanded, autoAliases := matcher.ExpandBlacklistKeywords([]string{"Práctica", "docente"}, true)
	if !reflect.DeepEqual(expanded[:2], []string{"Práctica", "docente"}) {
		t.Fatalf("expanded prefix = %v, want [Práctica docente]", expanded[:2])
	}
	for _, alias := range []string{"practicante", "pasante", "intern", "profesor", "relator"} {
		if !containsString(autoAliases, alias) {
			t.Errorf("autoAliases = %v, want %q", autoAliases, alias)
		}
	}
	for _, base := range []string{"practica", "docente"} {
		if containsString(autoAliases, base) {
			t.Errorf("autoAliases = %v, must not contain %q", autoAliases, base)
		}
	}
}

func TestMatchesCompanyBlacklist(t *testing.T) {
	for _, test := range []struct {
		company   string
		blacklist []string
		want      bool
	}{
		{company: "Acme Corporation", blacklist: []string{"corp"}, want: true},
		{company: "ACME S.A.", blacklist: []string{"acme"}, want: true},
		{company: "Globex", blacklist: []string{"acme"}, want: false},
		{company: "Globex", blacklist: nil, want: false},
	} {
		if got := MatchesCompanyBlacklist(test.company, test.blacklist); got != test.want {
			t.Errorf("MatchesCompanyBlacklist(%q, %v) = %v, want %v", test.company, test.blacklist, got, test.want)
		}
	}
}

func TestNewHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(ctx); err == nil {
		t.Fatal("New(canceled) error = nil, want an error")
	}
}
