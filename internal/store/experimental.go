package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// TrackedJob is the metadata behind a short-lived tracking button token.
type TrackedJob struct {
	Token    string
	GuildID  int64
	JobID    string
	Source   string
	JobURL   string
	JobTitle string
	Company  string
}

// JobInteraction records a job click and its optional application answer.
type JobInteraction struct {
	ID        int64
	GuildID   int64
	UserID    int64
	JobURL    string
	JobTitle  string
	Company   string
	Source    string
	ClickedAt string
	Applied   *bool
	Notes     *string
}

// ApplicationRecord is one line in the paginated application history.
type ApplicationRecord struct {
	JobTitle  string
	Company   string
	JobURL    string
	Source    string
	ClickedAt string
	Applied   *bool
	Notes     *string
}

// ApplicationSummary holds the counters shown above the application history.
type ApplicationSummary struct {
	Clicked int64
	Applied int64
	Skipped int64
	Viewed  int64
}

// DebugReport is one note left in the tracking modal.
type DebugReport struct {
	UserID    int64
	JobTitle  string
	JobURL    string
	Source    string
	ClickedAt string
	Applied   *bool
	Notes     string
}

// SniperRanking is one row of a guild's application leaderboard.
type SniperRanking struct {
	UserID       int64
	Applications int64
	Viewed       int64
}

// StoreTrackedJob stores the metadata behind a tracking button and returns its
// freshly generated token. The clock and token generator are injectable so
// tests can pin both.
func (s *Store) StoreTrackedJob(
	ctx context.Context,
	guildID int64,
	jobID, source, jobURL, jobTitle, company string,
) (string, error) {
	if err := s.ensureGuild(ctx, guildID); err != nil {
		return "", err
	}
	if s.tokens == nil {
		return "", fmt.Errorf("store tracked job: token generator is nil")
	}
	token, err := s.tokens()
	if err != nil {
		return "", fmt.Errorf("store tracked job: %w", err)
	}
	now := s.utcNow()
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO tracked_jobs
			(token, guild_id, job_id, source, job_url, job_title, company, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		token, guildID, jobID, source, jobURL, jobTitle, company,
		now.Format("2006-01-02T15:04:05-07:00"),
	); err != nil {
		return "", fmt.Errorf("store tracked job: %w", err)
	}
	return token, nil
}

// GetTrackedJob returns one tracking token's metadata, or nil when unknown.
func (s *Store) GetTrackedJob(ctx context.Context, token string) (*TrackedJob, error) {
	tracked := &TrackedJob{}
	err := s.db.QueryRowContext(ctx, `
		SELECT token, guild_id, job_id, source, job_url, job_title, company
		FROM tracked_jobs WHERE token = ?`, token,
	).Scan(
		&tracked.Token, &tracked.GuildID, &tracked.JobID, &tracked.Source,
		&tracked.JobURL, &tracked.JobTitle, &tracked.Company,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get tracked job: %w", err)
	}
	return tracked, nil
}

// RecordJobClick registers a view and returns the interaction row id. Repeated
// clicks refresh the timestamp and metadata but preserve the answer and notes.
func (s *Store) RecordJobClick(
	ctx context.Context,
	guildID, userID int64,
	jobURL, jobTitle, company, source string,
) (int64, error) {
	if err := s.ensureGuild(ctx, guildID); err != nil {
		return 0, err
	}
	clickedAt := s.utcNow().Format("2006-01-02T15:04:05-07:00")
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO job_interactions
			(guild_id, user_id, job_url, job_title, company, source, clicked_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (guild_id, user_id, job_url) DO UPDATE SET
			clicked_at = excluded.clicked_at,
			job_title = excluded.job_title,
			company = excluded.company,
			source = excluded.source`,
		guildID, userID, jobURL, jobTitle, company, source, clickedAt,
	); err != nil {
		return 0, fmt.Errorf("record job click: %w", err)
	}
	var recordID int64
	if err := s.db.QueryRowContext(ctx, `
		SELECT id FROM job_interactions
		WHERE guild_id = ? AND user_id = ? AND job_url = ?`,
		guildID, userID, jobURL,
	).Scan(&recordID); err != nil {
		return 0, fmt.Errorf("read job interaction id: %w", err)
	}
	return recordID, nil
}

// GetJobInteraction returns one stored interaction by row id.
func (s *Store) GetJobInteraction(ctx context.Context, recordID int64) (*JobInteraction, error) {
	interaction := &JobInteraction{}
	var applied sql.NullInt64
	var notes sql.NullString
	err := s.db.QueryRowContext(ctx, `
		SELECT id, guild_id, user_id, job_url, job_title, company, source,
		       clicked_at, applied, notes
		FROM job_interactions WHERE id = ?`, recordID,
	).Scan(
		&interaction.ID, &interaction.GuildID, &interaction.UserID,
		&interaction.JobURL, &interaction.JobTitle, &interaction.Company,
		&interaction.Source, &interaction.ClickedAt, &applied, &notes,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get job interaction: %w", err)
	}
	interaction.Applied = nullableBool(applied)
	interaction.Notes = nullableString(notes)
	return interaction, nil
}

// UpdateApplicationStatus saves the Yes/No answer and optional debug notes.
func (s *Store) UpdateApplicationStatus(
	ctx context.Context, recordID int64, applied *bool, notes *string,
) (bool, error) {
	var value any
	if applied != nil {
		value = 0
		if *applied {
			value = 1
		}
	}
	var noteValue any
	if notes != nil {
		noteValue = *notes
	}
	changed, err := s.changed(ctx,
		"UPDATE job_interactions SET applied = ?, notes = ? WHERE id = ?",
		value, noteValue, recordID,
	)
	if err != nil {
		return false, fmt.Errorf("update application status: %w", err)
	}
	return changed, nil
}

// GetGuildSnipersRanking returns the top users by confirmed applications and
// views. The limit is clamped to at least one.
func (s *Store) GetGuildSnipersRanking(
	ctx context.Context, guildID int64, limit int,
) ([]SniperRanking, error) {
	if limit < 1 {
		limit = 1
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT user_id,
		       SUM(CASE WHEN applied = 1 THEN 1 ELSE 0 END) AS applications,
		       COUNT(*) AS viewed
		FROM job_interactions
		WHERE guild_id = ?
		GROUP BY user_id
		ORDER BY applications DESC, viewed DESC, user_id
		LIMIT ?`, guildID, limit)
	if err != nil {
		return nil, fmt.Errorf("query snipers ranking: %w", err)
	}
	defer rows.Close()
	result := make([]SniperRanking, 0)
	for rows.Next() {
		var row SniperRanking
		if err := rows.Scan(&row.UserID, &row.Applications, &row.Viewed); err != nil {
			return nil, fmt.Errorf("scan snipers ranking: %w", err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate snipers ranking: %w", err)
	}
	return result, nil
}

// GetGuildSnipersTotals returns confirmed applications and all recorded views.
func (s *Store) GetGuildSnipersTotals(ctx context.Context, guildID int64) (int64, int64, error) {
	var applied, viewed int64
	if err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(CASE WHEN applied = 1 THEN 1 ELSE 0 END), 0), COUNT(*)
		FROM job_interactions WHERE guild_id = ?`, guildID,
	).Scan(&applied, &viewed); err != nil {
		return 0, 0, fmt.Errorf("query snipers totals: %w", err)
	}
	return applied, viewed, nil
}

// GetUserApplicationSummary returns the four counters shown in /applications.
func (s *Store) GetUserApplicationSummary(
	ctx context.Context, guildID, userID int64,
) (ApplicationSummary, error) {
	var summary ApplicationSummary
	if err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN applied = 1 THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN applied = 0 THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN applied IS NULL THEN 1 ELSE 0 END), 0)
		FROM job_interactions WHERE guild_id = ? AND user_id = ?`,
		guildID, userID,
	).Scan(&summary.Clicked, &summary.Applied, &summary.Skipped, &summary.Viewed); err != nil {
		return ApplicationSummary{}, fmt.Errorf("query application summary: %w", err)
	}
	return summary, nil
}

// GetUserApplications returns one page of a user's history, newest first.
func (s *Store) GetUserApplications(
	ctx context.Context, guildID, userID int64, limit, offset int,
) ([]ApplicationRecord, error) {
	if limit < 1 {
		limit = 1
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT job_title, company, job_url, source, clicked_at, applied, notes
		FROM job_interactions
		WHERE guild_id = ? AND user_id = ?
		ORDER BY clicked_at DESC, id DESC
		LIMIT ? OFFSET ?`, guildID, userID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("query user applications: %w", err)
	}
	defer rows.Close()
	result := make([]ApplicationRecord, 0)
	for rows.Next() {
		var record ApplicationRecord
		var applied sql.NullInt64
		var notes sql.NullString
		if err := rows.Scan(
			&record.JobTitle, &record.Company, &record.JobURL, &record.Source,
			&record.ClickedAt, &applied, &notes,
		); err != nil {
			return nil, fmt.Errorf("scan user application: %w", err)
		}
		record.Applied = nullableBool(applied)
		record.Notes = nullableString(notes)
		result = append(result, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate user applications: %w", err)
	}
	return result, nil
}

// GetDebugReportCount counts non-empty notes, optionally for one portal.
func (s *Store) GetDebugReportCount(
	ctx context.Context, guildID int64, source *string,
) (int64, error) {
	query, args := debugQuery(guildID, source, false)
	var count int64
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, fmt.Errorf("count debug reports: %w", err)
	}
	return count, nil
}

// GetDebugReports returns one page of non-empty notes, newest first.
func (s *Store) GetDebugReports(
	ctx context.Context, guildID int64, source *string, limit, offset int,
) ([]DebugReport, error) {
	if limit < 1 {
		limit = 1
	}
	if offset < 0 {
		offset = 0
	}
	query, args := debugQuery(guildID, source, true)
	args = append(args, limit, offset)
	query += " ORDER BY clicked_at DESC, id DESC LIMIT ? OFFSET ?"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query debug reports: %w", err)
	}
	defer rows.Close()
	result := make([]DebugReport, 0)
	for rows.Next() {
		var report DebugReport
		var applied sql.NullInt64
		var notes sql.NullString
		if err := rows.Scan(
			&report.UserID, &report.JobTitle, &report.JobURL, &report.Source,
			&report.ClickedAt, &applied, &notes,
		); err != nil {
			return nil, fmt.Errorf("scan debug report: %w", err)
		}
		report.Applied = nullableBool(applied)
		report.Notes = strings.TrimSpace(notes.String)
		result = append(result, report)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate debug reports: %w", err)
	}
	return result, nil
}

func debugQuery(guildID int64, source *string, withFields bool) (string, []any) {
	selectClause := "SELECT COUNT(*)"
	if withFields {
		selectClause = "SELECT user_id, job_title, job_url, source, clicked_at, applied, notes"
	}
	query := selectClause + ` FROM job_interactions
		WHERE guild_id = ? AND notes IS NOT NULL AND TRIM(notes) != ''`
	args := []any{guildID}
	if source != nil {
		value := strings.TrimSpace(*source)
		if value != "" {
			query += " AND LOWER(source) = LOWER(?)"
			args = append(args, value)
		}
	}
	return query, args
}

func (s *Store) utcNow() time.Time {
	if s.now == nil {
		return time.Now().UTC()
	}
	return s.now().UTC()
}

func nullableBool(value sql.NullInt64) *bool {
	if !value.Valid {
		return nil
	}
	result := value.Int64 != 0
	return &result
}

func nullableString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}
