package data

import (
	"encoding/json"
	"fmt"
)

// ChileanCity is one city or commune exposed by ChileTrabajos.
type ChileanCity struct {
	Name    string
	Bucket  string
	Region  string
	Aliases []string
}

func parseChileCities(raw []byte) ([]ChileanCity, error) {
	var payload []struct {
		Name    string   `json:"name"`
		Bucket  string   `json:"bucket"`
		Region  string   `json:"region"`
		Aliases []string `json:"aliases"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("parse %s: %w", chileCitiesFile, err)
	}

	cities := make([]ChileanCity, 0, len(payload))
	for _, item := range payload {
		cities = append(cities, ChileanCity{
			Name:    item.Name,
			Bucket:  item.Bucket,
			Region:  item.Region,
			Aliases: append([]string(nil), item.Aliases...),
		})
	}
	return cities, nil
}
