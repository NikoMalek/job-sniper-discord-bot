package locations

import (
	"sort"
	"strings"
	"unicode"
)

// accentFolding approximates Python's NFKD plus casefold for the Latin
// characters used by the embedded catalogs and typical user input.
var accentFolding = map[rune]string{
	'à': "a", 'á': "a", 'â': "a", 'ã': "a", 'ä': "a", 'å': "a", 'æ': "ae",
	'ā': "a", 'ă': "a", 'ą': "a",
	'ç': "c", 'ć': "c", 'ĉ': "c", 'ċ': "c", 'č': "c",
	'ď': "d", 'đ': "d", 'ð': "d",
	'è': "e", 'é': "e", 'ê': "e", 'ë': "e", 'ē': "e", 'ė': "e", 'ę': "e", 'ě': "e",
	'ĝ': "g", 'ğ': "g", 'ġ': "g", 'ģ': "g", 'ĥ': "h",
	'ì': "i", 'í': "i", 'î': "i", 'ï': "i", 'ĩ': "i", 'ī': "i", 'ĭ': "i", 'ĳ': "ij",
	'ĺ': "l", 'ļ': "l", 'ľ': "l", 'ŀ': "l", 'ł': "l",
	'ñ': "n", 'ń': "n", 'ņ': "n", 'ň': "n", 'ŉ': "n", 'ŋ': "n",
	'ò': "o", 'ó': "o", 'ô': "o", 'õ': "o", 'ö': "o", 'ø': "o", 'œ': "oe",
	'ō': "o", 'ŏ': "o", 'ő': "o",
	'ŕ': "r", 'ŗ': "r", 'ř': "r",
	'ś': "s", 'ŝ': "s", 'ş': "s", 'š': "s",
	'ţ': "t", 'ť': "t", 'ŧ': "t",
	'ù': "u", 'ú': "u", 'û': "u", 'ü': "u", 'ũ': "u", 'ū': "u", 'ŭ': "u", 'ů': "u", 'ű': "u", 'ų': "u",
	'ý': "y", 'ÿ': "y",
	'ź': "z", 'ż': "z", 'ž': "z",
	'ß': "ss",
}

// Normalize lowercases, folds accents and collapses whitespace.
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

func displayCase(value string) string {
	particles := map[string]struct{}{
		"de": {}, "del": {}, "la": {}, "las": {}, "los": {}, "y": {}, "da": {}, "do": {},
	}
	words := strings.Fields(value)
	for index, word := range words {
		if index > 0 {
			if _, ok := particles[strings.ToLower(word)]; ok {
				words[index] = strings.ToLower(word)
				continue
			}
		}
		runes := []rune(word)
		if len(runes) == 0 {
			continue
		}
		runes[0] = unicode.ToUpper(runes[0])
		words[index] = string(runes)
	}
	return strings.Join(words, " ")
}

func upper(value string) string {
	return strings.ToUpper(value)
}

func toLowerRune(r rune) rune {
	return unicode.ToLower(r)
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
