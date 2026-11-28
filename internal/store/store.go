// Package store persists guild configuration and per-cycle notification state.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	// MinMaxAgeHours is the lower bound applied by the startup migration.
	MinMaxAgeHours = 10.0 / 60.0
	// MaxMaxAgeHours is both the schema default and the upper bound.
	MaxMaxAgeHours = 24.0
	// NotifiedRetentionHours is the default cleanup window.
	NotifiedRetentionHours = MaxMaxAgeHours + 1.0
	// DefaultLocation is the implicit location when none is configured.
	DefaultLocation = "Worldwide"
)

// GuildMonitorConfig is the complete configuration used by one guild monitor.
type GuildMonitorConfig struct {
	GuildID              int64
	ChannelID            *int64
	Locations            []string
	Keywords             []string
	BlacklistedCompanies []string
	Languages            []string
	MaxAgeHours          float64
	Portals              []string
	BlacklistedKeywords  []string
}

// Location returns the first configured location or the implicit default.
func (c GuildMonitorConfig) Location() string {
	if len(c.Locations) > 0 {
		return c.Locations[0]
	}
	return DefaultLocation
}

// Store is a context-aware repository backed by SQLite.
type Store struct {
	db     *sql.DB
	tokens func() (string, error)
	now    func() time.Time
}

// Open initializes the database at path and returns a ready store.
func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("open store: empty database path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}

	dsn := path + "?_busy_timeout=5000&_foreign_keys=1&_journal_mode=WAL"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	store := &Store{
		db: db,
		tokens: func() (string, error) {
			buffer := make([]byte, 8)
			if _, err := rand.Read(buffer); err != nil {
				return "", fmt.Errorf("generate tracking token: %w", err)
			}
			return hex.EncodeToString(buffer), nil
		},
		now: func() time.Time { return time.Now().UTC() },
	}
	if err := store.Initialize(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return store, nil
}

// Close releases the underlying connection pool.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close store: %w", err)
	}
	return nil
}

func lowerTerm(value string) string {
	return strings.ToLower(value)
}

func (s *Store) ensureGuild(ctx context.Context, guildID int64) error {
	if _, err := s.db.ExecContext(ctx,
		"INSERT OR IGNORE INTO guild_configs (guild_id) VALUES (?)", guildID,
	); err != nil {
		return fmt.Errorf("ensure guild %d: %w", guildID, err)
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO guild_portals (guild_id, source)
		SELECT ?, 'linkedin'
		WHERE NOT EXISTS (SELECT 1 FROM guild_portals WHERE guild_id = ?)`,
		guildID, guildID,
	); err != nil {
		return fmt.Errorf("ensure default portal for guild %d: %w", guildID, err)
	}
	return nil
}
