// Package job defines the job model shared by scrapers, filtering and delivery.
package job

import "time"

// Job is the normalized representation returned by every portal scraper.
type Job struct {
	ID                  string     `json:"id"`
	Title               string     `json:"title"`
	Company             string     `json:"company"`
	Location            string     `json:"location"`
	URL                 string     `json:"url"`
	Source              string     `json:"source"`
	PublishedAt         string     `json:"published_at"`
	CompanyLogoURL      string     `json:"company_logo_url"`
	Description         string     `json:"description"`
	PublishedAtDatetime *time.Time `json:"published_at_datetime"`
}
