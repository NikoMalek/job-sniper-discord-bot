package matching

import (
	"context"
	"reflect"
	"testing"

	"github.com/NikoMalek/job-sniper-discord-bot/internal/data"
	"github.com/pemistahl/lingua-go"
)

type fakeDetector struct {
	detect func(string) (lingua.Language, bool)
	calls  []string
}

func (f *fakeDetector) DetectLanguageOf(text string) (lingua.Language, bool) {
	f.calls = append(f.calls, text)
	return f.detect(text)
}

func newTestMatcherWithDetector(t *testing.T, detector Detector) *Matcher {
	t.Helper()
	catalog, err := data.Load(context.Background())
	if err != nil {
		t.Fatalf("data.Load() unexpected error: %v", err)
	}
	return NewWithCatalog(catalog, detector)
}

func always(language lingua.Language) func(string) (lingua.Language, bool) {
	return func(string) (lingua.Language, bool) { return language, true }
}

func TestMatchesLanguagePassesWithoutConfiguredLanguages(t *testing.T) {
	detector := &fakeDetector{detect: always(lingua.Spanish)}
	matcher := newTestMatcherWithDetector(t, detector)

	if !matcher.MatchesLanguage(makeJob("Oferta", "Descripción larga"), nil) {
		t.Fatal("MatchesLanguage(nil) = false, want true")
	}
	if !matcher.MatchesLanguage(makeJob("Oferta", "Descripción larga"), []string{"klingon"}) {
		t.Fatal("MatchesLanguage(unknown language) = false, want true")
	}
	if len(detector.calls) != 0 {
		t.Fatalf("detector calls = %v, want none", detector.calls)
	}
}

func TestMatchesLanguageUsesTitleFirst(t *testing.T) {
	detector := &fakeDetector{detect: always(lingua.Spanish)}
	matcher := newTestMatcherWithDetector(t, detector)
	j := makeJob("  Esta es una oferta de trabajo con más de veinte caracteres  ", "")

	if !matcher.MatchesLanguage(j, []string{"spanish"}) {
		t.Fatal("MatchesLanguage() = false, want true")
	}
	want := []string{"Esta es una oferta de trabajo con más de veinte caracteres"}
	if !reflect.DeepEqual(detector.calls, want) {
		t.Fatalf("detector calls = %v, want %v", detector.calls, want)
	}
}

func TestMatchesLanguageSkipsShortFragments(t *testing.T) {
	detector := &fakeDetector{detect: always(lingua.Spanish)}
	matcher := newTestMatcherWithDetector(t, detector)
	j := makeJob("Oferta", "Corto.\nTambién corto.")

	if !matcher.MatchesLanguage(j, []string{"spanish"}) {
		t.Fatal("MatchesLanguage() = false, want true when no fragment is detected")
	}
	if len(detector.calls) != 0 {
		t.Fatalf("detector calls = %v, want none", detector.calls)
	}
}

func TestMatchesLanguageSplitsFragments(t *testing.T) {
	want := []string{
		"Primera frase suficientemente larga para ser detectada.",
		"Segunda frase suficientemente larga para ser detectada!",
	}
	detector := &fakeDetector{detect: func(text string) (lingua.Language, bool) {
		if text == want[0] {
			return lingua.English, true
		}
		return lingua.Spanish, true
	}}
	matcher := newTestMatcherWithDetector(t, detector)
	description := want[0] + "\n" + want[1]

	if !matcher.MatchesLanguage(makeJob("Oferta", description), []string{"spanish"}) {
		t.Fatal("MatchesLanguage() = false, want true")
	}
	if !reflect.DeepEqual(detector.calls, want) {
		t.Fatalf("detector calls = %v, want %v", detector.calls, want)
	}
}

func TestMatchesLanguageRejectsDetectedDisallowedFragments(t *testing.T) {
	detector := &fakeDetector{detect: always(lingua.Spanish)}
	matcher := newTestMatcherWithDetector(t, detector)
	j := makeJob("Oferta", "Una descripción suficientemente larga para ser detectada como español.")

	if matcher.MatchesLanguage(j, []string{"english"}) {
		t.Fatal("MatchesLanguage() = true, want false")
	}
	if len(detector.calls) == 0 {
		t.Fatal("detector was not called for the long fragment")
	}
}

func TestMatchesLanguagePassesWhenDetectionIsUnknown(t *testing.T) {
	detector := &fakeDetector{detect: func(string) (lingua.Language, bool) {
		return lingua.Unknown, false
	}}
	matcher := newTestMatcherWithDetector(t, detector)
	j := makeJob("Oferta", "Una descripción suficientemente larga para no ser detectada.")

	if !matcher.MatchesLanguage(j, []string{"english"}) {
		t.Fatal("MatchesLanguage() = false, want true when nothing is detected")
	}
}

func TestLanguageFilterWithRealDetector(t *testing.T) {
	matcher, err := New(context.Background())
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	for _, test := range []struct {
		language string
		text     string
	}{
		{
			language: "spanish",
			text:     "El mercado laboral chileno busca ingenieros de software con experiencia desarrollando aplicaciones web para empresas internacionales.",
		},
		{
			language: "english",
			text:     "The engineering team is looking for an experienced software developer to build reliable web applications for our international customers.",
		},
		{
			language: "portuguese",
			text:     "A equipe de engenharia procura um desenvolvedor de software experiente para criar aplicações web confiáveis para clientes internacionais.",
		},
		{
			language: "french",
			text:     "L'équipe d'ingénierie recherche un développeur de logiciels expérimenté pour créer des applications web fiables destinées à des clients internationaux.",
		},
		{
			language: "german",
			text:     "Das Engineering-Team sucht einen erfahrenen Softwareentwickler, um zuverlässige Webanwendungen für internationale Kunden zu entwickeln.",
		},
	} {
		t.Run(test.language, func(t *testing.T) {
			if !matcher.MatchesLanguage(makeJob("Oferta", test.text), []string{test.language}) {
				t.Errorf("MatchesLanguage() = false, want true for %s sample", test.language)
			}
		})
	}
}
