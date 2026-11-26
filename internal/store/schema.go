package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

const schemaDDL = `
CREATE TABLE IF NOT EXISTS guild_configs (
    guild_id INTEGER PRIMARY KEY,
    channel_id INTEGER,
    location TEXT NOT NULL DEFAULT '',
    max_age_hours REAL NOT NULL DEFAULT 24.0,
    setup_message_sent INTEGER NOT NULL DEFAULT 0,
    experimental_enabled INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS guild_keywords (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    guild_id INTEGER NOT NULL,
    keyword TEXT NOT NULL,
    UNIQUE (guild_id, keyword),
    FOREIGN KEY (guild_id) REFERENCES guild_configs(guild_id)
        ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS guild_blacklisted_companies (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    guild_id INTEGER NOT NULL,
    company TEXT NOT NULL,
    UNIQUE (guild_id, company),
    FOREIGN KEY (guild_id) REFERENCES guild_configs(guild_id)
        ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS guild_blacklisted_keywords (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    guild_id INTEGER NOT NULL,
    keyword TEXT NOT NULL,
    UNIQUE (guild_id, keyword),
    FOREIGN KEY (guild_id) REFERENCES guild_configs(guild_id)
        ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS guild_languages (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    guild_id INTEGER NOT NULL,
    language TEXT NOT NULL,
    UNIQUE (guild_id, language),
    FOREIGN KEY (guild_id) REFERENCES guild_configs(guild_id)
        ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS guild_portals (
    guild_id INTEGER NOT NULL,
    source TEXT NOT NULL,
    UNIQUE (guild_id, source),
    FOREIGN KEY (guild_id) REFERENCES guild_configs(guild_id)
        ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS guild_locations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    guild_id INTEGER NOT NULL,
    position INTEGER NOT NULL,
    location TEXT NOT NULL,
    UNIQUE (guild_id, location),
    FOREIGN KEY (guild_id) REFERENCES guild_configs(guild_id)
        ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS notified_jobs (
    guild_id INTEGER NOT NULL,
    job_id TEXT NOT NULL,
    source TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (guild_id, source, job_id),
    FOREIGN KEY (guild_id) REFERENCES guild_configs(guild_id)
        ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_notified_jobs_created_at
    ON notified_jobs(created_at);

CREATE TABLE IF NOT EXISTS tracked_jobs (
    token TEXT PRIMARY KEY,
    guild_id INTEGER NOT NULL,
    job_id TEXT NOT NULL,
    source TEXT NOT NULL,
    job_url TEXT NOT NULL,
    job_title TEXT NOT NULL,
    company TEXT NOT NULL,
    created_at TEXT NOT NULL,
    FOREIGN KEY (guild_id) REFERENCES guild_configs(guild_id)
        ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS job_interactions (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    guild_id INTEGER NOT NULL,
    user_id INTEGER NOT NULL,
    job_url TEXT NOT NULL,
    job_title TEXT NOT NULL,
    company TEXT NOT NULL,
    source TEXT NOT NULL,
    clicked_at TEXT NOT NULL,
    applied INTEGER CHECK (applied IS NULL OR applied IN (0, 1)),
    notes TEXT,
    UNIQUE (guild_id, user_id, job_url),
    FOREIGN KEY (guild_id) REFERENCES guild_configs(guild_id)
        ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_job_interactions_guild_clicked
    ON job_interactions(guild_id, clicked_at DESC);
CREATE INDEX IF NOT EXISTS idx_job_interactions_user_clicked
    ON job_interactions(guild_id, user_id, clicked_at DESC);
CREATE INDEX IF NOT EXISTS idx_tracked_jobs_created_at
    ON tracked_jobs(created_at);
`

var notifiedKey = []string{"guild_id", "source", "job_id"}

// Initialize creates or upgrades the schema. It is safe to call repeatedly.
func (s *Store) Initialize(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin schema migration: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, schemaDDL); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	if err := migrateGuildColumns(ctx, tx); err != nil {
		return err
	}
	if err := migrateInteractionNotes(ctx, tx); err != nil {
		return err
	}
	if err := clampMaxAge(ctx, tx); err != nil {
		return err
	}
	if err := migrateNotifiedKey(ctx, tx); err != nil {
		return err
	}
	if err := backfillGuildDefaults(ctx, tx); err != nil {
		return err
	}
	for _, target := range []struct{ table, column string }{
		{table: "guild_keywords", column: "keyword"},
		{table: "guild_blacklisted_companies", column: "company"},
		{table: "guild_blacklisted_keywords", column: "keyword"},
		{table: "guild_languages", column: "language"},
	} {
		if err := normalizeTerms(ctx, tx, target.table, target.column); err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit schema migration: %w", err)
	}
	return nil
}

type columnInfo struct {
	name string
}

func tableColumns(ctx context.Context, tx *sql.Tx, table string) ([]columnInfo, error) {
	rows, err := tx.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return nil, fmt.Errorf("read columns for %s: %w", table, err)
	}
	defer rows.Close()

	columns := make([]columnInfo, 0)
	for rows.Next() {
		var cid, notNull, pk int
		var name, dataType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &pk); err != nil {
			return nil, fmt.Errorf("scan column info for %s: %w", table, err)
		}
		columns = append(columns, columnInfo{name: name})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate columns for %s: %w", table, err)
	}
	return columns, nil
}

func hasColumn(columns []columnInfo, name string) bool {
	for _, column := range columns {
		if column.name == name {
			return true
		}
	}
	return false
}

func migrateGuildColumns(ctx context.Context, tx *sql.Tx) error {
	columns, err := tableColumns(ctx, tx, "guild_configs")
	if err != nil {
		return err
	}
	for _, migration := range []struct{ column, ddl string }{
		{column: "max_age_hours", ddl: "ALTER TABLE guild_configs ADD COLUMN max_age_hours REAL NOT NULL DEFAULT 24.0"},
		{column: "setup_message_sent", ddl: "ALTER TABLE guild_configs ADD COLUMN setup_message_sent INTEGER NOT NULL DEFAULT 0"},
		{column: "experimental_enabled", ddl: "ALTER TABLE guild_configs ADD COLUMN experimental_enabled INTEGER NOT NULL DEFAULT 0"},
	} {
		if hasColumn(columns, migration.column) {
			continue
		}
		if _, err := tx.ExecContext(ctx, migration.ddl); err != nil {
			return fmt.Errorf("add guild_configs.%s: %w", migration.column, err)
		}
	}
	return nil
}

func migrateInteractionNotes(ctx context.Context, tx *sql.Tx) error {
	columns, err := tableColumns(ctx, tx, "job_interactions")
	if err != nil {
		return err
	}
	if hasColumn(columns, "notes") {
		return nil
	}
	statement := "ALTER TABLE job_interactions ADD COLUMN notes TEXT"
	if hasColumn(columns, "observation") {
		statement = "ALTER TABLE job_interactions RENAME COLUMN observation TO notes"
	}
	if _, err := tx.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("migrate job_interactions notes: %w", err)
	}
	return nil
}

func clampMaxAge(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx,
		"UPDATE guild_configs SET max_age_hours = ? WHERE max_age_hours > ?",
		MaxMaxAgeHours, MaxMaxAgeHours,
	); err != nil {
		return fmt.Errorf("clamp maximum max_age_hours: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		"UPDATE guild_configs SET max_age_hours = ? WHERE max_age_hours < ?",
		MinMaxAgeHours, MinMaxAgeHours,
	); err != nil {
		return fmt.Errorf("clamp minimum max_age_hours: %w", err)
	}
	return nil
}

func primaryKey(ctx context.Context, tx *sql.Tx, table string) ([]string, error) {
	indexes, err := tx.QueryContext(ctx, "PRAGMA index_list("+table+")")
	if err != nil {
		return nil, fmt.Errorf("list indexes for %s: %w", table, err)
	}

	type indexInfo struct {
		name   string
		unique int
		origin string
	}
	candidates := make([]indexInfo, 0)
	for indexes.Next() {
		var seq int
		var info indexInfo
		var partial int
		if err := indexes.Scan(&seq, &info.name, &info.unique, &info.origin, &partial); err != nil {
			indexes.Close()
			return nil, fmt.Errorf("scan index info for %s: %w", table, err)
		}
		if info.origin == "pk" || (info.unique == 1 && strings.HasPrefix(info.name, "sqlite_autoindex_")) {
			candidates = append(candidates, info)
		}
	}
	if err := indexes.Err(); err != nil {
		indexes.Close()
		return nil, fmt.Errorf("iterate indexes for %s: %w", table, err)
	}
	indexes.Close()
	if len(candidates) == 0 {
		return nil, nil
	}

	columns, err := tx.QueryContext(ctx, fmt.Sprintf("PRAGMA index_info(%q)", candidates[0].name))
	if err != nil {
		return nil, fmt.Errorf("read key columns for %s: %w", table, err)
	}
	defer columns.Close()

	result := make([]string, 0)
	for columns.Next() {
		var seqNo, cid int
		var name string
		if err := columns.Scan(&seqNo, &cid, &name); err != nil {
			return nil, fmt.Errorf("scan key column for %s: %w", table, err)
		}
		result = append(result, name)
	}
	if err := columns.Err(); err != nil {
		return nil, fmt.Errorf("iterate key columns for %s: %w", table, err)
	}
	return result, nil
}

func migrateNotifiedKey(ctx context.Context, tx *sql.Tx) error {
	key, err := primaryKey(ctx, tx, "notified_jobs")
	if err != nil {
		return err
	}
	if len(key) == 0 || equalStrings(key, notifiedKey) {
		return nil
	}
	_, err = tx.ExecContext(ctx, `
		ALTER TABLE notified_jobs RENAME TO notified_jobs_legacy;
		CREATE TABLE notified_jobs (
			guild_id INTEGER NOT NULL,
			job_id TEXT NOT NULL,
			source TEXT NOT NULL,
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (guild_id, source, job_id),
			FOREIGN KEY (guild_id) REFERENCES guild_configs(guild_id)
				ON DELETE CASCADE
		);
		INSERT OR IGNORE INTO notified_jobs
			(guild_id, job_id, source, created_at)
		SELECT guild_id, job_id, source, created_at FROM notified_jobs_legacy;
		DROP TABLE notified_jobs_legacy;
		CREATE INDEX IF NOT EXISTS idx_notified_jobs_created_at
			ON notified_jobs(created_at);
	`)
	if err != nil {
		return fmt.Errorf("rebuild notified_jobs key: %w", err)
	}
	return nil
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func backfillGuildDefaults(ctx context.Context, tx *sql.Tx) error {
	statements := []struct {
		sql string
		err string
	}{
		{
			sql: `
				INSERT OR IGNORE INTO guild_portals (guild_id, source)
				SELECT guild_id, 'linkedin' FROM guild_configs
				WHERE guild_id NOT IN (SELECT guild_id FROM guild_portals)`,
			err: "backfill default portal",
		},
		{
			sql: `
				INSERT OR IGNORE INTO guild_locations (guild_id, position, location)
				SELECT guild_id, 0, location FROM guild_configs
				WHERE guild_id NOT IN (SELECT guild_id FROM guild_locations)
				  AND location <> ''`,
			err: "backfill location",
		},
		{
			sql: "UPDATE OR IGNORE guild_configs SET location = 'Worldwide' WHERE location = 'Todo el mundo'",
			err: "normalize configured location",
		},
		{
			sql: "UPDATE OR IGNORE guild_locations SET location = 'Worldwide' WHERE location = 'Todo el mundo'",
			err: "normalize stored location",
		},
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement.sql); err != nil {
			return fmt.Errorf("%s: %w", statement.err, err)
		}
	}
	return nil
}

type termKey struct {
	guild int64
	term  string
}

type termRow struct {
	id    int64
	guild int64
	value string
}

func normalizeTerms(ctx context.Context, tx *sql.Tx, table, column string) error {
	query := fmt.Sprintf("SELECT id, guild_id, %s FROM %s ORDER BY guild_id, id", column, table)
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("read terms from %s: %w", table, err)
	}

	seen := make(map[termKey]struct{})
	toDelete := make([]int64, 0)
	toUpdate := make([]termRow, 0)
	for rows.Next() {
		var row termRow
		if err := rows.Scan(&row.id, &row.guild, &row.value); err != nil {
			rows.Close()
			return fmt.Errorf("scan term from %s: %w", table, err)
		}
		normalized := lowerTerm(row.value)
		key := termKey{guild: row.guild, term: normalized}
		if _, ok := seen[key]; ok {
			toDelete = append(toDelete, row.id)
			continue
		}
		seen[key] = struct{}{}
		if normalized != row.value {
			toUpdate = append(toUpdate, row)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate terms from %s: %w", table, err)
	}
	rows.Close()

	for _, id := range toDelete {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE id = ?", id); err != nil {
			return fmt.Errorf("deduplicate term in %s: %w", table, err)
		}
	}
	for _, row := range toUpdate {
		statement := fmt.Sprintf("UPDATE %s SET %s = ? WHERE id = ?", table, column)
		if _, err := tx.ExecContext(ctx, statement, lowerTerm(row.value), row.id); err != nil {
			return fmt.Errorf("lowercase term in %s: %w", table, err)
		}
	}
	return nil
}
