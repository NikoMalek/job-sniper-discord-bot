package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func pinTrackingClockAndToken(st *Store) {
	st.tokens = func() (string, error) { return "token-1", nil }
	st.now = func() time.Time {
		return time.Date(2026, 11, 27, 10, 30, 0, 0, time.UTC)
	}
}

func TestTrackedJobAndJobInteractionRoundTrip(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	pinTrackingClockAndToken(st)

	token, err := st.StoreTrackedJob(
		ctx, guild, "job-1", "LinkedIn", "https://example.com/job-1", "Developer", "Acme",
	)
	if err != nil || token != "token-1" {
		t.Fatalf("StoreTrackedJob() = %q, %v", token, err)
	}
	tracked, err := st.GetTrackedJob(ctx, token)
	if err != nil {
		t.Fatalf("GetTrackedJob() error = %v", err)
	}
	wantTracked := &TrackedJob{
		Token: token, GuildID: guild, JobID: "job-1", Source: "LinkedIn",
		JobURL: "https://example.com/job-1", JobTitle: "Developer", Company: "Acme",
	}
	if !reflect.DeepEqual(tracked, wantTracked) {
		t.Fatalf("GetTrackedJob() = %#v, want %#v", tracked, wantTracked)
	}
	if unknown, err := st.GetTrackedJob(ctx, "missing"); err != nil || unknown != nil {
		t.Fatalf("GetTrackedJob(missing) = %#v, %v", unknown, err)
	}

	recordID, err := st.RecordJobClick(
		ctx, guild, 7, "https://example.com/job-1", "Developer", "Acme", "LinkedIn",
	)
	if err != nil {
		t.Fatalf("RecordJobClick() error = %v", err)
	}
	applied := true
	notes := "  wrong language  "
	changed, err := st.UpdateApplicationStatus(ctx, recordID, &applied, &notes)
	if err != nil || !changed {
		t.Fatalf("UpdateApplicationStatus() = %v, %v", changed, err)
	}

	secondID, err := st.RecordJobClick(
		ctx, guild, 7, "https://example.com/job-1", "Senior Developer", "Acme", "LinkedIn",
	)
	if err != nil || secondID != recordID {
		t.Fatalf("repeated RecordJobClick() = %d, %v; want %d", secondID, err, recordID)
	}
	row, err := st.GetJobInteraction(ctx, recordID)
	if err != nil || row == nil {
		t.Fatalf("GetJobInteraction() = %#v, %v", row, err)
	}
	if row.JobTitle != "Senior Developer" {
		t.Errorf("JobTitle = %q, want refreshed title", row.JobTitle)
	}
	if row.Applied == nil || !*row.Applied {
		t.Errorf("Applied = %v, want true after a repeated click", row.Applied)
	}
	if row.Notes == nil || *row.Notes != "  wrong language  " {
		t.Errorf("Notes = %v, want preserved notes", row.Notes)
	}
	if row.ClickedAt != "2026-11-27T10:30:00+00:00" {
		t.Errorf("ClickedAt = %q, want injected clock", row.ClickedAt)
	}
}

func TestExperimentalRankingsSummariesAndDebugReports(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	pinTrackingClockAndToken(st)

	type click struct {
		user    int64
		jobID   string
		title   string
		applied *bool
		notes   *string
	}
	yes := true
	no := false
	badLink := "  broken link  "
	clicks := []click{
		{7, "a", "Job A", &yes, &badLink},
		{7, "b", "Job B", nil, nil},
		{8, "c", "Job C", &no, nil},
	}
	for _, row := range clicks {
		if _, err := st.RecordJobClick(
			ctx, guild, row.user, "https://example.com/"+row.jobID,
			row.title, "Acme", "LinkedIn",
		); err != nil {
			t.Fatalf("RecordJobClick(%s): %v", row.jobID, err)
		}
		if row.applied == nil && row.notes == nil {
			continue
		}
		if _, err := st.db.ExecContext(ctx,
			"UPDATE job_interactions SET applied = ?, notes = ? WHERE guild_id = ? AND user_id = ? AND job_url = ?",
			boolToValue(row.applied), row.notes, guild, row.user, "https://example.com/"+row.jobID,
		); err != nil {
			t.Fatalf("seed application status: %v", err)
		}
	}

	ranking, err := st.GetGuildSnipersRanking(ctx, guild, 10)
	if err != nil {
		t.Fatalf("GetGuildSnipersRanking() error = %v", err)
	}
	wantRanking := []SniperRanking{
		{UserID: 7, Applications: 1, Viewed: 2},
		{UserID: 8, Applications: 0, Viewed: 1},
	}
	if !reflect.DeepEqual(ranking, wantRanking) {
		t.Fatalf("ranking = %#v, want %#v", ranking, wantRanking)
	}
	applied, viewed, err := st.GetGuildSnipersTotals(ctx, guild)
	if err != nil || applied != 1 || viewed != 3 {
		t.Fatalf("totals = %d, %d, %v", applied, viewed, err)
	}
	summary, err := st.GetUserApplicationSummary(ctx, guild, 7)
	if err != nil {
		t.Fatalf("GetUserApplicationSummary() error = %v", err)
	}
	wantSummary := ApplicationSummary{Clicked: 2, Applied: 1, Skipped: 0, Viewed: 1}
	if !reflect.DeepEqual(summary, wantSummary) {
		t.Fatalf("summary = %#v, want %#v", summary, wantSummary)
	}

	applications, err := st.GetUserApplications(ctx, guild, 7, 5, 0)
	if err != nil || len(applications) != 2 {
		t.Fatalf("applications = %#v, %v", applications, err)
	}
	if applications[0].JobTitle != "Job B" {
		t.Errorf("newest application = %q, want Job B", applications[0].JobTitle)
	}
	source := "linkedin"
	count, err := st.GetDebugReportCount(ctx, guild, &source)
	if err != nil || count != 1 {
		t.Fatalf("debug count = %d, %v, want 1", count, err)
	}
	reports, err := st.GetDebugReports(ctx, guild, &source, 5, 0)
	if err != nil || len(reports) != 1 {
		t.Fatalf("debug reports = %#v, %v", reports, err)
	}
	if reports[0].Notes != "broken link" {
		t.Errorf("debug notes = %q, want trimmed notes", reports[0].Notes)
	}
	otherSource := "LinkedIn "
	if count, err := st.GetDebugReportCount(ctx, guild, &otherSource); err != nil || count != 1 {
		t.Errorf("trimmed source count = %d, %v, want 1", count, err)
	}
}

func TestStoreTrackedJobTokenFailure(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	wantErr := errors.New("random source failed")
	st.tokens = func() (string, error) { return "", wantErr }
	if _, err := st.StoreTrackedJob(ctx, guild, "job", "LinkedIn", "url", "title", "company"); !errors.Is(err, wantErr) {
		t.Fatalf("StoreTrackedJob() error = %v, want wrapped token error", err)
	}
}

func boolToValue(value *bool) any {
	if value == nil {
		return nil
	}
	if *value {
		return 1
	}
	return 0
}
