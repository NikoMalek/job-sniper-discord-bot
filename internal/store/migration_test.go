package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeLegacyDB(t *testing.T, path string, maxAgeColumn bool) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() unexpected error: %v", err)
	}
	db, err := sql.Open("sqlite", path+"?_foreign_keys=1&_journal_mode=WAL")
	if err != nil {
		t.Fatalf("sql.Open() unexpected error: %v", err)
	}
	defer db.Close()

	maxAgeDDL := ""
	if maxAgeColumn {
		maxAgeDDL = "max_age_hours REAL NOT NULL DEFAULT 24.0,"
	}
	schema := fmt.Sprintf(`
		CREATE TABLE guild_configs (
			guild_id INTEGER PRIMARY KEY,
			channel_id INTEGER,
			location TEXT NOT NULL DEFAULT '',
			%s
			setup_message_sent INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE guild_keywords (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			guild_id INTEGER NOT NULL,
			keyword TEXT NOT NULL,
			UNIQUE (guild_id, keyword)
		);
		CREATE TABLE guild_blacklisted_companies (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			guild_id INTEGER NOT NULL,
			company TEXT NOT NULL,
			UNIQUE (guild_id, company)
		);
		CREATE TABLE guild_blacklisted_keywords (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			guild_id INTEGER NOT NULL,
			keyword TEXT NOT NULL,
			UNIQUE (guild_id, keyword)
		);
		CREATE TABLE guild_languages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			guild_id INTEGER NOT NULL,
			language TEXT NOT NULL,
			UNIQUE (guild_id, language)
		);
		CREATE TABLE guild_portals (
			guild_id INTEGER NOT NULL,
			source TEXT NOT NULL,
			UNIQUE (guild_id, source)
		);
		CREATE TABLE guild_locations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			guild_id INTEGER NOT NULL,
			position INTEGER NOT NULL,
			location TEXT NOT NULL,
			UNIQUE (guild_id, location)
		);
		CREATE TABLE notified_jobs (
			guild_id INTEGER NOT NULL,
			job_id TEXT NOT NULL,
			source TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (guild_id, job_id)
		);
		CREATE INDEX idx_notified_jobs_created_at ON notified_jobs(created_at);
		CREATE TABLE tracked_jobs (
			token TEXT PRIMARY KEY,
			guild_id INTEGER NOT NULL,
			job_id TEXT NOT NULL,
			source TEXT NOT NULL,
			job_url TEXT NOT NULL,
			job_title TEXT NOT NULL,
			company TEXT NOT NULL,
			created_at TEXT NOT NULL
		);
		CREATE TABLE job_interactions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			guild_id INTEGER NOT NULL,
			user_id INTEGER NOT NULL,
			job_url TEXT NOT NULL,
			job_title TEXT NOT NULL,
			company TEXT NOT NULL,
			source TEXT NOT NULL,
			clicked_at TEXT NOT NULL,
			applied INTEGER,
			observation TEXT,
			UNIQUE (guild_id, user_id, job_url)
		);
	`, maxAgeDDL)
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}

	statements := []string{
		"INSERT INTO guild_configs (guild_id, channel_id, location) VALUES (1, 10, 'Todo el mundo')",
		"INSERT INTO guild_keywords (guild_id, keyword) VALUES (1, 'Python'), (1, 'python')",
		"INSERT INTO guild_blacklisted_companies (guild_id, company) VALUES (1, 'Acme'), (1, 'acme')",
		"INSERT INTO guild_blacklisted_keywords (guild_id, keyword) VALUES (1, 'Práctica'), (1, 'práctica')",
		"INSERT INTO guild_languages (guild_id, language) VALUES (1, 'English'), (1, 'english')",
		"INSERT INTO guild_locations (guild_id, position, location) VALUES (1, 0, 'Todo el mundo')",
		"INSERT INTO notified_jobs (guild_id, job_id, source) VALUES (1, 'j1', 'LinkedIn'), (1, 'j2', 'GetOnBoard')",
		`INSERT INTO job_interactions
			(guild_id, user_id, job_url, job_title, company, source, clicked_at, applied, observation)
		 VALUES (1, 5, 'https://example.com/1', 'Role', 'Acme', 'linkedin', '2026-01-01T00:00:00+00:00', NULL, 'old note')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("insert legacy row: %v", err)
		}
	}
}

func TestMigrateLegacyDatabase(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	writeLegacyDB(t, path, false)

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open(legacy) unexpected error: %v", err)
	}
	defer store.Close()

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx() unexpected error: %v", err)
	}
	defer tx.Rollback()

	configColumns, err := tableColumns(ctx, tx, "guild_configs")
	if err != nil {
		t.Fatalf("tableColumns(guild_configs) unexpected error: %v", err)
	}
	for _, name := range []string{"max_age_hours", "setup_message_sent", "experimental_enabled"} {
		if !hasColumn(configColumns, name) {
			t.Errorf("guild_configs is missing %s after migration", name)
		}
	}
	interactionColumns, err := tableColumns(ctx, tx, "job_interactions")
	if err != nil {
		t.Fatalf("tableColumns(job_interactions) unexpected error: %v", err)
	}
	if !hasColumn(interactionColumns, "notes") {
		t.Error("job_interactions is missing notes after migration")
	}
	if hasColumn(interactionColumns, "observation") {
		t.Error("job_interactions still has observation after migration")
	}
	key, err := primaryKey(ctx, tx, "notified_jobs")
	if err != nil {
		t.Fatalf("primaryKey(notified_jobs) unexpected error: %v", err)
	}
	if !equalStrings(key, notifiedKey) {
		t.Fatalf("notified_jobs primary key = %v, want %v", key, notifiedKey)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() unexpected error: %v", err)
	}

	config, err := store.GetGuildConfig(ctx, guild)
	if err != nil {
		t.Fatalf("GetGuildConfig() unexpected error: %v", err)
	}
	if config.Location() != DefaultLocation {
		t.Errorf("Location() = %q, want %q", config.Location(), DefaultLocation)
	}
	if !reflect.DeepEqual(config.Locations, []string{DefaultLocation}) {
		t.Errorf("Locations = %v, want [%s]", config.Locations, DefaultLocation)
	}
	if !reflect.DeepEqual(config.Keywords, []string{"python"}) {
		t.Errorf("Keywords = %v, want [python]", config.Keywords)
	}
	if !reflect.DeepEqual(config.BlacklistedCompanies, []string{"acme"}) {
		t.Errorf("BlacklistedCompanies = %v, want [acme]", config.BlacklistedCompanies)
	}
	if !reflect.DeepEqual(config.BlacklistedKeywords, []string{"práctica"}) {
		t.Errorf("BlacklistedKeywords = %v, want [práctica]", config.BlacklistedKeywords)
	}
	if !reflect.DeepEqual(config.Languages, []string{"english"}) {
		t.Errorf("Languages = %v, want [english]", config.Languages)
	}
	if !reflect.DeepEqual(config.Portals, []string{"linkedin"}) {
		t.Errorf("Portals = %v, want [linkedin]", config.Portals)
	}

	keys, err := store.GetNotifiedKeys(ctx, guild)
	if err != nil {
		t.Fatalf("GetNotifiedKeys() unexpected error: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("GetNotifiedKeys() = %v, want two preserved rows", keys)
	}

	var notes string
	if err := store.db.QueryRowContext(ctx,
		"SELECT notes FROM job_interactions WHERE id = 1",
	).Scan(&notes); err != nil {
		t.Fatalf("read migrated notes: %v", err)
	}
	if notes != "old note" {
		t.Fatalf("notes = %q, want old note", notes)
	}

	var tableCount int
	if err := store.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name IN ('tracked_jobs', 'job_interactions')`,
	).Scan(&tableCount); err != nil {
		t.Fatalf("count experimental tables: %v", err)
	}
	if tableCount != 2 {
		t.Fatalf("experimental table count = %d, want 2", tableCount)
	}

	if err := store.Initialize(ctx); err != nil {
		t.Fatalf("second Initialize() unexpected error: %v", err)
	}
	config, err = store.GetGuildConfig(ctx, guild)
	if err != nil {
		t.Fatalf("GetGuildConfig() after re-init unexpected error: %v", err)
	}
	if !reflect.DeepEqual(config.Keywords, []string{"python"}) {
		t.Fatalf("Keywords after re-init = %v, want [python]", config.Keywords)
	}
}

func TestMigrateClampsMaxAgeHours(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy_clamp.db")
	writeLegacyDB(t, path, true)
	db, err := sql.Open("sqlite", path+"?_foreign_keys=1&_journal_mode=WAL")
	if err != nil {
		t.Fatalf("sql.Open() unexpected error: %v", err)
	}
	if _, err := db.Exec("UPDATE guild_configs SET max_age_hours = 48.0 WHERE guild_id = 1"); err != nil {
		t.Fatalf("set high max age: %v", err)
	}
	if _, err := db.Exec("INSERT INTO guild_configs (guild_id, max_age_hours) VALUES (2, 0.001)"); err != nil {
		t.Fatalf("insert low max age: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close() unexpected error: %v", err)
	}

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open(legacy) unexpected error: %v", err)
	}
	defer store.Close()

	for _, test := range []struct {
		guild int64
		want  float64
	}{
		{guild: 1, want: MaxMaxAgeHours},
		{guild: 2, want: MinMaxAgeHours},
	} {
		config, err := store.GetGuildConfig(ctx, test.guild)
		if err != nil {
			t.Fatalf("GetGuildConfig(%d) unexpected error: %v", test.guild, err)
		}
		if config.MaxAgeHours != test.want {
			t.Errorf("guild %d MaxAgeHours = %v, want %v", test.guild, config.MaxAgeHours, test.want)
		}
	}
}

func TestOpenCreatesParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "bot.db")
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() unexpected error: %v", err)
	}
	defer store.Close()

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("database file was not created: %v", err)
	}
}

func TestOpenHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Open(ctx, filepath.Join(t.TempDir(), "canceled.db"))
	if err == nil {
		t.Fatal("Open(canceled) error = nil, want an error")
	}
}
