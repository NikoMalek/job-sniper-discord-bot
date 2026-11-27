package store

import (
	"context"
	"fmt"
	"strconv"
	"time"
)

// NotifiedKey identifies one already-notified job variant inside a guild.
type NotifiedKey struct {
	Source string
	JobID  string
}

// MarkNotified records a notified job variant and reports whether it was new.
func (s *Store) MarkNotified(ctx context.Context, guildID int64, jobID, source string) (bool, error) {
	if err := s.ensureGuild(ctx, guildID); err != nil {
		return false, err
	}
	return s.changed(ctx, `
		INSERT OR IGNORE INTO notified_jobs (guild_id, job_id, source)
		VALUES (?, ?, ?)`, guildID, jobID, source)
}

// RemoveNotified removes one notified job variant.
func (s *Store) RemoveNotified(ctx context.Context, guildID int64, jobID, source string) error {
	if _, err := s.db.ExecContext(ctx, `
		DELETE FROM notified_jobs
		WHERE guild_id = ? AND job_id = ? AND source = ?`,
		guildID, jobID, source,
	); err != nil {
		return fmt.Errorf("remove notified job: %w", err)
	}
	return nil
}

// GetNotifiedKeys returns every notified (source, job_id) pair for a guild.
func (s *Store) GetNotifiedKeys(ctx context.Context, guildID int64) (map[NotifiedKey]struct{}, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT source, job_id FROM notified_jobs WHERE guild_id = ?", guildID)
	if err != nil {
		return nil, fmt.Errorf("query notified keys: %w", err)
	}
	defer rows.Close()

	result := make(map[NotifiedKey]struct{})
	for rows.Next() {
		var key NotifiedKey
		if err := rows.Scan(&key.Source, &key.JobID); err != nil {
			return nil, fmt.Errorf("scan notified key: %w", err)
		}
		result[key] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate notified keys: %w", err)
	}
	return result, nil
}

// CountNotified returns the number of notified variants for a guild.
func (s *Store) CountNotified(ctx context.Context, guildID int64) (int64, error) {
	var count int64
	if err := s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM notified_jobs WHERE guild_id = ?", guildID,
	).Scan(&count); err != nil {
		return 0, fmt.Errorf("count notified jobs: %w", err)
	}
	return count, nil
}

// CleanupNotifiedJobs deletes notification history older than hours. When
// hours is omitted, NotifiedRetentionHours is used.
func (s *Store) CleanupNotifiedJobs(ctx context.Context, hours ...float64) (int64, error) {
	if len(hours) > 1 {
		return 0, fmt.Errorf("cleanup notified jobs: expected at most one hours argument")
	}
	window := NotifiedRetentionHours
	if len(hours) == 1 {
		window = hours[0]
	}
	modifier := "-" + strconv.FormatFloat(window, 'g', 6, 64) + " hours"
	result, err := s.db.ExecContext(ctx,
		"DELETE FROM notified_jobs WHERE created_at < datetime('now', ?)", modifier)
	if err != nil {
		return 0, fmt.Errorf("cleanup notified jobs: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read cleaned notified count: %w", err)
	}
	return count, nil
}

// CleanupTrackedJobs deletes tracking tokens older than hours using the
// current UTC time. The default window matches Python's retention setting.
func (s *Store) CleanupTrackedJobs(ctx context.Context, hours ...float64) (int64, error) {
	return s.CleanupTrackedJobsAt(ctx, time.Now().UTC(), hours...)
}

// CleanupTrackedJobsAt deletes tracking tokens older than hours relative to
// now. It mirrors Python's ISO-8601 cutoff with second precision and is used
// by tests that need a deterministic clock.
func (s *Store) CleanupTrackedJobsAt(
	ctx context.Context, now time.Time, hours ...float64,
) (int64, error) {
	if len(hours) > 1 {
		return 0, fmt.Errorf("cleanup tracked jobs: expected at most one hours argument")
	}
	window := NotifiedRetentionHours
	if len(hours) == 1 {
		window = hours[0]
	}
	cutoff := now.Add(-time.Duration(window * float64(time.Hour))).UTC()
	stamp := cutoff.Format("2006-01-02T15:04:05-07:00")
	result, err := s.db.ExecContext(ctx,
		"DELETE FROM tracked_jobs WHERE created_at < ?", stamp)
	if err != nil {
		return 0, fmt.Errorf("cleanup tracked jobs: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read cleaned tracked count: %w", err)
	}
	return count, nil
}
