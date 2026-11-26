package languages

import "testing"

func TestNormalizeLanguageAliasesAndCase(t *testing.T) {
	tests := []struct {
		value string
		want  string
	}{
		{value: "  ENGLISH ", want: "english"},
		{value: "Inglés", want: "english"},
		{value: "es", want: "spanish"},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			if got := NormalizeLanguage(tt.value); got != tt.want {
				t.Fatalf("NormalizeLanguage(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestNormalizeLanguageUnknownValueIsCleanedNotRejected(t *testing.T) {
	if got := NormalizeLanguage("Klingon"); got != "klingon" {
		t.Fatalf("NormalizeLanguage(Klingon) = %q, want klingon", got)
	}
}

func TestParseLanguagesRemovesDuplicatesAndKeepsOrder(t *testing.T) {
	got := ParseLanguages("english, English, spanish,, ")
	want := []string{"english", "spanish"}
	if len(got) != len(want) {
		t.Fatalf("ParseLanguages() = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("ParseLanguages() = %v, want %v", got, want)
		}
	}
}

func TestParseLanguagesEmpty(t *testing.T) {
	if got := ParseLanguages(""); len(got) != 0 {
		t.Fatalf("ParseLanguages(\"\") = %v, want an empty slice", got)
	}
}

func TestTrackingTextPerLanguageWithSpanishFallback(t *testing.T) {
	tests := []struct {
		language string
		want     string
	}{
		{language: "english", want: "🔗 View Job"},
		{language: "spanish", want: "🔗 Ver oferta"},
		{language: "klingon", want: "🔗 Ver oferta"},
	}
	for _, tt := range tests {
		t.Run(tt.language, func(t *testing.T) {
			if got := TrackingText("view_job", tt.language, nil); got != tt.want {
				t.Fatalf("TrackingText(view_job, %q) = %q, want %q", tt.language, got, tt.want)
			}
		})
	}
}

func TestSupportedLanguagesAndLabels(t *testing.T) {
	wantOrder := []string{"spanish", "english", "portuguese", "french", "german"}
	if len(SupportedLanguages) != len(wantOrder) {
		t.Fatalf("len(SupportedLanguages) = %d, want %d", len(SupportedLanguages), len(wantOrder))
	}
	for _, language := range wantOrder {
		if !IsSupported(language) {
			t.Errorf("IsSupported(%q) = false, want true", language)
		}
	}
	if IsSupported("klingon") {
		t.Error("IsSupported(klingon) = true, want false")
	}

	wantLabels := map[string]string{
		"spanish":    "español",
		"english":    "inglés",
		"portuguese": "portugués",
		"french":     "francés",
		"german":     "alemán",
	}
	if len(LanguageLabels) != len(wantLabels) {
		t.Fatalf("len(LanguageLabels) = %d, want %d", len(LanguageLabels), len(wantLabels))
	}
	for language, want := range wantLabels {
		if got := Label(language); got != want {
			t.Errorf("Label(%q) = %q, want %q", language, got, want)
		}
	}
	if got := Label("klingon"); got != "klingon" {
		t.Errorf("Label(klingon) = %q, want klingon", got)
	}
}
