// Package config loads application settings and configures structured logging.
package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

const (
	defaultCheckIntervalMinutes = "5"
	defaultDBPath               = "data/jobs.db"
)

// Settings holds the runtime configuration read from the environment and .env.
type Settings struct {
	DiscordBotToken      string
	CheckIntervalMinutes float64
	DBPath               string
}

type environmentLookup func(key string) (string, bool)

type dotEnvLoader func(filenames ...string) error

type validationError struct {
	message string
	cause   error
}

func (e *validationError) Error() string {
	return e.message
}

func (e *validationError) Unwrap() error {
	return e.cause
}

// Load reads settings from the process environment after loading .env.
// It is a convenience wrapper around LoadContext with a background context.
func Load() (Settings, error) {
	return LoadContext(context.Background())
}

// LoadContext reads settings from the process environment after loading .env.
// A missing .env file is ignored, matching load_dotenv() in the Python bot.
func LoadContext(ctx context.Context) (Settings, error) {
	return load(ctx, godotenv.Load, os.LookupEnv)
}

func load(ctx context.Context, loadDotEnv dotEnvLoader, lookup environmentLookup) (Settings, error) {
	if err := ctx.Err(); err != nil {
		return Settings{}, fmt.Errorf("load settings: %w", err)
	}

	if err := loadDotEnv(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Settings{}, fmt.Errorf("load .env: %w", err)
	}

	if err := ctx.Err(); err != nil {
		return Settings{}, fmt.Errorf("load settings: %w", err)
	}

	token := strings.TrimSpace(valueOr(lookup, "DISCORD_BOT_TOKEN", ""))
	if token == "" {
		return Settings{}, &validationError{message: "DISCORD_BOT_TOKEN is required."}
	}

	intervalText := strings.TrimSpace(valueOr(lookup, "CHECK_INTERVAL_MINUTES", defaultCheckIntervalMinutes))
	interval, err := strconv.ParseFloat(intervalText, 64)
	if err != nil {
		return Settings{}, &validationError{
			message: "CHECK_INTERVAL_MINUTES must be a positive number.",
			cause:   err,
		}
	}
	if interval <= 0 {
		return Settings{}, &validationError{message: "CHECK_INTERVAL_MINUTES must be greater than zero."}
	}

	dbPath := valueOr(lookup, "DB_PATH", defaultDBPath)
	if dbPath == "" {
		dbPath = "."
	}
	expandedPath, err := expandUserHome(ctx, dbPath)
	if err != nil {
		return Settings{}, fmt.Errorf("expand DB_PATH: %w", err)
	}

	return Settings{
		DiscordBotToken:      token,
		CheckIntervalMinutes: interval,
		DBPath:               expandedPath,
	}, nil
}

func valueOr(lookup environmentLookup, key, fallback string) string {
	if value, ok := lookup(key); ok {
		return value
	}
	return fallback
}

func expandUserHome(ctx context.Context, path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, `~\`) {
		return path, nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	if path == "~" {
		return home, nil
	}
	return home + path[1:], nil
}
