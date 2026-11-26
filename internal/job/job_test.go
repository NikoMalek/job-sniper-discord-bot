package job

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"
)

func TestJobJSONTags(t *testing.T) {
	published := time.Date(2026, time.November, 25, 12, 30, 0, 0, time.UTC)
	original := Job{
		ID:                  "job-1",
		Title:               "Go Developer",
		Company:             "Acme",
		Location:            "Santiago, Chile",
		URL:                 "https://example.com/jobs/1",
		Source:              "getonbrd",
		PublishedAt:         "Hace 2 días",
		CompanyLogoURL:      "https://example.com/logo.png",
		Description:         "Remote role",
		PublishedAtDatetime: &published,
	}

	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal() unexpected error: %v", err)
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("json.Unmarshal() unexpected error: %v", err)
	}
	wantKeys := []string{
		"id", "title", "company", "location", "url", "source", "published_at",
		"company_logo_url", "description", "published_at_datetime",
	}
	if len(payload) != len(wantKeys) {
		t.Fatalf("JSON has %d keys, want %d: %s", len(payload), len(wantKeys), encoded)
	}
	for _, key := range wantKeys {
		if _, ok := payload[key]; !ok {
			t.Errorf("JSON is missing key %q: %s", key, encoded)
		}
	}

	var decoded Job
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() into Job unexpected error: %v", err)
	}
	if !reflect.DeepEqual(decoded, original) {
		t.Fatalf("round trip = %+v, want %+v", decoded, original)
	}
}

func TestJobZeroValues(t *testing.T) {
	var decoded Job
	if err := json.Unmarshal([]byte(`{"id":"x","title":"t","company":"c","location":"l","url":"u","source":"s","published_at":"p"}`), &decoded); err != nil {
		t.Fatalf("json.Unmarshal() unexpected error: %v", err)
	}
	if decoded.CompanyLogoURL != "" || decoded.Description != "" || decoded.PublishedAtDatetime != nil {
		t.Fatalf("optional fields are not zero: %+v", decoded)
	}
}
