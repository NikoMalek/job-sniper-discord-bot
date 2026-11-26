package data

import (
	"encoding/json"
	"fmt"
)

func parseKeywordAliases(raw []byte) (map[string][]string, error) {
	var payload map[string][]string
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("parse %s: %w", keywordAliasesFile, err)
	}

	aliases := make(map[string][]string, len(payload))
	for keyword, variants := range payload {
		aliases[keyword] = append([]string(nil), variants...)
	}
	return aliases, nil
}

func parseBlacklistGroups(raw []byte) ([][]string, error) {
	var payload [][]string
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("parse %s: %w", blacklistKeywordAliasGroupsFile, err)
	}

	groups := make([][]string, len(payload))
	for index, group := range payload {
		groups[index] = append([]string(nil), group...)
	}
	return groups, nil
}
