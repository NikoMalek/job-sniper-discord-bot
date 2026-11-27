package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanupTrackedJobsAt(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer st.Close()

	if err := st.ensureGuild(ctx, 1); err != nil {
		t.Fatalf("ensure guild: %v", err)
	}
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	rows := []struct {
		token string
		stamp string
	}{
		{token: "old", stamp: "2026-11-26T10:59:59+00:00"},
		{token: "boundary", stamp: "2026-11-26T11:00:00+00:00"},
		{token: "recent", stamp: "2026-11-26T11:00:01+00:00"},
	}
	for _, row := range rows {
		if _, err := st.db.ExecContext(ctx, `
			INSERT INTO tracked_jobs
			(token, guild_id, job_id, source, job_url, job_title, company, created_at)
			VALUES (?, 1, ?, 'LinkedIn', 'https://example.com', 'Job', 'Acme', ?)`,
			row.token, row.token, row.stamp,
		); err != nil {
			t.Fatalf("insert tracked job: %v", err)
		}
	}

	removed, err := st.CleanupTrackedJobsAt(ctx, now, 25)
	if err != nil {
		t.Fatalf("CleanupTrackedJobsAt() error = %v", err)
	}
	if removed != 1 {
		t.Fatalf("CleanupTrackedJobsAt() removed = %d, want 1", removed)
	}
	var remaining int
	if err := st.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM tracked_jobs WHERE token IN ('boundary', 'recent')",
	).Scan(&remaining); err != nil {
		t.Fatalf("count remaining: %v", err)
	}
	if remaining != 2 {
		t.Fatalf("remaining tracked jobs = %d, want 2", remaining)
	}
}

func TestCleanupTrackedJobsRejectsMultipleWindows(t *testing.T) {
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer st.Close()
	if _, err := st.CleanupTrackedJobs(ctx, 1, 2); err == nil {
		t.Fatal("CleanupTrackedJobs() with two windows error = nil, want error")
	}
}
