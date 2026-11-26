package data

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Country is one entry in the country catalog.
type Country struct {
	Code   string
	NameES string
	NameEN string
}

type countriesPayload struct {
	Countries []struct {
		Code   string `json:"code"`
		NameES string `json:"name_es"`
		NameEN string `json:"name_en"`
	} `json:"countries"`
	Aliases map[string]string `json:"aliases"`
}

func parseCountries(raw []byte) ([]Country, map[string]string, error) {
	var payload countriesPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", countriesFile, err)
	}

	countries := make([]Country, 0, len(payload.Countries))
	counts := make(map[string]int, len(payload.Countries))
	for _, item := range payload.Countries {
		country := Country{
			Code:   upper(item.Code),
			NameES: item.NameES,
			NameEN: item.NameEN,
		}
		countries = append(countries, country)
		counts[country.Code]++
	}

	var duplicates []string
	for code, count := range counts {
		if count > 1 {
			duplicates = append(duplicates, code)
		}
	}
	sort.Strings(duplicates)
	if len(duplicates) > 0 {
		return nil, nil, fmt.Errorf(
			"%s: duplicate country codes: %s", countriesFile, strings.Join(duplicates, ", "),
		)
	}

	aliases := make(map[string]string, len(payload.Aliases))
	for alias, code := range payload.Aliases {
		aliases[alias] = upper(code)
	}

	unknownSet := make(map[string]struct{})
	for _, code := range aliases {
		if _, ok := counts[code]; !ok {
			unknownSet[code] = struct{}{}
		}
	}
	unknown := make([]string, 0, len(unknownSet))
	for code := range unknownSet {
		unknown = append(unknown, code)
	}
	sort.Strings(unknown)
	if len(unknown) > 0 {
		return nil, nil, fmt.Errorf(
			"%s: aliases point to unknown countries: %s", countriesFile, strings.Join(unknown, ", "),
		)
	}

	return countries, aliases, nil
}

func upper(value string) string {
	return strings.ToUpper(value)
}
