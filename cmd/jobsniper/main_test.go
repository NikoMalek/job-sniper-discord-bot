package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/NikoMalek/job-sniper-discord-bot/internal/config"
)

type fakeRunner struct {
	err       error
	immediate bool
	canceled  chan struct{}
}

func (r *fakeRunner) Run(ctx context.Context) error {
	if r.immediate {
		return r.err
	}
	<-ctx.Done()
	if r.canceled != nil {
		close(r.canceled)
	}
	return r.err
}

func captureLogger(t *testing.T) (*bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	previous := slog.Default()
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	slog.SetDefault(slog.New(config.NewHandler(stdout, stderr, slog.LevelDebug, "test")))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return stdout, stderr
}

func TestLogStartupUsesSpecifiedText(t *testing.T) {
	stdout, _ := captureLogger(t)
	logStartup(config.Settings{DBPath: "data/jobs.db", CheckIntervalMinutes: 5})
	want := "Starting Job Sniper: database=data/jobs.db interval=5 min"
	if !strings.Contains(stdout.String(), want) {
		t.Fatalf("stdout = %q, want it to contain %q", stdout.String(), want)
	}
}

func TestIntervalDuration(t *testing.T) {
	tests := []struct {
		minutes float64
		want    time.Duration
	}{
		{5, 5 * time.Minute},
		{2.5, 150 * time.Second},
		{1.0 / 60.0, time.Second},
	}
	for _, tt := range tests {
		if got := intervalDuration(tt.minutes); got != tt.want {
			t.Errorf("intervalDuration(%v) = %v, want %v", tt.minutes, got, tt.want)
		}
	}
}

func TestRunServicesStopsOnSignal(t *testing.T) {
	stdout, _ := captureLogger(t)
	botCanceled := make(chan struct{})
	monitorCanceled := make(chan struct{})
	client := &fakeRunner{canceled: botCanceled}
	mon := &fakeRunner{canceled: monitorCanceled}
	signals := make(chan os.Signal, 1)
	signals <- syscall.SIGTERM

	if err := runServices(context.Background(), client, mon, signals); err != nil {
		t.Fatalf("runServices() error = %v", err)
	}
	assertClosed(t, botCanceled, "bot context")
	assertClosed(t, monitorCanceled, "monitor context")
	if !strings.Contains(stdout.String(), "Shutdown requested.") {
		t.Fatalf("stdout = %q, want shutdown log", stdout.String())
	}
}

func TestRunServicesReturnsServiceErrorAndCancelsPeer(t *testing.T) {
	_, _ = captureLogger(t)
	wantErr := errors.New("bot failed")
	client := &fakeRunner{err: wantErr, immediate: true}
	monitorCanceled := make(chan struct{})
	mon := &fakeRunner{canceled: monitorCanceled}

	err := runServices(context.Background(), client, mon, nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("runServices() error = %v, want %v", err, wantErr)
	}
	assertClosed(t, monitorCanceled, "monitor context")
}

func TestRunServicesParentCancellationIsSilent(t *testing.T) {
	stdout, _ := captureLogger(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := runServices(ctx, &fakeRunner{}, &fakeRunner{}, nil); err != nil {
		t.Fatalf("runServices() error = %v", err)
	}
	if strings.Contains(stdout.String(), "Shutdown requested.") {
		t.Fatalf("stdout = %q, want no shutdown log", stdout.String())
	}
}

func assertClosed(t *testing.T, ch <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s cancellation", description)
	}
}
