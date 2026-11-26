// Package dedupe groups equivalent jobs found in the same scraping cycle.
package dedupe

import (
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

const (
	// TitleJaccardThreshold is the minimum title-token Jaccard similarity.
	TitleJaccardThreshold = 0.75
	// DescriptionContainmentThreshold is the minimum description containment.
	DescriptionContainmentThreshold = 0.60
	// DescriptionJaccardThreshold is the minimum description Jaccard similarity.
	DescriptionJaccardThreshold = 0.30
	// StrongContainmentThreshold is required when titles are not exact.
	StrongContainmentThreshold = 0.70
	// StrongJaccardThreshold is required when titles are not exact.
	StrongJaccardThreshold = 0.40
	// CompanyJaccardThreshold is the minimum company-token Jaccard similarity.
	CompanyJaccardThreshold = 0.80
)

// TitleNoise tokens are removed before comparing titles.
var TitleNoise = stringSetFrom("remote remoto hibrido hybrid presencial onsite on-site urgent urgente hiring vacante oferta job")

// LegalSuffixes are removed from the end of company names.
var LegalSuffixes = stringSetFrom("sa spa ltda limitada inc llc corp corporation ltd gmbh srl sas cia co company")

// Stopwords are removed from every token set.
var Stopwords = stringSetFrom(`
	a al algo algun alguna algunas alguno algunos ante antes aquel aquella
	aquellas aquellos aqui asi aun bajo bien cada como con contra cual cuales
	cualquier cuando de del desde donde dos el ella ellas ellos en entre era
	eramos eran eras eres es esa esas ese eso esos esta estaba estado estan
	estar estas este esto estos estoy fue fueron ha habia han hasta hay la las
	le les lo los mas me mi mis mucho muchos muy nada ni no nos nosotros o os
	otra otras otro otros para pero poco por porque que quien quienes se sea
	sean segun ser si sin sobre solo son su sus tambien tan tanto te tenemos
	tener ti tiene tienen todo todos tu tus un una uno unos usted ustedes y ya
	yo
	about above after again all also and any are because been before being
	below between both but can could did do does doing down during each few
	for from further had has have having here how into its itself just more
	most no nor not now off on once only or other our out over own same she
	should some such than that the their them then there these they this those
	through to too under until up very was we were what when where which while
	who whom why will with would you your
	empresa equipo buscamos requisitos funciones responsabilidades ofrecemos
	beneficios experiencia conocimientos trabajo puesto cargo persona
	postulante candidato candidata aplicar postular descripcion acerca
	company team role responsibilities requirements benefits experience skills
	knowledge working work position candidate apply description
`)

// TrackingParams are removed from URLs before comparison. Keys beginning with
// "utm_" are removed as well.
var TrackingParams = stringSetFrom(`
	utm_source utm_medium utm_campaign utm_term utm_content
	trk trackingid refid ref position pagenum originalsubdomain
`)

var nonAlphaNumeric = regexp.MustCompile(`[^a-z0-9+#]+`)

// accentFolding approximates NFKD plus casefold for Latin characters without
// pulling in a Unicode normalization dependency.
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

func normalize(value string) string {
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
	cleaned := nonAlphaNumeric.ReplaceAllString(builder.String(), " ")
	return strings.Join(strings.Fields(cleaned), " ")
}

func tokens(value string, noise stringSet) []string {
	result := make([]string, 0)
	for _, token := range strings.Fields(normalize(value)) {
		if len(token) < 2 || Stopwords.has(token) || (noise != nil && noise.has(token)) {
			continue
		}
		result = append(result, token)
	}
	return result
}

func companyKey(company string) string {
	parts := tokens(company, nil)
	for len(parts) > 0 && LegalSuffixes.has(parts[len(parts)-1]) {
		parts = parts[:len(parts)-1]
	}
	if len(parts) > 0 && parts[0] == "the" {
		parts = parts[1:]
	}
	return strings.Join(parts, " ")
}

func titleKey(title string) string {
	return strings.Join(tokens(title, TitleNoise), " ")
}

func descriptionTokens(description string) stringSet {
	return stringSetFromSlice(tokens(description, nil))
}

type queryPair struct {
	key   string
	value string
}

func urlKey(rawURL string) string {
	cleaned := strings.TrimSpace(rawURL)
	parsed, err := url.Parse(cleaned)
	if err != nil || (parsed.Scheme == "" && parsed.Host == "") {
		return normalize(cleaned)
	}

	path := parsed.EscapedPath()
	if parsed.Opaque != "" {
		path = parsed.Opaque
	}
	path = strings.TrimRight(path, "/")
	if path == "" {
		path = "/"
	}

	query := make([]queryPair, 0)
	for _, pair := range parseQueryPairs(parsed.RawQuery) {
		lowerKey := strings.ToLower(pair.key)
		if TrackingParams.has(lowerKey) || strings.HasPrefix(lowerKey, "utm_") {
			continue
		}
		query = append(query, pair)
	}

	var builder strings.Builder
	builder.WriteString(strings.ToLower(parsed.Scheme))
	if parsed.Host != "" {
		builder.WriteString("://")
		netloc := parsed.Host
		if parsed.User != nil {
			netloc = parsed.User.String() + "@" + netloc
		}
		builder.WriteString(strings.ToLower(netloc))
	} else if parsed.Scheme != "" {
		builder.WriteByte(':')
	}
	builder.WriteString(path)
	if len(query) > 0 {
		builder.WriteByte('?')
		for index, pair := range query {
			if index > 0 {
				builder.WriteByte('&')
			}
			builder.WriteString(pythonQueryEscape(pair.key))
			builder.WriteByte('=')
			builder.WriteString(pythonQueryEscape(pair.value))
		}
	}
	return builder.String()
}

// pythonQueryEscape matches Python's urllib.parse.quote_plus: unreserved ASCII
// characters stay intact, spaces become '+' and everything else is hex-escaped.
func pythonQueryEscape(value string) string {
	const hexDigits = "0123456789ABCDEF"
	var builder strings.Builder
	for _, b := range []byte(value) {
		switch {
		case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9',
			b == '-', b == '_', b == '.', b == '~':
			builder.WriteByte(b)
		case b == ' ':
			builder.WriteByte('+')
		default:
			builder.WriteByte('%')
			builder.WriteByte(hexDigits[b>>4])
			builder.WriteByte(hexDigits[b&0x0F])
		}
	}
	return builder.String()
}

func parseQueryPairs(rawQuery string) []queryPair {
	pairs := make([]queryPair, 0)
	for _, field := range strings.Split(rawQuery, "&") {
		if field == "" {
			continue
		}
		key, value, found := strings.Cut(field, "=")
		if !found {
			value = ""
		}
		if decoded, err := url.QueryUnescape(key); err == nil {
			key = decoded
		}
		if decoded, err := url.QueryUnescape(value); err == nil {
			value = decoded
		}
		pairs = append(pairs, queryPair{key: key, value: value})
	}
	return pairs
}
