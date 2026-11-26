// Package languages provides language aliases, normalization and parsing.
package languages

import "strings"

// LanguageAliases maps every accepted spelling to its canonical language.
var LanguageAliases = map[string]string{
	"es":         "spanish",
	"espanol":    "spanish",
	"español":    "spanish",
	"spanish":    "spanish",
	"en":         "english",
	"ingles":     "english",
	"inglés":     "english",
	"english":    "english",
	"pt":         "portuguese",
	"portugues":  "portuguese",
	"portugués":  "portuguese",
	"portuguese": "portuguese",
	"fr":         "french",
	"frances":    "french",
	"francés":    "french",
	"french":     "french",
	"de":         "german",
	"aleman":     "german",
	"alemán":     "german",
	"german":     "german",
}

// SupportedLanguages is the set of canonical languages the bot understands.
var SupportedLanguages = map[string]struct{}{
	"spanish":    {},
	"english":    {},
	"portuguese": {},
	"french":     {},
	"german":     {},
}

// LanguageLabels maps canonical languages to their Discord labels.
var LanguageLabels = map[string]string{
	"spanish":    "español",
	"english":    "inglés",
	"portuguese": "portugués",
	"french":     "francés",
	"german":     "alemán",
}

// LanguageLabelOrder is the display order of LanguageLabels.
var LanguageLabelOrder = []string{"spanish", "english", "portuguese", "french", "german"}

// NormalizeLanguage returns the canonical language, or the cleaned value for
// unknown inputs.
func NormalizeLanguage(value string) string {
	cleaned := strings.ToLower(strings.Join(strings.Fields(value), " "))
	if canonical, ok := LanguageAliases[cleaned]; ok {
		return canonical
	}
	return cleaned
}

// ParseLanguages splits a comma-separated list, normalizes every non-empty
// part and removes duplicates while preserving order.
func ParseLanguages(raw string) []string {
	languages := make([]string, 0)
	seen := make(map[string]struct{})
	for _, part := range strings.Split(raw, ",") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		language := NormalizeLanguage(part)
		if _, ok := seen[language]; ok {
			continue
		}
		seen[language] = struct{}{}
		languages = append(languages, language)
	}
	return languages
}

// IsSupported reports whether a canonical language is supported.
func IsSupported(language string) bool {
	_, ok := SupportedLanguages[language]
	return ok
}

// Label returns the Discord label for a canonical language.
func Label(language string) string {
	if label, ok := LanguageLabels[language]; ok {
		return label
	}
	return language
}

// trackingTexts contains only the key exercised by the ported Phase 3 tests;
// the remaining experimental UI templates belong to the bot phase.
var trackingTexts = map[string]map[string]string{
	"view_job": {
		"spanish":    "🔗 Ver oferta",
		"english":    "🔗 View Job",
		"portuguese": "🔗 Ver vaga",
		"french":     "🔗 Voir l'offre",
		"german":     "🔗 Stelle ansehen",
	},
}

// TrackingText returns a localized experimental-zone string. Unknown languages
// fall back to Spanish, matching the Python helper.
func TrackingText(key, language string, values map[string]string) string {
	translations, ok := trackingTexts[key]
	if !ok {
		return ""
	}
	template := translations[language]
	if template == "" {
		template = translations["spanish"]
	}
	replacements := make([]string, 0, len(values)*2)
	for name, value := range values {
		replacements = append(replacements, "{"+name+"}", value)
	}
	if len(replacements) == 0 {
		return template
	}
	return strings.NewReplacer(replacements...).Replace(template)
}
