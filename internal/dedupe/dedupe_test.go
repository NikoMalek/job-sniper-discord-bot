package dedupe

import (
	"reflect"
	"testing"

	"github.com/NikoMalek/job-sniper-discord-bot/internal/job"
)

const description = "Buscamos desarrollador backend con experiencia en Python, FastAPI y " +
	"PostgreSQL para construir servicios de pagos en la nube AWS, con " +
	"pruebas automatizadas, integracion continua y despliegues en contenedores."

func makeJob(jobID string, title, company, source, rawURL, jobDescription string) job.Job {
	if title == "" {
		title = "Backend Developer Python"
	}
	if company == "" {
		company = "Acme"
	}
	if source == "" {
		source = "LinkedIn"
	}
	if rawURL == "" {
		rawURL = "https://example.com/" + source + "/" + jobID
	}
	return job.Job{
		ID:          jobID,
		Title:       title,
		Company:     company,
		Location:    "Santiago, Chile",
		URL:         rawURL,
		Source:      source,
		PublishedAt: "hace 1 hora",
		Description: jobDescription,
	}
}

func ids(groups [][]job.Job) [][]string {
	result := make([][]string, len(groups))
	for index, group := range groups {
		result[index] = make([]string, len(group))
		for member, item := range group {
			result[index][member] = item.ID
		}
	}
	return result
}

func assertGroups(t *testing.T, got [][]job.Job, want [][]string) {
	t.Helper()
	if actual := ids(got); !reflect.DeepEqual(actual, want) {
		t.Fatalf("GroupJobs() groups = %v, want %v", actual, want)
	}
}

func TestEmptyAndSingle(t *testing.T) {
	if got := GroupJobs(nil); len(got) != 0 {
		t.Fatalf("GroupJobs(nil) = %v, want no groups", ids(got))
	}

	item := makeJob("1", "", "", "", "", "")
	assertGroups(t, GroupJobs([]job.Job{item}), [][]string{{"1"}})
}

func TestSameURLIgnoringTrackingParamsIsGrouped(t *testing.T) {
	first := makeJob("1", "", "", "", "https://example.com/job/1?utm_source=x&trk=abc", "")
	second := makeJob("2", "", "", "Get on Board", "https://example.com/job/1/", "")
	assertGroups(t, GroupJobs([]job.Job{first, second}), [][]string{{"1", "2"}})
}

func TestSameJobOnTwoPortalsIsGrouped(t *testing.T) {
	first := makeJob("1", "", "Acme SpA", "", "", description)
	second := makeJob("2", "", "Acme", "Computrabajo", "", description)
	assertGroups(t, GroupJobs([]job.Job{first, second}), [][]string{{"1", "2"}})
}

func TestSameTitleAndCompanyWithoutDescriptionIsGrouped(t *testing.T) {
	first := makeJob("1", "", "", "", "", "")
	second := makeJob("2", "Backend Developer Python (Remoto)", "", "Computrabajo", "", "")
	assertGroups(t, GroupJobs([]job.Job{first, second}), [][]string{{"1", "2"}})
}

func TestDifferentCompaniesAreNotGrouped(t *testing.T) {
	first := makeJob("1", "", "Acme", "", "", description)
	second := makeJob("2", "", "Globex", "Computrabajo", "", description)
	assertGroups(t, GroupJobs([]job.Job{first, second}), [][]string{{"1"}, {"2"}})
}

func TestDifferentTitlesAreNotGrouped(t *testing.T) {
	first := makeJob("1", "", "", "", "", "")
	second := makeJob("2", "Diseñadora UX Senior", "", "Computrabajo", "", "")
	assertGroups(t, GroupJobs([]job.Job{first, second}), [][]string{{"1"}, {"2"}})
}

func TestInputOrderIsPreservedAndFirstJobIsPrimary(t *testing.T) {
	first := makeJob("1", "", "", "", "", "")
	other := makeJob("2", "Analista de datos", "Globex", "", "", "")
	duplicate := makeJob("3", "", "", "Computrabajo", "", "")
	groups := GroupJobs([]job.Job{first, other, duplicate})
	assertGroups(t, groups, [][]string{{"1", "3"}, {"2"}})
	if groups[0][0].ID != "1" {
		t.Fatalf("primary variant = %q, want 1", groups[0][0].ID)
	}
}

func TestTransitiveDuplicatesFormOneGroup(t *testing.T) {
	first := makeJob("1", "", "", "", "", "")
	second := makeJob("2", "", "", "Computrabajo", "", "")
	third := makeJob("3", "", "", "Get on Board", "", "")
	assertGroups(t, GroupJobs([]job.Job{first, second, third}), [][]string{{"1", "2", "3"}})
}

func TestConstantsMatchPython(t *testing.T) {
	tests := []struct {
		name string
		got  float64
		want float64
	}{
		{"TitleJaccardThreshold", TitleJaccardThreshold, 0.75},
		{"DescriptionContainmentThreshold", DescriptionContainmentThreshold, 0.60},
		{"DescriptionJaccardThreshold", DescriptionJaccardThreshold, 0.30},
		{"StrongContainmentThreshold", StrongContainmentThreshold, 0.70},
		{"StrongJaccardThreshold", StrongJaccardThreshold, 0.40},
		{"CompanyJaccardThreshold", CompanyJaccardThreshold, 0.80},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = %v, want %v", tt.name, tt.got, tt.want)
		}
	}

	if len(TitleNoise) != 13 || len(LegalSuffixes) != 15 || len(Stopwords) != 262 || len(TrackingParams) != 12 {
		t.Errorf("set sizes = %d/%d/%d/%d, want 13/15/262/12",
			len(TitleNoise), len(LegalSuffixes), len(Stopwords), len(TrackingParams))
	}
	for _, value := range []string{"remote", "remoto", "hibrido", "hybrid", "presencial", "onsite", "on-site", "urgent", "urgente", "hiring", "vacante", "oferta", "job"} {
		if !TitleNoise.has(value) {
			t.Errorf("TitleNoise is missing %q", value)
		}
	}
	for _, value := range []string{"utm_source", "trk", "trackingid", "refid", "ref", "position", "pagenum", "originalsubdomain"} {
		if !TrackingParams.has(value) {
			t.Errorf("TrackingParams is missing %q", value)
		}
	}
	if !Stopwords.has("de") || !Stopwords.has("experience") || !Stopwords.has("responsibilities") {
		t.Error("Stopwords is missing a copied term")
	}
}

func TestNormalize(t *testing.T) {
	tests := []struct {
		value string
		want  string
	}{
		{value: "  Ingeniero   de Software  ", want: "ingeniero de software"},
		{value: "Niño+A", want: "nino+a"},
		{value: "Straße", want: "strasse"},
		{value: "Åland æ ø", want: "aland"},
		{value: "C# Junior", want: "c# junior"},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			if got := normalize(tt.value); got != tt.want {
				t.Fatalf("normalize(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestTokens(t *testing.T) {
	got := tokens("Backend Developer Python Remoto y la", TitleNoise)
	want := []string{"backend", "developer", "python"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("tokens() = %v, want %v", got, want)
	}
}

func TestCompanyKey(t *testing.T) {
	tests := []struct {
		value string
		want  string
	}{
		{value: "Acme SpA", want: "acme"},
		{value: "The Acme Corporation", want: "acme"},
		{value: "Acme Limitada S.A.", want: "acme"},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			if got := companyKey(tt.value); got != tt.want {
				t.Fatalf("companyKey(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestURLKey(t *testing.T) {
	tests := []struct {
		value string
		want  string
	}{
		{
			value: "https://Example.com/Job/1/?UTM_Source=x&trk=abc&q=a b",
			want:  "https://example.com/Job/1?q=a+b",
		},
		{value: " https://example.com/job/1#section ", want: "https://example.com/job/1"},
		{value: "not a url", want: "not a url"},
		{value: "mailto:foo@bar", want: "mailto:foo@bar"},
		{value: "https://example.com/?a=~&b=x+y", want: "https://example.com/?a=~&b=x+y"},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			if got := urlKey(tt.value); got != tt.want {
				t.Fatalf("urlKey(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestSimilarityHelpers(t *testing.T) {
	left := stringSetFrom("a b c")
	right := stringSetFrom("b c d")
	if got := jaccard(left, right); got != 2.0/4.0 {
		t.Errorf("jaccard() = %v, want 0.5", got)
	}
	if got := containment(left, right); got != 2.0/3.0 {
		t.Errorf("containment() = %v, want 2/3", got)
	}
	if got := jaccard(left, stringSetFrom("")); got != 0.0 {
		t.Errorf("jaccard() with empty set = %v, want 0", got)
	}
}

func TestCompaniesMatch(t *testing.T) {
	tests := []struct {
		left  string
		right string
		want  bool
	}{
		{left: "acme", right: "acme", want: true},
		{left: "acme", right: "acme holdings", want: true},
		{left: "ac", right: "acme", want: false},
		{left: "", right: "acme", want: false},
		{left: "alpha beta", right: "alpha gamma", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.left+"|"+tt.right, func(t *testing.T) {
			if got := companiesMatch(tt.left, tt.right); got != tt.want {
				t.Fatalf("companiesMatch(%q, %q) = %v, want %v", tt.left, tt.right, got, tt.want)
			}
		})
	}
}
