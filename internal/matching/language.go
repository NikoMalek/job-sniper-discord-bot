package matching

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/NikoMalek/job-sniper-discord-bot/internal/job"
	"github.com/pemistahl/lingua-go"
)

// Detector is the part of lingua-go used by the language filter. Tests can
// replace it; production uses a detector built once from the five languages.
type Detector interface {
	DetectLanguageOf(text string) (lingua.Language, bool)
}

var languageMap = map[string]lingua.Language{
	"spanish":    lingua.Spanish,
	"english":    lingua.English,
	"portuguese": lingua.Portuguese,
	"french":     lingua.French,
	"german":     lingua.German,
}

var defaultDetector = lingua.NewLanguageDetectorBuilder().
	FromLanguages(
		lingua.Spanish,
		lingua.English,
		lingua.Portuguese,
		lingua.French,
		lingua.German,
	).
	Build()

type languageFilter struct {
	detector Detector
}

func newLanguageFilter(detector Detector) *languageFilter {
	if detector == nil {
		detector = defaultDetector
	}
	return &languageFilter{detector: detector}
}

// MatchesLanguage applies the title-first, fragment-second language rules.
func (m *Matcher) MatchesLanguage(j job.Job, languages []string) bool {
	return m.language.Matches(j, languages)
}

func (f *languageFilter) Matches(j job.Job, languages []string) bool {
	if len(languages) == 0 {
		return true
	}
	allowed := make(map[lingua.Language]struct{}, len(languages))
	for _, language := range languages {
		if detected, ok := languageMap[language]; ok {
			allowed[detected] = struct{}{}
		}
	}
	if len(allowed) == 0 {
		return true
	}

	title := strings.TrimSpace(j.Title)
	if utf8.RuneCountInString(title) >= 20 {
		if detected, ok := f.detector.DetectLanguageOf(title); ok {
			if _, ok := allowed[detected]; ok {
				return true
			}
		}
	}

	detectedFragments := 0
	for _, fragment := range splitFragments(j.Description) {
		clean := strings.Join(strings.Fields(fragment), " ")
		if utf8.RuneCountInString(clean) < 30 {
			continue
		}
		detected, ok := f.detector.DetectLanguageOf(clean)
		if !ok {
			continue
		}
		detectedFragments++
		if _, ok := allowed[detected]; ok {
			return true
		}
	}
	return detectedFragments == 0
}

func isSentencePunctuation(r rune) bool {
	return r == '.' || r == '!' || r == '?'
}

// splitFragments mirrors re.split(r"(?<=[.!?])\s+|\n{1,}", text) without
// lookbehind support. Whitespace after sentence punctuation is consumed;
// otherwise newline runs are consumed.
func splitFragments(text string) []string {
	fragments := make([]string, 0)
	start := 0
	for index := 0; index < len(text); {
		r, size := utf8.DecodeRuneInString(text[index:])
		if !unicode.IsSpace(r) {
			index += size
			continue
		}

		end := index
		firstNewline := -1
		for end < len(text) {
			next, nextSize := utf8.DecodeRuneInString(text[end:])
			if !unicode.IsSpace(next) {
				break
			}
			if next == '\n' && firstNewline < 0 {
				firstNewline = end
			}
			end += nextSize
		}

		previous, _ := utf8.DecodeLastRuneInString(text[:index])
		switch {
		case isSentencePunctuation(previous):
			fragments = append(fragments, text[start:index])
			start = end
		case firstNewline >= 0:
			fragments = append(fragments, text[start:firstNewline])
			start = firstNewline
			for start < end {
				next, nextSize := utf8.DecodeRuneInString(text[start:])
				if next != '\n' {
					break
				}
				start += nextSize
			}
		}
		index = end
	}
	return append(fragments, text[start:])
}
