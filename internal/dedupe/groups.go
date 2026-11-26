package dedupe

import (
	"strings"

	"github.com/NikoMalek/job-sniper-discord-bot/internal/job"
)

type stringSet map[string]struct{}

func stringSetFrom(words string) stringSet {
	return stringSetFromSlice(strings.Fields(words))
}

func stringSetFromSlice(values []string) stringSet {
	set := make(stringSet, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

func (s stringSet) has(value string) bool {
	_, ok := s[value]
	return ok
}

func intersection(left, right stringSet) stringSet {
	result := make(stringSet)
	for value := range left {
		if right.has(value) {
			result[value] = struct{}{}
		}
	}
	return result
}

func setUnion(left, right stringSet) stringSet {
	result := make(stringSet, len(left)+len(right))
	for value := range left {
		result[value] = struct{}{}
	}
	for value := range right {
		result[value] = struct{}{}
	}
	return result
}

func jaccard(left, right stringSet) float64 {
	if len(left) == 0 || len(right) == 0 {
		return 0.0
	}
	return float64(len(intersection(left, right))) / float64(len(setUnion(left, right)))
}

func containment(left, right stringSet) float64 {
	if len(left) == 0 || len(right) == 0 {
		return 0.0
	}
	smaller := len(left)
	if len(right) < smaller {
		smaller = len(right)
	}
	return float64(len(intersection(left, right))) / float64(smaller)
}

func companiesMatch(left, right string) bool {
	if left == "" || right == "" {
		return false
	}
	if left == right {
		return true
	}

	shorter, longer := left, right
	if len(right) < len(left) {
		shorter, longer = right, left
	}
	if len(shorter) >= 3 && containsWholeWord(longer, shorter) {
		return true
	}
	return jaccard(stringSetFrom(left), stringSetFrom(right)) >= CompanyJaccardThreshold
}

func containsWholeWord(longer, shorter string) bool {
	offset := 0
	for offset+len(shorter) <= len(longer) {
		index := strings.Index(longer[offset:], shorter)
		if index < 0 {
			return false
		}
		start := offset + index
		end := start + len(shorter)
		beforeOK := start == 0 || !isWordByte(longer[start-1])
		afterOK := end == len(longer) || !isWordByte(longer[end])
		if beforeOK && afterOK {
			return true
		}
		offset = start + 1
	}
	return false
}

func isWordByte(value byte) bool {
	return value == '_' ||
		(value >= 'a' && value <= 'z') ||
		(value >= 'A' && value <= 'Z') ||
		(value >= '0' && value <= '9')
}

type fingerprint struct {
	companyKey        string
	titleKey          string
	titleTokens       stringSet
	descriptionTokens stringSet
	urlKey            string
}

func newFingerprint(item job.Job) fingerprint {
	return fingerprint{
		companyKey:        companyKey(item.Company),
		titleKey:          titleKey(item.Title),
		titleTokens:       stringSetFromSlice(tokens(item.Title, TitleNoise)),
		descriptionTokens: descriptionTokens(item.Description),
		urlKey:            urlKey(item.URL),
	}
}

func isDuplicate(left, right fingerprint) bool {
	if left.urlKey != "" && left.urlKey == right.urlKey {
		return true
	}
	if !companiesMatch(left.companyKey, right.companyKey) {
		return false
	}

	titleExact := left.titleKey != "" && left.titleKey == right.titleKey
	if !titleExact && jaccard(left.titleTokens, right.titleTokens) < TitleJaccardThreshold {
		return false
	}

	if len(left.descriptionTokens) == 0 || len(right.descriptionTokens) == 0 {
		return titleExact
	}

	descriptionContainment := containment(left.descriptionTokens, right.descriptionTokens)
	if descriptionContainment < DescriptionContainmentThreshold {
		return false
	}
	descriptionJaccard := jaccard(left.descriptionTokens, right.descriptionTokens)
	if descriptionJaccard < DescriptionJaccardThreshold {
		return false
	}
	if titleExact {
		return true
	}
	return descriptionContainment >= StrongContainmentThreshold &&
		descriptionJaccard >= StrongJaccardThreshold
}

// GroupJobs groups equivalent jobs while preserving input order. The first
// job of each group is the primary variant.
func GroupJobs(jobs []job.Job) [][]job.Job {
	items := append([]job.Job(nil), jobs...)
	if len(items) < 2 {
		if len(items) == 0 {
			return [][]job.Job{}
		}
		return [][]job.Job{items}
	}

	fingerprints := make([]fingerprint, len(items))
	for index, item := range items {
		fingerprints[index] = newFingerprint(item)
	}
	parent := make([]int, len(items))
	for index := range parent {
		parent[index] = index
	}

	var find func(int) int
	find = func(index int) int {
		for parent[index] != index {
			parent[index] = parent[parent[index]]
			index = parent[index]
		}
		return index
	}
	union := func(left, right int) {
		leftRoot, rightRoot := find(left), find(right)
		if leftRoot != rightRoot {
			parent[rightRoot] = leftRoot
		}
	}

	for left := 0; left < len(items); left++ {
		for right := left + 1; right < len(items); right++ {
			if isDuplicate(fingerprints[left], fingerprints[right]) {
				union(left, right)
			}
		}
	}

	groups := make(map[int][]job.Job)
	order := make([]int, 0)
	for index, item := range items {
		root := find(index)
		if _, ok := groups[root]; !ok {
			order = append(order, root)
		}
		groups[root] = append(groups[root], item)
	}

	result := make([][]job.Job, 0, len(order))
	for _, root := range order {
		result = append(result, groups[root])
	}
	return result
}
