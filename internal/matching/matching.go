// Package matching applies keyword, blacklist and language filters to jobs.
package matching

import (
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/NikoMalek/job-sniper-discord-bot/internal/data"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/job"
)

// Matcher holds the embedded catalog data needed by keyword and blacklist
// matching. Its maps are read-only after construction and safe to share.
type Matcher struct {
	keywordAliases  map[string][]string
	blacklistGroups [][]string
	language        *languageFilter
}

// New loads the embedded catalogs and builds a matcher with the shared
// language detector.
func New(ctx context.Context) (*Matcher, error) {
	catalog, err := data.Load(ctx)
	if err != nil {
		return nil, fmt.Errorf("load matching catalogs: %w", err)
	}
	return NewWithCatalog(catalog, nil), nil
}

// NewWithCatalog builds a matcher from an already-loaded catalog. A nil
// detector selects the package-level detector that is built once.
func NewWithCatalog(catalog *data.Catalog, detector Detector) *Matcher {
	matcher := &Matcher{language: newLanguageFilter(detector)}
	if catalog == nil {
		return matcher
	}
	matcher.keywordAliases = catalog.KeywordAliases()
	matcher.blacklistGroups = catalog.BlacklistKeywordAliasGroups()
	return matcher
}

// accentFolding approximates NFKD plus casefold for Latin characters without
// adding a Unicode normalization dependency.
var accentFolding = map[rune]string{
	'à': "a", 'á': "a", 'â': "a", 'ã': "a", 'ä': "a", 'å': "a",
	'ā': "a", 'ă': "a", 'ą': "a",
	'ç': "c", 'ć': "c", 'ĉ': "c", 'ċ': "c", 'č': "c",
	'ď': "d",
	'è': "e", 'é': "e", 'ê': "e", 'ë': "e", 'ē': "e", 'ė': "e", 'ę': "e", 'ě': "e",
	'ĝ': "g", 'ğ': "g", 'ġ': "g", 'ģ': "g", 'ĥ': "h",
	'ì': "i", 'í': "i", 'î': "i", 'ï': "i", 'ĩ': "i", 'ī': "i", 'ĭ': "i", 'ĳ': "ij",
	'ĺ': "l", 'ļ': "l", 'ľ': "l", 'ŀ': "l",
	'ñ': "n", 'ń': "n", 'ņ': "n", 'ň': "n",
	'ò': "o", 'ó': "o", 'ô': "o", 'õ': "o", 'ö': "o",
	'ō': "o", 'ŏ': "o", 'ő': "o",
	'ŕ': "r", 'ŗ': "r", 'ř': "r",
	'ś': "s", 'ŝ': "s", 'ş': "s", 'š': "s",
	'ţ': "t", 'ť': "t",
	'ù': "u", 'ú': "u", 'û': "u", 'ü': "u", 'ũ': "u", 'ū': "u", 'ŭ': "u", 'ů': "u", 'ű': "u", 'ų': "u",
	'ý': "y", 'ÿ': "y",
	'ź': "z", 'ż': "z", 'ž': "z",
	'ß': "ss",
}

// Normalize folds accent and case differences and collapses whitespace.
func Normalize(value string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(value) {
		if replacement, ok := accentFolding[r]; ok {
			builder.WriteString(replacement)
			continue
		}
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		builder.WriteRune(r)
	}
	return strings.Join(strings.Fields(builder.String()), " ")
}

func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

func hasWordBefore(value string, index int) bool {
	if index <= 0 {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(value[:index])
	return isWordRune(r)
}

func hasWordAfter(value string, index int) bool {
	if index >= len(value) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(value[index:])
	return isWordRune(r)
}

// ContainsTerm reports whether term appears in text as a whole word after
// normalization. It is equivalent to the Python regex with non-word lookaround.
func ContainsTerm(text, term string) bool {
	normalizedText := Normalize(text)
	normalizedTerm := Normalize(term)
	if normalizedTerm == "" {
		return false
	}
	for offset := 0; offset <= len(normalizedText)-len(normalizedTerm); {
		index := strings.Index(normalizedText[offset:], normalizedTerm)
		if index < 0 {
			return false
		}
		start := offset + index
		end := start + len(normalizedTerm)
		if !hasWordBefore(normalizedText, start) && !hasWordAfter(normalizedText, end) {
			return true
		}
		offset = start + 1
	}
	return false
}

// KeywordVariants returns the normalized keyword followed by its catalog aliases.
func (m *Matcher) KeywordVariants(keyword string) []string {
	term := Normalize(keyword)
	if term == "" {
		return nil
	}
	variants := []string{term}
	return append(variants, m.keywordAliases[term]...)
}

// MatchesKeyword applies the title, overlap and description keyword rules.
func (m *Matcher) MatchesKeyword(j job.Job, keyword string) bool {
	variants := m.KeywordVariants(keyword)
	if len(variants) == 0 {
		return false
	}
	for _, variant := range variants {
		if ContainsTerm(j.Title, variant) {
			return true
		}
	}

	titleWords := make(map[string]struct{})
	for _, word := range strings.Fields(Normalize(j.Title)) {
		titleWords[word] = struct{}{}
	}
	for _, variant := range variants {
		words := strings.Fields(variant)
		if len(words) > 1 {
			overlap := 0
			for _, word := range words {
				if _, ok := titleWords[word]; ok {
					overlap++
				}
			}
			if float64(overlap)/float64(len(words)) >= 0.5 {
				return true
			}
			continue
		}
		if ContainsTerm(j.Description, variant) {
			return true
		}
	}
	return false
}

// ExpandBlacklistKeywords removes normalized duplicates and optionally adds
// every alias group member that shares a group with a requested keyword.
func (m *Matcher) ExpandBlacklistKeywords(keywords []string, includeAliases bool) ([]string, []string) {
	baseTerms := make([]string, 0, len(keywords))
	seen := make(map[string]struct{}, len(keywords))
	for _, value := range keywords {
		norm := Normalize(value)
		if norm == "" {
			continue
		}
		if _, ok := seen[norm]; ok {
			continue
		}
		seen[norm] = struct{}{}
		baseTerms = append(baseTerms, value)
	}
	if !includeAliases {
		return baseTerms, []string{}
	}

	result := append([]string(nil), baseTerms...)
	aliasesAdded := make([]string, 0)
	for _, value := range baseTerms {
		norm := Normalize(value)
		for _, group := range m.blacklistGroups {
			if !groupContains(group, norm) {
				continue
			}
			for _, candidate := range group {
				if _, ok := seen[candidate]; ok {
					continue
				}
				seen[candidate] = struct{}{}
				result = append(result, candidate)
				aliasesAdded = append(aliasesAdded, candidate)
			}
		}
	}
	return result, aliasesAdded
}

func groupContains(group []string, value string) bool {
	for _, member := range group {
		if member == value {
			return true
		}
	}
	return false
}

// MatchesBlacklistedKeyword returns the first blocked keyword found in the
// job title, or an empty string when none matches.
func (m *Matcher) MatchesBlacklistedKeyword(j job.Job, blacklisted []string) string {
	if len(blacklisted) == 0 || j.Title == "" {
		return ""
	}
	for _, blocked := range blacklisted {
		if strings.TrimSpace(blocked) == "" {
			continue
		}
		for _, variant := range m.KeywordVariants(blocked) {
			if ContainsTerm(j.Title, variant) {
				return blocked
			}
		}
	}
	return ""
}

// MatchesCompanyBlacklist reports whether any blocked company is a substring
// of the job company, ignoring case.
func MatchesCompanyBlacklist(company string, blacklisted []string) bool {
	foldedCompany := strings.ToLower(company)
	for _, blocked := range blacklisted {
		if strings.Contains(foldedCompany, strings.ToLower(blocked)) {
			return true
		}
	}
	return false
}
