package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/NikoMalek/job-sniper-discord-bot/internal/bot"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/config"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/data"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/matching"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/monitor"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/notifier"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/scrapers"
	"github.com/NikoMalek/job-sniper-discord-bot/internal/store"
)

const shutdownTimeout = 10 * time.Second

type runner interface {
	Run(context.Context) error
}

type runnerResult struct {
	name string
	err  error
}

func main() {
	if err := run(context.Background()); err != nil {
		slog.Error("Job Sniper stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) (err error) {
	settings, err := config.LoadContext(ctx)
	if err != nil {
		return fmt.Errorf("load settings: %w", err)
	}
	if err := config.ConfigureLogging(ctx); err != nil {
		return fmt.Errorf("configure logging: %w", err)
	}
	logStartup(settings)

	database, err := store.Open(ctx, settings.DBPath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() {
		if closeErr := database.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close database: %w", closeErr)
		}
	}()

	catalog, err := data.Load(ctx)
	if err != nil {
		return fmt.Errorf("load catalogs: %w", err)
	}
	matcher := matching.NewWithCatalog(catalog, nil)
	registry := scrapers.DefaultRegistry()

	client, err := bot.NewClient(bot.ClientOptions{
		Token:             settings.DiscordBotToken,
		Settings:          settings,
		Store:             database,
		Registry:          registry,
		Catalog:           catalog,
		Matcher:           matcher,
		ExperimentalStore: database,
	})
	if err != nil {
		return fmt.Errorf("create Discord client: %w", err)
	}

	session := client.Session()
	messageSender := notifier.NewDiscordMessageSender(session)
	jobSender := notifier.NewSender(notifier.SenderOptions{
		Messages: messageSender,
		Tracking: database,
	})
	mon, err := monitor.New(ctx, monitor.Options{
		Store:    database,
		Portals:  monitor.NewRegistryPortals(registry),
		Channels: bot.NewDiscordChannelResolver(session),
		Sender:   jobSender,
		Matcher:  matcher,
		Interval: intervalDuration(settings.CheckIntervalMinutes),
		Ready:    client.Ready(),
		Logger:   slog.Default(),
	})
	if err != nil {
		return fmt.Errorf("create monitor: %w", err)
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)

	err = runServices(ctx, client, mon, signals)
	if closeErr := client.Close(); closeErr != nil && err == nil {
		err = fmt.Errorf("close Discord client: %w", closeErr)
	}
	waitCtx, waitCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer waitCancel()
	if waitErr := mon.Wait(waitCtx); waitErr != nil && err == nil {
		err = fmt.Errorf("wait for monitor tasks: %w", waitErr)
	}
	return err
}

func logStartup(settings config.Settings) {
	slog.Info(fmt.Sprintf(
		"Starting Job Sniper: database=%s interval=%g min",
		settings.DBPath, settings.CheckIntervalMinutes,
	))
}

func intervalDuration(minutes float64) time.Duration {
	return time.Duration(minutes * float64(time.Minute))
}

func runServices(
	ctx context.Context, client, mon runner, signals <-chan os.Signal,
) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	results := make(chan runnerResult, 2)
	go func() { results <- runnerResult{name: "bot", err: client.Run(runCtx)} }()
	go func() { results <- runnerResult{name: "monitor", err: mon.Run(runCtx)} }()

	parentDone := ctx.Done()
	remaining := 2
	var firstErr error
	for remaining > 0 {
		select {
		case received, ok := <-signals:
			if !ok {
				signals = nil
				continue
			}
			slog.Info("Shutdown requested.", "signal", received)
			signals = nil
			cancel()
		case <-parentDone:
			parentDone = nil
			cancel()
		case result := <-results:
			remaining--
			if result.err != nil && firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", result.name, result.err)
			}
			if remaining > 0 {
				cancel()
			}
		}
	}
	return firstErr
}
