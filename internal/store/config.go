package store

import (
	"context"
	"database/sql"
	"fmt"
)

func (s *Store) strings(ctx context.Context, query string, args ...any) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query strings: %w", err)
	}
	defer rows.Close()

	result := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("scan string: %w", err)
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate strings: %w", err)
	}
	return result, nil
}

func (s *Store) changed(ctx context.Context, query string, args ...any) (bool, error) {
	result, err := s.db.ExecContext(ctx, query, args...)
	if err != nil {
		return false, fmt.Errorf("execute statement: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read affected rows: %w", err)
	}
	return count == 1, nil
}

// HasSetupMessage reports whether the guild has already seen the setup message.
func (s *Store) HasSetupMessage(ctx context.Context, guildID int64) (bool, error) {
	if err := s.ensureGuild(ctx, guildID); err != nil {
		return false, err
	}
	var value int
	if err := s.db.QueryRowContext(ctx,
		"SELECT setup_message_sent FROM guild_configs WHERE guild_id = ?", guildID,
	).Scan(&value); err != nil {
		return false, fmt.Errorf("read setup message flag: %w", err)
	}
	return value != 0, nil
}

// MarkSetupMessageSent records that the guild has seen the setup message.
func (s *Store) MarkSetupMessageSent(ctx context.Context, guildID int64) error {
	if err := s.ensureGuild(ctx, guildID); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		"UPDATE guild_configs SET setup_message_sent = 1 WHERE guild_id = ?", guildID,
	)
	if err != nil {
		return fmt.Errorf("mark setup message sent: %w", err)
	}
	return nil
}

// IsExperimentalEnabled reports the guild's experimental-mode flag.
func (s *Store) IsExperimentalEnabled(ctx context.Context, guildID int64) (bool, error) {
	if err := s.ensureGuild(ctx, guildID); err != nil {
		return false, err
	}
	var value int
	if err := s.db.QueryRowContext(ctx,
		"SELECT experimental_enabled FROM guild_configs WHERE guild_id = ?", guildID,
	).Scan(&value); err != nil {
		return false, fmt.Errorf("read experimental flag: %w", err)
	}
	return value != 0, nil
}

// SetExperimentalEnabled updates the guild's experimental-mode flag.
func (s *Store) SetExperimentalEnabled(ctx context.Context, guildID int64, enabled bool) error {
	if err := s.ensureGuild(ctx, guildID); err != nil {
		return err
	}
	value := 0
	if enabled {
		value = 1
	}
	if _, err := s.db.ExecContext(ctx,
		"UPDATE guild_configs SET experimental_enabled = ? WHERE guild_id = ?", value, guildID,
	); err != nil {
		return fmt.Errorf("set experimental flag: %w", err)
	}
	return nil
}

// GetExperimentalEnabledGuildIDs returns every guild with experimental mode on.
func (s *Store) GetExperimentalEnabledGuildIDs(ctx context.Context) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT guild_id FROM guild_configs
		WHERE experimental_enabled = 1
		ORDER BY guild_id`)
	if err != nil {
		return nil, fmt.Errorf("query experimental guilds: %w", err)
	}
	defer rows.Close()

	result := make([]int64, 0)
	for rows.Next() {
		var guildID int64
		if err := rows.Scan(&guildID); err != nil {
			return nil, fmt.Errorf("scan experimental guild: %w", err)
		}
		result = append(result, guildID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate experimental guilds: %w", err)
	}
	return result, nil
}

// SetChannel updates the notification channel for a guild.
func (s *Store) SetChannel(ctx context.Context, guildID, channelID int64) error {
	if err := s.ensureGuild(ctx, guildID); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx,
		"UPDATE guild_configs SET channel_id = ? WHERE guild_id = ?", channelID, guildID,
	); err != nil {
		return fmt.Errorf("set channel: %w", err)
	}
	return nil
}

// SetMaxAgeHours updates the maximum accepted job age for a guild.
func (s *Store) SetMaxAgeHours(ctx context.Context, guildID int64, hours float64) error {
	if err := s.ensureGuild(ctx, guildID); err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx,
		"UPDATE guild_configs SET max_age_hours = ? WHERE guild_id = ?", hours, guildID,
	); err != nil {
		return fmt.Errorf("set max age hours: %w", err)
	}
	return nil
}

// SetLocation replaces all guild locations with a single one.
func (s *Store) SetLocation(ctx context.Context, guildID int64, location string) error {
	if err := s.ensureGuild(ctx, guildID); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin set location: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		"UPDATE guild_configs SET location = ? WHERE guild_id = ?", location, guildID,
	); err != nil {
		return fmt.Errorf("set configured location: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		"DELETE FROM guild_locations WHERE guild_id = ?", guildID,
	); err != nil {
		return fmt.Errorf("clear locations: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO guild_locations (guild_id, position, location)
		VALUES (?, 0, ?)`, guildID, location,
	); err != nil {
		return fmt.Errorf("insert location: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit set location: %w", err)
	}
	return nil
}

// GetLocations returns the guild locations in configured order.
func (s *Store) GetLocations(ctx context.Context, guildID int64) ([]string, error) {
	if err := s.ensureGuild(ctx, guildID); err != nil {
		return nil, err
	}
	return s.strings(ctx, `
		SELECT location FROM guild_locations
		WHERE guild_id = ? ORDER BY position, id`, guildID)
}

// AddLocation appends a location and reports whether it was newly inserted.
func (s *Store) AddLocation(ctx context.Context, guildID int64, location string) (bool, error) {
	if err := s.ensureGuild(ctx, guildID); err != nil {
		return false, err
	}
	position := 0
	if err := s.db.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(position), -1) + 1 FROM guild_locations WHERE guild_id = ?", guildID,
	).Scan(&position); err != nil {
		return false, fmt.Errorf("read next location position: %w", err)
	}
	return s.changed(ctx, `
		INSERT OR IGNORE INTO guild_locations (guild_id, position, location)
		VALUES (?, ?, ?)`, guildID, position, location)
}

// RemoveLocation removes one location and renumbers the remaining ones.
func (s *Store) RemoveLocation(ctx context.Context, guildID int64, location string) (bool, error) {
	removed, err := s.changed(ctx,
		"DELETE FROM guild_locations WHERE guild_id = ? AND location = ?", guildID, location,
	)
	if err != nil || !removed {
		return removed, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin renumber locations: %w", err)
	}
	defer tx.Rollback()

	ids, err := queryInt64s(ctx, tx, `
		SELECT id FROM guild_locations
		WHERE guild_id = ? ORDER BY position, id`, guildID)
	if err != nil {
		return false, err
	}
	for index, id := range ids {
		if _, err := tx.ExecContext(ctx,
			"UPDATE guild_locations SET position = ? WHERE id = ?", index, id,
		); err != nil {
			return false, fmt.Errorf("renumber location: %w", err)
		}
	}
	first, err := queryOptionalString(ctx, tx, `
		SELECT location FROM guild_locations
		WHERE guild_id = ? ORDER BY position, id LIMIT 1`, guildID)
	if err != nil {
		return false, err
	}
	if first == nil {
		first = new(string)
	}
	if _, err := tx.ExecContext(ctx,
		"UPDATE guild_configs SET location = ? WHERE guild_id = ?", *first, guildID,
	); err != nil {
		return false, fmt.Errorf("update configured location: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit renumber locations: %w", err)
	}
	return true, nil
}

func queryInt64s(ctx context.Context, tx *sql.Tx, query string, args ...any) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query ids: %w", err)
	}
	defer rows.Close()

	result := make([]int64, 0)
	for rows.Next() {
		var value int64
		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("scan id: %w", err)
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate ids: %w", err)
	}
	return result, nil
}

func queryOptionalString(ctx context.Context, tx *sql.Tx, query string, args ...any) (*string, error) {
	var value sql.NullString
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&value); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("query optional string: %w", err)
	}
	if !value.Valid {
		return nil, nil
	}
	return &value.String, nil
}

// AddKeyword stores a lowercased keyword for a guild.
func (s *Store) AddKeyword(ctx context.Context, guildID int64, keyword string) (bool, error) {
	if err := s.ensureGuild(ctx, guildID); err != nil {
		return false, err
	}
	return s.changed(ctx,
		"INSERT OR IGNORE INTO guild_keywords (guild_id, keyword) VALUES (?, ?)",
		guildID, lowerTerm(keyword))
}

// RemoveKeyword deletes a lowercased keyword from a guild.
func (s *Store) RemoveKeyword(ctx context.Context, guildID int64, keyword string) (bool, error) {
	return s.changed(ctx,
		"DELETE FROM guild_keywords WHERE guild_id = ? AND keyword = ?",
		guildID, lowerTerm(keyword))
}

// AddBlacklistedCompany stores a lowercased blocked company for a guild.
func (s *Store) AddBlacklistedCompany(ctx context.Context, guildID int64, company string) (bool, error) {
	if err := s.ensureGuild(ctx, guildID); err != nil {
		return false, err
	}
	return s.changed(ctx, `
		INSERT INTO guild_blacklisted_companies (guild_id, company)
		SELECT ?, ? WHERE NOT EXISTS (
			SELECT 1 FROM guild_blacklisted_companies
			WHERE guild_id = ? AND lower(company) = lower(?))`,
		guildID, lowerTerm(company), guildID, lowerTerm(company))
}

// RemoveBlacklistedCompany deletes a blocked company case-insensitively.
func (s *Store) RemoveBlacklistedCompany(ctx context.Context, guildID int64, company string) (bool, error) {
	return s.changed(ctx, `
		DELETE FROM guild_blacklisted_companies
		WHERE guild_id = ? AND lower(company) = lower(?)`,
		guildID, lowerTerm(company))
}

// AddBlacklistedKeyword stores a lowercased blocked keyword for a guild.
func (s *Store) AddBlacklistedKeyword(ctx context.Context, guildID int64, keyword string) (bool, error) {
	if err := s.ensureGuild(ctx, guildID); err != nil {
		return false, err
	}
	return s.changed(ctx, `
		INSERT INTO guild_blacklisted_keywords (guild_id, keyword)
		SELECT ?, ? WHERE NOT EXISTS (
			SELECT 1 FROM guild_blacklisted_keywords
			WHERE guild_id = ? AND keyword = ?)`,
		guildID, lowerTerm(keyword), guildID, lowerTerm(keyword))
}

// RemoveBlacklistedKeyword deletes a lowercased blocked keyword from a guild.
func (s *Store) RemoveBlacklistedKeyword(ctx context.Context, guildID int64, keyword string) (bool, error) {
	return s.changed(ctx,
		"DELETE FROM guild_blacklisted_keywords WHERE guild_id = ? AND keyword = ?",
		guildID, lowerTerm(keyword))
}

// AddLanguage stores a lowercased language for a guild.
func (s *Store) AddLanguage(ctx context.Context, guildID int64, language string) (bool, error) {
	if err := s.ensureGuild(ctx, guildID); err != nil {
		return false, err
	}
	return s.changed(ctx,
		"INSERT OR IGNORE INTO guild_languages (guild_id, language) VALUES (?, ?)",
		guildID, lowerTerm(language))
}

// RemoveLanguage deletes a lowercased language from a guild.
func (s *Store) RemoveLanguage(ctx context.Context, guildID int64, language string) (bool, error) {
	return s.changed(ctx,
		"DELETE FROM guild_languages WHERE guild_id = ? AND language = ?",
		guildID, lowerTerm(language))
}

// GetPortals returns the guild's enabled portals in alphabetical order.
func (s *Store) GetPortals(ctx context.Context, guildID int64) ([]string, error) {
	return s.strings(ctx,
		"SELECT source FROM guild_portals WHERE guild_id = ? ORDER BY source", guildID)
}

// AddPortal enables a portal for a guild.
func (s *Store) AddPortal(ctx context.Context, guildID int64, source string) (bool, error) {
	if err := s.ensureGuild(ctx, guildID); err != nil {
		return false, err
	}
	return s.changed(ctx,
		"INSERT OR IGNORE INTO guild_portals (guild_id, source) VALUES (?, ?)",
		guildID, source)
}

// RemovePortal disables a portal for a guild.
func (s *Store) RemovePortal(ctx context.Context, guildID int64, source string) (bool, error) {
	return s.changed(ctx,
		"DELETE FROM guild_portals WHERE guild_id = ? AND source = ?",
		guildID, source)
}

// GetGuildConfig returns the full configuration of one guild.
func (s *Store) GetGuildConfig(ctx context.Context, guildID int64) (GuildMonitorConfig, error) {
	if err := s.ensureGuild(ctx, guildID); err != nil {
		return GuildMonitorConfig{}, err
	}
	return s.configForGuild(ctx, guildID)
}

// GetMonitorConfigs returns configurations for guilds with a channel and keywords.
func (s *Store) GetMonitorConfigs(ctx context.Context) ([]GuildMonitorConfig, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT guild_id FROM guild_configs
		WHERE channel_id IS NOT NULL
		ORDER BY guild_id`)
	if err != nil {
		return nil, fmt.Errorf("query monitor guilds: %w", err)
	}
	ids, err := scanInt64Rows(rows, "monitor guild")
	if err != nil {
		return nil, err
	}

	configs := make([]GuildMonitorConfig, 0, len(ids))
	for _, id := range ids {
		config, err := s.configForGuild(ctx, id)
		if err != nil {
			return nil, err
		}
		if len(config.Keywords) > 0 {
			configs = append(configs, config)
		}
	}
	return configs, nil
}

func scanInt64Rows(rows *sql.Rows, label string) ([]int64, error) {
	defer rows.Close()

	result := make([]int64, 0)
	for rows.Next() {
		var value int64
		if err := rows.Scan(&value); err != nil {
			return nil, fmt.Errorf("scan %s: %w", label, err)
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate %s: %w", label, err)
	}
	return result, nil
}

func (s *Store) configForGuild(ctx context.Context, guildID int64) (GuildMonitorConfig, error) {
	var channelID sql.NullInt64
	var maxAge float64
	if err := s.db.QueryRowContext(ctx, `
		SELECT channel_id, max_age_hours FROM guild_configs WHERE guild_id = ?`, guildID,
	).Scan(&channelID, &maxAge); err != nil {
		return GuildMonitorConfig{}, fmt.Errorf("read guild config %d: %w", guildID, err)
	}

	keywords, err := s.strings(ctx,
		"SELECT keyword FROM guild_keywords WHERE guild_id = ? ORDER BY keyword", guildID)
	if err != nil {
		return GuildMonitorConfig{}, err
	}
	companies, err := s.strings(ctx, `
		SELECT company FROM guild_blacklisted_companies
		WHERE guild_id = ? ORDER BY company`, guildID)
	if err != nil {
		return GuildMonitorConfig{}, err
	}
	blockedKeywords, err := s.strings(ctx, `
		SELECT keyword FROM guild_blacklisted_keywords
		WHERE guild_id = ? ORDER BY keyword`, guildID)
	if err != nil {
		return GuildMonitorConfig{}, err
	}
	languages, err := s.strings(ctx,
		"SELECT language FROM guild_languages WHERE guild_id = ? ORDER BY language", guildID)
	if err != nil {
		return GuildMonitorConfig{}, err
	}
	portals, err := s.strings(ctx,
		"SELECT source FROM guild_portals WHERE guild_id = ? ORDER BY source", guildID)
	if err != nil {
		return GuildMonitorConfig{}, err
	}
	locations, err := s.strings(ctx, `
		SELECT location FROM guild_locations
		WHERE guild_id = ? ORDER BY position, id`, guildID)
	if err != nil {
		return GuildMonitorConfig{}, err
	}

	config := GuildMonitorConfig{
		GuildID:              guildID,
		Keywords:             keywords,
		BlacklistedCompanies: companies,
		BlacklistedKeywords:  blockedKeywords,
		Languages:            languages,
		MaxAgeHours:          maxAge,
		Portals:              portals,
		Locations:            locations,
	}
	if channelID.Valid {
		value := channelID.Int64
		config.ChannelID = &value
	}
	return config, nil
}
