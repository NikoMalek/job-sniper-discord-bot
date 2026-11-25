package config

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestParseLogLevel(t *testing.T) {
	tests := []struct {
		raw  string
		want slog.Level
	}{
		{raw: "debug", want: slog.LevelDebug},
		{raw: " INFO ", want: slog.LevelInfo},
		{raw: "", want: slog.LevelInfo},
		{raw: "warn", want: slog.LevelWarn},
		{raw: "WARNING", want: slog.LevelWarn},
		{raw: "error", want: slog.LevelError},
		{raw: "critical", want: levelCritical},
		{raw: "FATAL", want: levelCritical},
		{raw: "NOTSET", want: levelNotSet},
		{raw: "trace", want: slog.LevelInfo},
		{raw: "20", want: slog.LevelInfo},
	}

	for _, tt := range tests {
		t.Run(tt.raw, func(t *testing.T) {
			if got := ParseLogLevel(tt.raw); got != tt.want {
				t.Fatalf("ParseLogLevel(%q) = %v, want %v", tt.raw, got, tt.want)
			}
		})
	}
}

func TestHandlerRoutesRecordsByLevel(t *testing.T) {
	var stdout, stderr strings.Builder
	handler := NewHandler(&stdout, &stderr, slog.LevelDebug, "test")
	timestamp := time.Date(2026, time.November, 25, 12, 34, 56, 0, time.UTC)

	tests := []struct {
		level      slog.Level
		message    string
		wantStdout string
		wantStderr string
	}{
		{
			level:      slog.LevelDebug,
			message:    "debug message",
			wantStdout: "2026-11-25 12:34:56 DEBUG    test: debug message\n",
		},
		{
			level:      slog.LevelInfo,
			message:    "info message",
			wantStdout: "2026-11-25 12:34:56 INFO     test: info message\n",
		},
		{
			level:      slog.LevelWarn,
			message:    "warn message",
			wantStderr: "2026-11-25 12:34:56 WARNING  test: warn message\n",
		},
		{
			level:      slog.LevelError,
			message:    "error message",
			wantStderr: "2026-11-25 12:34:56 ERROR    test: error message\n",
		},
		{
			level:      levelCritical,
			message:    "critical message",
			wantStderr: "2026-11-25 12:34:56 CRITICAL test: critical message\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.message, func(t *testing.T) {
			stdout.Reset()
			stderr.Reset()
			record := slog.NewRecord(timestamp, tt.level, tt.message, 0)
			if err := handler.Handle(context.Background(), record); err != nil {
				t.Fatalf("Handle() unexpected error: %v", err)
			}
			if stdout.String() != tt.wantStdout {
				t.Errorf("stdout = %q, want %q", stdout.String(), tt.wantStdout)
			}
			if stderr.String() != tt.wantStderr {
				t.Errorf("stderr = %q, want %q", stderr.String(), tt.wantStderr)
			}
		})
	}
}

func TestHandlerFiltersBelowMinimumLevel(t *testing.T) {
	var stdout, stderr strings.Builder
	handler := NewHandler(&stdout, &stderr, slog.LevelWarn, "test")
	timestamp := time.Date(2026, time.November, 25, 12, 34, 56, 0, time.UTC)

	for _, level := range []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError} {
		record := slog.NewRecord(timestamp, level, "message", 0)
		if err := handler.Handle(context.Background(), record); err != nil {
			t.Fatalf("Handle() unexpected error: %v", err)
		}
	}

	if stdout.Len() != 0 {
		t.Errorf("stdout = %q, want empty", stdout.String())
	}
	want := "2026-11-25 12:34:56 WARNING  test: message\n" +
		"2026-11-25 12:34:56 ERROR    test: message\n"
	if stderr.String() != want {
		t.Errorf("stderr = %q, want %q", stderr.String(), want)
	}
}

func TestHandlerFormatsAttributes(t *testing.T) {
	var stdout, stderr strings.Builder
	handler := NewHandler(&stdout, &stderr, slog.LevelDebug, "test")
	record := slog.NewRecord(time.Date(2026, time.November, 25, 12, 34, 56, 0, time.UTC), slog.LevelInfo, "hello", 0)
	record.AddAttrs(slog.String("key", "value"), slog.Int("count", 2))

	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatalf("Handle() unexpected error: %v", err)
	}
	want := "2026-11-25 12:34:56 INFO     test: hello key=value count=2\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
}

func TestHandlerWithAttrsAndGroup(t *testing.T) {
	var stdout, stderr strings.Builder
	handler := slog.New(NewHandler(&stdout, &stderr, slog.LevelDebug, "test")).
		With("component", "config").
		WithGroup("request").
		With("id", "abc").
		Handler()
	record := slog.NewRecord(time.Date(2026, time.November, 25, 12, 34, 56, 0, time.UTC), slog.LevelInfo, "hello", 0)
	record.AddAttrs(slog.String("action", "load"))

	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatalf("Handle() unexpected error: %v", err)
	}
	want := "2026-11-25 12:34:56 INFO     test: hello component=config request.id=abc request.action=load\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
}

func TestHandlerNameAttributeOverridesLoggerName(t *testing.T) {
	var stdout, stderr strings.Builder
	handler := NewHandler(&stdout, &stderr, slog.LevelDebug, "test")
	record := slog.NewRecord(time.Date(2026, time.November, 25, 12, 34, 56, 0, time.UTC), slog.LevelInfo, "hello", 0)
	record.AddAttrs(slog.String("name", "src.main"))

	if err := handler.Handle(context.Background(), record); err != nil {
		t.Fatalf("Handle() unexpected error: %v", err)
	}
	want := "2026-11-25 12:34:56 INFO     src.main: hello\n"
	if stdout.String() != want {
		t.Fatalf("stdout = %q, want %q", stdout.String(), want)
	}
}

func TestConfigureLoggingCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := ConfigureLogging(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("ConfigureLogging() error = %v, want context.Canceled", err)
	}
}
