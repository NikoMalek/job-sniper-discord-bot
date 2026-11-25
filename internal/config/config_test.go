package config

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestLoadValidation(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("home directory is unavailable: %v", err)
	}
	separator := string(os.PathSeparator)

	tests := []struct {
		name          string
		env           map[string]string
		want          Settings
		wantNaNFloat  bool
		wantErr       string
		wantSyntaxErr bool
	}{
		{
			name: "defaults",
			env:  map[string]string{"DISCORD_BOT_TOKEN": "test-token"},
			want: Settings{
				DiscordBotToken:      "test-token",
				CheckIntervalMinutes: 5,
				DBPath:               "data/jobs.db",
			},
		},
		{
			name: "token is trimmed",
			env:  map[string]string{"DISCORD_BOT_TOKEN": "  test-token  "},
			want: Settings{
				DiscordBotToken:      "test-token",
				CheckIntervalMinutes: 5,
				DBPath:               "data/jobs.db",
			},
		},
		{
			name:    "missing token",
			env:     map[string]string{},
			wantErr: "DISCORD_BOT_TOKEN is required.",
		},
		{
			name:    "blank token",
			env:     map[string]string{"DISCORD_BOT_TOKEN": " \t "},
			wantErr: "DISCORD_BOT_TOKEN is required.",
		},
		{
			name: "non-numeric interval",
			env: map[string]string{
				"DISCORD_BOT_TOKEN":      "test-token",
				"CHECK_INTERVAL_MINUTES": "soon",
			},
			wantErr:       "CHECK_INTERVAL_MINUTES must be a positive number.",
			wantSyntaxErr: true,
		},
		{
			name: "empty interval is not treated as the default",
			env: map[string]string{
				"DISCORD_BOT_TOKEN":      "test-token",
				"CHECK_INTERVAL_MINUTES": "",
			},
			wantErr:       "CHECK_INTERVAL_MINUTES must be a positive number.",
			wantSyntaxErr: true,
		},
		{
			name: "zero interval",
			env: map[string]string{
				"DISCORD_BOT_TOKEN":      "test-token",
				"CHECK_INTERVAL_MINUTES": "0",
			},
			wantErr: "CHECK_INTERVAL_MINUTES must be greater than zero.",
		},
		{
			name: "negative interval",
			env: map[string]string{
				"DISCORD_BOT_TOKEN":      "test-token",
				"CHECK_INTERVAL_MINUTES": "-2.5",
			},
			wantErr: "CHECK_INTERVAL_MINUTES must be greater than zero.",
		},
		{
			name: "fractional interval",
			env: map[string]string{
				"DISCORD_BOT_TOKEN":      "test-token",
				"CHECK_INTERVAL_MINUTES": " 2.5 ",
			},
			want: Settings{
				DiscordBotToken:      "test-token",
				CheckIntervalMinutes: 2.5,
				DBPath:               "data/jobs.db",
			},
		},
		{
			name: "scientific interval",
			env: map[string]string{
				"DISCORD_BOT_TOKEN":      "test-token",
				"CHECK_INTERVAL_MINUTES": "1e2",
			},
			want: Settings{
				DiscordBotToken:      "test-token",
				CheckIntervalMinutes: 100,
				DBPath:               "data/jobs.db",
			},
		},
		{
			name: "nan interval matches Python comparison",
			env: map[string]string{
				"DISCORD_BOT_TOKEN":      "test-token",
				"CHECK_INTERVAL_MINUTES": "nan",
			},
			want: Settings{
				DiscordBotToken:      "test-token",
				CheckIntervalMinutes: math.NaN(),
				DBPath:               "data/jobs.db",
			},
			wantNaNFloat: true,
		},
		{
			name: "database path from environment",
			env: map[string]string{
				"DISCORD_BOT_TOKEN": "test-token",
				"DB_PATH":           "custom/jobs.db",
			},
			want: Settings{
				DiscordBotToken:      "test-token",
				CheckIntervalMinutes: 5,
				DBPath:               "custom/jobs.db",
			},
		},
		{
			name: "empty database path becomes current directory",
			env: map[string]string{
				"DISCORD_BOT_TOKEN": "test-token",
				"DB_PATH":           "",
			},
			want: Settings{
				DiscordBotToken:      "test-token",
				CheckIntervalMinutes: 5,
				DBPath:               ".",
			},
		},
		{
			name: "tilde database path is expanded",
			env: map[string]string{
				"DISCORD_BOT_TOKEN": "test-token",
				"DB_PATH":           "~" + separator + "jobs.db",
			},
			want: Settings{
				DiscordBotToken:      "test-token",
				CheckIntervalMinutes: 5,
				DBPath:               filepath.Join(home, "jobs.db"),
			},
		},
		{
			name: "bare tilde database path is expanded",
			env: map[string]string{
				"DISCORD_BOT_TOKEN": "test-token",
				"DB_PATH":           "~",
			},
			want: Settings{
				DiscordBotToken:      "test-token",
				CheckIntervalMinutes: 5,
				DBPath:               home,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := load(context.Background(), noDotEnv, mapLookup(tt.env))
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("load() error = nil, want %q", tt.wantErr)
				}
				if err.Error() != tt.wantErr {
					t.Fatalf("load() error = %q, want %q", err.Error(), tt.wantErr)
				}
				if tt.wantSyntaxErr && !errors.Is(err, strconv.ErrSyntax) {
					t.Fatalf("load() error does not wrap strconv.ErrSyntax: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("load() unexpected error: %v", err)
			}
			if got.DiscordBotToken != tt.want.DiscordBotToken {
				t.Errorf("DiscordBotToken = %q, want %q", got.DiscordBotToken, tt.want.DiscordBotToken)
			}
			if tt.wantNaNFloat {
				if !math.IsNaN(got.CheckIntervalMinutes) {
					t.Errorf("CheckIntervalMinutes = %v, want NaN", got.CheckIntervalMinutes)
				}
			} else if got.CheckIntervalMinutes != tt.want.CheckIntervalMinutes {
				t.Errorf("CheckIntervalMinutes = %v, want %v", got.CheckIntervalMinutes, tt.want.CheckIntervalMinutes)
			}
			if got.DBPath != tt.want.DBPath {
				t.Errorf("DBPath = %q, want %q", got.DBPath, tt.want.DBPath)
			}
		})
	}
}

func TestLoadReadsDotEnv(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	unsetEnv(t, "DISCORD_BOT_TOKEN", "CHECK_INTERVAL_MINUTES", "DB_PATH")
	writeDotEnv(t, dir, "DISCORD_BOT_TOKEN=dotenv-value\nCHECK_INTERVAL_MINUTES=7\nDB_PATH=from-dotenv/jobs.db\n")

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}
	want := Settings{
		DiscordBotToken:      "dotenv-value",
		CheckIntervalMinutes: 7,
		DBPath:               "from-dotenv/jobs.db",
	}
	if got != want {
		t.Fatalf("Load() = %+v, want %+v", got, want)
	}
}

func TestLoadEnvironmentOverridesDotEnv(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	unsetEnv(t, "DISCORD_BOT_TOKEN", "CHECK_INTERVAL_MINUTES", "DB_PATH")
	writeDotEnv(t, dir, "DISCORD_BOT_TOKEN=dotenv-value\nCHECK_INTERVAL_MINUTES=7\nDB_PATH=from-dotenv/jobs.db\n")
	t.Setenv("DISCORD_BOT_TOKEN", "env-value")
	t.Setenv("CHECK_INTERVAL_MINUTES", "9")
	t.Setenv("DB_PATH", "from-env/jobs.db")

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}
	want := Settings{
		DiscordBotToken:      "env-value",
		CheckIntervalMinutes: 9,
		DBPath:               "from-env/jobs.db",
	}
	if got != want {
		t.Fatalf("Load() = %+v, want %+v", got, want)
	}
}

func TestLoadIgnoresMissingDotEnv(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	unsetEnv(t, "DISCORD_BOT_TOKEN", "CHECK_INTERVAL_MINUTES", "DB_PATH")
	t.Setenv("DISCORD_BOT_TOKEN", "test-token")

	got, err := Load()
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}
	want := Settings{
		DiscordBotToken:      "test-token",
		CheckIntervalMinutes: 5,
		DBPath:               "data/jobs.db",
	}
	if got != want {
		t.Fatalf("Load() = %+v, want %+v", got, want)
	}
}

func TestLoadDotEnvErrors(t *testing.T) {
	lookup := mapLookup(map[string]string{"DISCORD_BOT_TOKEN": "test-token"})

	t.Run("missing file is ignored", func(t *testing.T) {
		_, err := load(context.Background(), func(...string) error { return os.ErrNotExist }, lookup)
		if err != nil {
			t.Fatalf("load() unexpected error: %v", err)
		}
	})

	t.Run("other read errors are wrapped", func(t *testing.T) {
		_, err := load(context.Background(), func(...string) error { return os.ErrPermission }, lookup)
		if !errors.Is(err, os.ErrPermission) {
			t.Fatalf("load() error = %v, want wrapped os.ErrPermission", err)
		}
	})
}

func TestLoadContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := LoadContext(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("LoadContext() error = %v, want context.Canceled", err)
	}
}

func mapLookup(env map[string]string) environmentLookup {
	return func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	}
}

func noDotEnv(...string) error {
	return nil
}

func writeDotEnv(t *testing.T, dir, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(contents), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}
}

func unsetEnv(t *testing.T, keys ...string) {
	t.Helper()
	for _, key := range keys {
		key := key
		previous, existed := os.LookupEnv(key)
		t.Cleanup(func() {
			var err error
			if existed {
				err = os.Setenv(key, previous)
			} else {
				err = os.Unsetenv(key)
			}
			if err != nil {
				t.Errorf("restore %s: %v", key, err)
			}
		})
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unset %s: %v", key, err)
		}
	}
}
